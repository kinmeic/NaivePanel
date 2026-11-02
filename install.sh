#!/usr/bin/env bash
# NaivePanel 安装脚本 — Debian 11+ / Ubuntu 20.04+ (amd64 / arm64)
# 用法: sudo bash install.sh
set -euo pipefail

# ============ 可调参数 ============
# Go 版本：跟随最新稳定版，避免旧版标准库漏洞（Caddy v2.11.7 要求 go >= 1.26.0）
GO_VERSION="${NAIVEPANEL_GO_VERSION:-1.27.3}"
# 钉住的 Caddy 版本：v2.11.7 是当前最新稳定版，已覆盖迄今全部公开发布的安全公告
# （GHSA-6365-7ppr-5r92、CVE-2026-55458、CVE-2026-52844/52845/52846、CVE-2026-45692、
# CVE-2026-45135、CVE-2026-30851/30852、CVE-2026-27585~27590 等，修复版本均 <= v2.11.5）。
# 实测 v2.11.7 + 下面的 forwardproxy naive 提交编译通过，含 http.handlers.forward_proxy。
# 升级前先本地试编译再改（可用环境变量 NAIVEPANEL_CADDY_VERSION 临时覆盖）。
CADDY_PIN="${NAIVEPANEL_CADDY_VERSION:-v2.11.7}"
# forwardproxy：源为 github.com/klzgrad/forwardproxy 的 naive 分支（NaiveProxy 服务端）。
# 固定到 naive 分支提交 c096d6a（= 该仓库 tag v2.11.7-naive）：
#   * 仓库 module path 无 /v2，Go modules 不接受 v2.x tag，故只能写伪版本号；
#   * naive 分支 HEAD（e747854）依赖 caddyhttp.IdleTimeoutsFromContext，
#     该 API 只存在于未发布的 caddy master（v2.11.8-dev），配合 v2.11.7 会编译失败，
#     因此这里钉在能与稳定版 Caddy 配套的最新提交上，而不是跟随分支 HEAD。
FORWARDPROXY_PIN="${NAIVEPANEL_FORWARDPROXY_PIN:-v0.0.0-20261006113151-c096d6a00cb2}"
# xcaddy 也钉版本，避免 @latest 引入未预期变更
XCADDY_VERSION="${NAIVEPANEL_XCADDY_VERSION:-v0.4.7}"
GOPROXY_DEFAULT="https://goproxy.cn,direct"
PANEL_BIN="/usr/local/bin/naivepanel"
# 定制 Caddy 直接覆盖官方二进制（官方包用 apt-mark hold 锁定防升级回覆盖）
CADDY_BIN="/usr/bin/caddy"
PANEL_CONFIG_DIR="/etc/naivepanel"
PANEL_CONFIG="${PANEL_CONFIG_DIR}/config.yaml"
CADDY_SITES_DIR="/etc/caddy/sites"
# ==================================

RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; BLUE=$'\033[34m'; NC=$'\033[0m'
info()  { echo "${BLUE}[*]${NC} $*"; }
ok()    { echo "${GREEN}[✓]${NC} $*"; }
warn()  { echo "${YELLOW}[!]${NC} $*"; }
die()   { echo "${RED}[✗]${NC} $*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "请用 root 运行: sudo bash install.sh"
command -v apt-get >/dev/null || die "仅支持 apt 系发行版（Debian / Ubuntu）"

ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
  amd64|arm64) ;;
  *) die "不支持的架构: $ARCH（仅支持 amd64 / arm64）" ;;
esac
GO_ARCH="$ARCH"
ok "系统架构: $ARCH"

export DEBIAN_FRONTEND=noninteractive

# ---------- 0. 临时 swap（仅在实际需要编译 Caddy 时创建） ----------
# 跳过 Caddy 编译时不需要 swap，因此这里只定义函数，真正的创建推迟到编译前调用。
SWAP_CREATED=0
ensure_build_swap() {
  local total_mb
  total_mb=$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo)
  [[ "$total_mb" -lt 1500 ]] || return 0
  warn "内存 ${total_mb}MB，编译 Caddy 可能 OOM，创建 2G 临时 swap"
  if [[ ! -f /swapfile.naivepanel ]]; then
    fallocate -l 2G /swapfile.naivepanel || dd if=/dev/zero of=/swapfile.naivepanel bs=1M count=2048
    chmod 600 /swapfile.naivepanel
    mkswap /swapfile.naivepanel >/dev/null
    swapon /swapfile.naivepanel
    SWAP_CREATED=1
  fi
}

# ---------- 1. 交互询问 ----------
echo
echo "========== NaivePanel 安装配置 =========="
read -rp "服务器域名（首个站点 & 面板寄宿域名）: " DOMAIN
[[ -n "$DOMAIN" ]] || die "域名不能为空"
[[ "$DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || die "域名含非法字符（仅允许字母、数字、点、连字符）"

DEFAULT_PATH="/manage-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
read -rp "面板访问路径 [${DEFAULT_PATH}]: " BASE_PATH
BASE_PATH="${BASE_PATH:-$DEFAULT_PATH}"
[[ "$BASE_PATH" == /* ]] || BASE_PATH="/$BASE_PATH"
[[ "$BASE_PATH" =~ ^/[A-Za-z0-9/_-]+$ ]] || die "面板路径含非法字符（仅允许字母、数字、/、_、-）"

read -rp "管理员账号 [admin]: " ADMIN_USER
ADMIN_USER="${ADMIN_USER:-admin}"
[[ "$ADMIN_USER" =~ ^[A-Za-z0-9_-]+$ ]] || die "管理员账号含非法字符（仅允许字母、数字、_、-）"

while true; do
  read -rsp "管理员密码（至少 10 位）: " ADMIN_PASS; echo
  read -rsp "再次输入密码: " ADMIN_PASS2; echo
  [[ "$ADMIN_PASS" == "$ADMIN_PASS2" ]] || { warn "两次输入不一致"; continue; }
  [[ ${#ADMIN_PASS} -ge 10 ]] || { warn "密码至少 10 位"; continue; }
  break
done

read -rp "Go 模块代理 [${GOPROXY_DEFAULT}]: " GOPROXY
GOPROXY="${GOPROXY:-$GOPROXY_DEFAULT}"
ok "配置确认: 域名=$DOMAIN 路径=$BASE_PATH 账号=$ADMIN_USER"

# ---------- 2. 基础依赖 ----------
info "安装基础依赖..."
apt-get update -qq
apt-get install -y -qq curl wget tar cron gnupg lsb-release ca-certificates debian-keyring debian-archive-keyring apt-transport-https >/dev/null
systemctl enable --now cron >/dev/null 2>&1 || true
ok "基础依赖就绪"

# ---------- 3. 安装 Go ----------
fetch_go_sha256() {
  # 取 Go 官方发布包的 SHA256。
  # 注意：https://go.dev/dl/<file>.sha256 会返回 HTTP 200 的 HTML（go-import 重定向页），
  # curl -f 不会报错，必须校验格式；真正的 .sha256 文件由 dl.google.com 提供。
  local tarball="$1" sha json
  if sha="$(curl -fsSL --retry 3 --retry-delay 2 "https://dl.google.com/go/${tarball}.sha256" 2>/dev/null)"; then
    sha="$(printf '%s' "$sha" | tr -d '[:space:]')"
    [[ "$sha" =~ ^[0-9a-fA-F]{64}$ ]] && { printf '%s' "$sha"; return 0; }
  fi
  # 兜底：解析官方 JSON 清单（不依赖 python3 / jq）：先压平换行，再按对象 "{" 切分，
  # 使同一发布包的 filename 与 sha256 落在同一行
  if json="$(curl -fsSL --retry 3 --retry-delay 2 "https://go.dev/dl/?mode=json&include=all" 2>/dev/null)"; then
    sha="$(printf '%s' "$json" | tr -d '\n\r' | tr '{' '\n' \
      | grep -F "\"filename\": \"${tarball}\"" \
      | grep -o '"sha256"[[:space:]]*:[[:space:]]*"[0-9a-fA-F]\{64\}"' | head -n1 | cut -d'"' -f4)"
    [[ "$sha" =~ ^[0-9a-fA-F]{64}$ ]] && { printf '%s' "$sha"; return 0; }
  fi
  return 1
}

install_go() {
  info "下载并安装 Go ${GO_VERSION} (${GO_ARCH})..."
  local tarball="go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
  local tmp_tar="/tmp/${tarball}"
  local go_sha
  # 先取校验值再下载，避免白下载；取不到就中止（不跳过校验）
  go_sha="$(fetch_go_sha256 "$tarball")" \
    || die "无法获取 Go ${GO_VERSION} 官方 SHA256（网络受限？请检查出网 DNS/代理后重试）"
  curl -fsSL --retry 3 --retry-delay 2 "https://dl.google.com/go/${tarball}" -o "$tmp_tar" \
    || curl -fsSL --retry 3 --retry-delay 2 "https://go.dev/dl/${tarball}" -o "$tmp_tar" \
    || die "Go 安装包下载失败: ${tarball}"
  [[ -s "$tmp_tar" ]] || die "Go 安装包为空: ${tmp_tar}"
  echo "${go_sha}  ${tmp_tar}" | sha256sum -c - >/dev/null \
    || { rm -f "$tmp_tar"; die "Go 下载包 SHA256 校验失败（下载被劫持/镜像不一致？）"; }
  tar -tzf "$tmp_tar" >/dev/null 2>&1 \
    || { rm -f "$tmp_tar"; die "Go 安装包已损坏（gzip 校验失败）"; }
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmp_tar"
  rm -f "$tmp_tar"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
}
CURRENT_GO=""
if command -v go >/dev/null; then
  CURRENT_GO="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
fi
if [[ -n "$CURRENT_GO" && "$(printf '%s\n' "$GO_VERSION" "$CURRENT_GO" | sort -V | head -n1)" == "$GO_VERSION" ]]; then
  ok "Go 已安装: $(go version | awk '{print $3}')"
else
  install_go
  ok "Go 安装完成: $(go version | awk '{print $3}')"
fi
export GOPROXY
export PATH="$PATH:/usr/local/go/bin:/root/go/bin"

# ---------- 4. Caddy 检测与安装 ----------
# CADDY_FOUND: 是否已装 caddy；CADDY_HAS_FP: 其是否已含 forward_proxy 模块
CADDY_FOUND=0
CADDY_HAS_FP=0
CADDY_PATH=""
CADDY_DECISION="install"
if command -v caddy >/dev/null || dpkg -l caddy 2>/dev/null | grep -q '^ii'; then
  CADDY_FOUND=1
  CADDY_PATH="$(command -v caddy || true)"
  if [[ -n "$CADDY_PATH" ]] \
    && "$CADDY_PATH" list-modules 2>/dev/null | grep -qx 'http.handlers.forward_proxy'; then
    CADDY_HAS_FP=1
  fi
fi

if [[ "$CADDY_HAS_FP" -eq 1 ]]; then
  # 已装 caddy + forwardproxy：新增"跳过"选项，避免为了装面板白编译一次
  warn "检测到已安装 Caddy + forward_proxy: $CADDY_PATH"
  CADDY_CUR_VER="$("$CADDY_PATH" version 2>/dev/null | awk '{print $1}')"
  info "现有版本: ${CADDY_CUR_VER:-未知}（脚本钉住: ${CADDY_PIN}）"
  [[ "$CADDY_CUR_VER" == "$CADDY_PIN" ]] \
    || warn "版本与脚本钉住的不一致；如需对齐，请选 1 重新编译"
  echo "  1) 重新编译定制 Caddy（覆盖现有二进制，可选）"
  echo "  2) 跳过 Caddy 安装，直接部署面板（默认，保留现有二进制）"
  echo "  3) 中止安装"
  read -rp "请选择 [2]: " CADDY_DECISION
  CADDY_DECISION="${CADDY_DECISION:-2}"
  case "$CADDY_DECISION" in
    1|rebuild) CADDY_DECISION=1 ;;
    2|skip) CADDY_DECISION=skip ;;
    3) CADDY_DECISION=3 ;;
    *) die "无效选择" ;;
  esac
elif [[ "$CADDY_FOUND" -eq 1 ]]; then
  warn "检测到已安装 Caddy: ${CADDY_PATH:-（包已装,二进制未知）}，但缺少 http.handlers.forward_proxy 模块"
  echo "  1) 使用现有安装（推荐，仅替换二进制为定制版）"
  echo "  2) 重新安装官方 Caddy"
  echo "  3) 中止安装"
  read -rp "请选择 [1]: " CADDY_DECISION
  CADDY_DECISION="${CADDY_DECISION:-1}"
  # 缺 forward_proxy 时不存在"跳过"：只能编译/重装或中止
  case "$CADDY_DECISION" in
    1|install|skip) CADDY_DECISION=1 ;;
    2|3) ;;
    *) die "无效选择" ;;
  esac
fi

# 未安装 Caddy：不询问，直接走官方仓库安装 + 编译（与旧行为一致）
if [[ "$CADDY_FOUND" -eq 0 ]]; then
  CADDY_DECISION=install
fi

# 跳过编译的前提：现有 caddy 必须由 systemd 托管，否则后续 reload/restart 无从谈起
if [[ "$CADDY_DECISION" == "skip" ]] && ! systemctl cat caddy >/dev/null 2>&1; then
  warn "现有 caddy 没有 systemd 服务单元（systemctl cat caddy 为空）"
  echo "  1) 改为重新编译定制 Caddy（按官方 unit 注册服务）"
  echo "  2) 中止安装"
  read -rp "请选择 [1]: " NO_UNIT_DECISION
  case "${NO_UNIT_DECISION:-1}" in
    1) CADDY_DECISION=1 ;;
    *) die "现有 Caddy 无 systemd 服务单元，无法继续" ;;
  esac
fi

case "$CADDY_DECISION" in
  1|2|install)
    # 1 = 用现有/新装官方包后编译定制二进制；2 = 重装官方包后再编译
    CADDY_NEWLY_INSTALLED=0
    if [[ "$CADDY_DECISION" == "2" ]]; then
      info "重新安装官方 Caddy..."
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
        | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
        > /etc/apt/sources.list.d/caddy-stable.list
      apt-get update -qq
      apt-get install -y -qq --reinstall caddy >/dev/null
    elif ! command -v caddy >/dev/null; then
      info "通过官方仓库安装 Caddy..."
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
        | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
        > /etc/apt/sources.list.d/caddy-stable.list
      apt-get update -qq
      apt-get install -y -qq caddy >/dev/null
      CADDY_NEWLY_INSTALLED=1
    fi
    ;;
  3) die "用户中止安装" ;;
  skip) ;;
  *) die "无效选择" ;;
esac

# 走编译流程时会覆盖现有二进制，先停服务避免 "text file busy"；跳过时不动正在服务的 Caddy
[[ "$CADDY_DECISION" == "skip" ]] || systemctl stop caddy >/dev/null 2>&1 || true

# 只有本次才装的官方包需要"启动一次生成默认配置与目录结构"；
# 已有安装在跑时不动它，配置交接留给面板首次 Apply（它自带校验/备份/回滚）
if [[ "${CADDY_NEWLY_INSTALLED:-0}" -eq 1 ]]; then
  info "初始化 Caddy 默认配置..."
  systemctl enable caddy >/dev/null 2>&1 || true
  systemctl start caddy 2>/dev/null || true
  sleep 2
  systemctl stop caddy 2>/dev/null || true
  ok "Caddy 官方安装完成"
fi

# 以实际安装路径为准（通常为 /usr/bin/caddy）
CADDY_BIN="$(command -v caddy)"
[[ -n "$CADDY_BIN" && -x "$CADDY_BIN" ]] || die "未找到 caddy 可执行文件（官方包安装失败？请检查上一步输出）"
if [[ "$CADDY_DECISION" == "skip" ]]; then
  # 跳过编译时对外服务中的 Caddy 不能动；没有 systemd 单元则后续 reload 无从谈起
  if ! systemctl cat caddy >/dev/null 2>&1; then
    die "现有 caddy 没有 systemd 服务单元（systemctl cat caddy 为空），请先把 Caddy 配成服务，或重新运行并选择重新编译"
  fi
  systemctl is-active --quiet caddy || warn "caddy 服务当前未运行，安装完成后会被启动"
fi

# ---------- 5. xcaddy 编译定制 Caddy（选择 2 跳过时整段略过） ----------
if [[ "$CADDY_DECISION" == "skip" ]]; then
  ok "跳过 Caddy 编译与二进制覆盖，沿用现有 Caddy: $CADDY_BIN（$(cd / && "$CADDY_BIN" version 2>/dev/null | awk '{print $1, $2}')）"
else
  ensure_build_swap
  info "安装 xcaddy ${XCADDY_VERSION}..."
  go install "github.com/caddyserver/xcaddy/cmd/xcaddy@${XCADDY_VERSION}"
  XCADDY="$(go env GOPATH)/bin/xcaddy"
  [[ -x "$XCADDY" ]] || XCADDY="/root/go/bin/xcaddy"
  [[ -x "$XCADDY" ]] || die "xcaddy 安装失败"

  info "编译定制 Caddy: ${CADDY_PIN} + forwardproxy(naive) ${FORWARDPROXY_PIN}（约 3-8 分钟）..."
  BUILD_ARGS=(build "$CADDY_PIN" --with "github.com/caddyserver/forwardproxy=github.com/klzgrad/forwardproxy@${FORWARDPROXY_PIN}")
  TMP_CADDY="$(mktemp -d)/caddy"
  (cd "$(dirname "$TMP_CADDY")" && "$XCADDY" "${BUILD_ARGS[@]}") \
    || die "Caddy 编译失败。若刚改了版本组合，请先在本地试编译；也可用 NAIVEPANEL_FORWARDPROXY_PIN=naive 跟随分支 HEAD（需配合未发布的 caddy master）"
  [[ -x "$TMP_CADDY" ]] || die "编译产物缺失: $TMP_CADDY"

  # 校验产物：版本号与 forward_proxy 模块都必须对上，避免装了不匹配的二进制
  BUILT_VER="$("$TMP_CADDY" version 2>/dev/null | awk '{print $1}')"
  [[ "$BUILT_VER" == "$CADDY_PIN" ]] || die "编译出的 Caddy 版本为 ${BUILT_VER}，与钉住的 ${CADDY_PIN} 不符"
  "$TMP_CADDY" list-modules | grep -qx 'http.handlers.forward_proxy' \
    || die "编译产物缺少 http.handlers.forward_proxy 模块"
  ok "产物校验通过: ${BUILT_VER} + http.handlers.forward_proxy"

  # 备份官方二进制（仅首次），然后用定制构建直接覆盖官方路径
  [[ -f "${CADDY_BIN}.official" ]] || cp -a "$CADDY_BIN" "${CADDY_BIN}.official"
  install -m 0755 "$TMP_CADDY" "$CADDY_BIN"
  rm -rf "$(dirname "$TMP_CADDY")"
  ok "定制 Caddy 已覆盖官方二进制 $CADDY_BIN（原版备份于 ${CADDY_BIN}.official）"

  # 锁定官方包，防止 apt 升级把定制二进制覆盖回去（caddy 若为二进制自装、非 apt 包则跳过）；清理旧方案的 drop-in（如有）
  apt-mark hold caddy >/dev/null 2>&1 || warn "apt-mark hold 失败（caddy 可能不是 apt 包），请自行确保持续使用定制二进制"
  rm -f /etc/systemd/system/caddy.service.d/naivepanel.conf
  systemctl daemon-reload >/dev/null 2>&1 || true
  ok "caddy.service 沿用官方 unit，二进制已是定制版（apt-mark hold 已锁定官方包）"
fi

# ---------- 6. 部署面板 ----------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${SCRIPT_DIR}/go.mod" ]]; then
  info "从源码构建面板..."
  (cd "$SCRIPT_DIR" && go build -trimpath -ldflags="-s -w -X main.version=$(date +%Y%m%d)" -o "$PANEL_BIN" ./cmd/naivepanel)
else
  die "未找到面板源码（请在 NaivePanel 仓库根目录运行本脚本）"
fi
ok "面板二进制: $PANEL_BIN"

# ---------- 7. 生成面板配置 ----------
mkdir -p "$PANEL_CONFIG_DIR" "$CADDY_SITES_DIR"
chmod 700 "$PANEL_CONFIG_DIR"
PASS_HASH="$(printf '%s\n' "$ADMIN_PASS" | "$PANEL_BIN" hash-password 2>/dev/null)"
unset ADMIN_PASS ADMIN_PASS2
# 面板 HTTPS 门禁共享密钥：Caddy 反代注入 header_up，面板拒绝无此头的请求
PROXY_TOKEN="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"

if [[ -f "$PANEL_CONFIG" ]]; then
  warn "面板配置已存在，备份为 ${PANEL_CONFIG}.bak.$(date +%s)"
  cp "$PANEL_CONFIG" "${PANEL_CONFIG}.bak.$(date +%s)"
fi
cat > "$PANEL_CONFIG" <<EOF
listen: 127.0.0.1:9000
domain: $DOMAIN
base_path: $BASE_PATH
admin_user: $ADMIN_USER
admin_pass_hash: '$PASS_HASH'
totp_enabled: false
host_site: $DOMAIN
session_ttl_hours: 12
proxy_token: '$PROXY_TOKEN'
caddy:
  bin: $CADDY_BIN
  main_file: /etc/caddy/Caddyfile
  sites_dir: $CADDY_SITES_DIR
bypasscore:
  socks_port: 1080
  bin_path: /usr/local/bin/bypasscore
  config_path: /etc/bypasscore/config.json
  control_sock: /run/bypasscore/control.sock
  work_dir: /etc/bypasscore
geo:
  dir: /etc/bypasscore
backup_dir: $PANEL_CONFIG_DIR/backups
sites:
  - domain: $DOMAIN
    forward_proxy:
      enabled: false
      accounts: []
    web:
      type: static
      root: /var/www/$DOMAIN
EOF
chmod 600 "$PANEL_CONFIG"
ok "面板配置: $PANEL_CONFIG"

mkdir -p "/var/www/$DOMAIN"
cat > "/var/www/$DOMAIN/index.html" <<EOF
<!DOCTYPE html><html><head><meta charset="utf-8"><title>Welcome</title></head>
<body><h1>It works.</h1></body></html>
EOF

# ---------- 8. systemd 服务 ----------
# 服务单元内容由二进制内置（naivepanel install），此处仅注册并设为开机启动；
# 首次启动留到 Caddy 配置渲染完成后统一 restart。
"$PANEL_BIN" install -config "$PANEL_CONFIG" -bin "$PANEL_BIN" -start=false

# ---------- 9. 渲染初始 Caddy 配置并启动 ----------
info "生成初始 Caddy 配置..."
# 主配置处理策略：
#   - 文件不存在 / 只有空白：直接写 NaivePanel 骨架；
#   - 已是 NaivePanel 布局（上次安装留下的）：保留，下方只做增量补齐；
#   - apt 刚装的官方包自带示例配置（:80 提供 /usr/share/caddy，未承载业务）：可安全替换；
#   - 机器上本来就在跑的配置（含跳过编译的情形）：默认只增量修改 + 备份，绝不覆盖。
CADDYFILE=/etc/caddy/Caddyfile
CADDYFILE_BAK=""
write_caddyfile_skeleton() {
  cat > "$CADDYFILE" <<EOF
{
	email admin@$DOMAIN
	order forward_proxy before file_server
}

import $CADDY_SITES_DIR/*.caddy
EOF
}
backup_caddyfile() {
  CADDYFILE_BAK="${CADDYFILE}.bak.$(date +%Y%m%d%H%M%S)"
  cp -a "$CADDYFILE" "$CADDYFILE_BAK"
}
# 官方 deb 包自带的示例配置只有这一句注释 + :80 静态站，没有真实业务
is_stock_caddyfile() {
  grep -q 'Caddyfile for Caddy web server' "$CADDYFILE" 2>/dev/null \
    && grep -q 'root \* /usr/share/caddy' "$CADDYFILE" 2>/dev/null
}

if [[ ! -e "$CADDYFILE" ]] || ! grep -qE '[^[:space:]]' "$CADDYFILE"; then
  # 文件不存在或只有空白：直接写骨架
  write_caddyfile_skeleton
elif is_stock_caddyfile && [[ "$CADDY_DECISION" != "skip" ]]; then
  # 未改动过的官方示例配置：替换成骨架，无需询问
  backup_caddyfile
  write_caddyfile_skeleton
  ok "已用 NaivePanel 骨架替换官方示例配置（备份 $CADDYFILE_BAK）"
elif grep -qE 'order\s+forward_proxy' "$CADDYFILE" \
  || grep -qE "import\s+${CADDY_SITES_DIR}/\*\.caddy" "$CADDYFILE"; then
  # NaivePanel 布局（可能是上次安装留下的）：保留，只做增量补齐
  backup_caddyfile
  ok "沿用现有 NaivePanel 主配置（备份 $CADDYFILE_BAK），下方只做增量补齐"
else
  # 机器上本来就在跑的配置：默认只增量修改，绝不覆盖
  backup_caddyfile
  warn "检测到已有的、非 NaivePanel 生成的 $CADDYFILE，已备份为 $CADDYFILE_BAK"
  echo "  1) 保留现有配置，仅追加 import / order（推荐，已有站点继续服务）"
  echo "  2) 替换为 NaivePanel 骨架（现有内容只留在备份里）"
  read -rp "请选择 [1]: " CADDYFILE_DECISION
  if [[ "${CADDYFILE_DECISION:-1}" == "2" ]]; then
    write_caddyfile_skeleton
    warn "主配置已替换为 NaivePanel 骨架，原配置见 $CADDYFILE_BAK"
  fi
fi

# 主配置里没有 import 我们的片段目录时，追加一行（只加不改）
if grep -qE "^\s*import\b.*sites/\*\.caddy\s*$" "$CADDYFILE"; then
  ok "主配置已导入站点片段目录"
else
  [[ "$CADDY_DECISION" == "skip" ]] && warn "现有主配置没有导入 ${CADDY_SITES_DIR}/*.caddy，追加一行"
  # 补换行，避免与文件末尾的既有内容粘在同一行
  [[ -z "$(tail -c1 "$CADDYFILE")" ]] || echo >>"$CADDYFILE"
  printf '\nimport %s/*.caddy\n' "$CADDY_SITES_DIR" >>"$CADDYFILE"
fi

# 全局块里缺少 forward_proxy 指令顺序声明时补一条（已有任何 order forward_proxy 则不动）
if ! grep -qE "^\s*order\s+forward_proxy\b" "$CADDYFILE"; then
  if grep -qE '^\{\s*$' "$CADDYFILE"; then
    # 有全局块：在块首行后插入，保持 tab 缩进（与面板渲染一致）
    sed -i '0,/^{[[:space:]]*$/s//{\n\torder forward_proxy before file_server/' "$CADDYFILE"
    ok "已在全局块补 order forward_proxy before file_server"
  else
    # 没有全局块：在文件开头新增一个最小全局块（纯增量，不影响既有站点块）
    printf '{\n\torder forward_proxy before file_server\n}\n\n' | cat - "$CADDYFILE" >"${CADDYFILE}.tmp" \
      && mv "${CADDYFILE}.tmp" "$CADDYFILE"
    ok "已在文件开头新增全局块并声明 order forward_proxy before file_server"
  fi
fi

# 同名站点片段已存在时先备份，避免覆盖运营者或上一次安装的配置
SNIPPET="${CADDY_SITES_DIR}/${DOMAIN}.caddy"
SNIPPET_BAK=""
if [[ -f "$SNIPPET" ]]; then
  SNIPPET_BAK="${SNIPPET}.bak.$(date +%Y%m%d%H%M%S)"
  cp -a "$SNIPPET" "$SNIPPET_BAK"
fi

# 主配置里可能已经有该域名的站点块：Caddy 不允许同一地址出现两次，提前提示
if grep -qE "(^|[[:space:],])${DOMAIN//./\\.}([[:space:],{]|$)" "$CADDYFILE"; then
  warn "主配置 $CADDYFILE 中已经出现 ${DOMAIN}"
  warn "若它是一个站点块，面板为同一域名写的片段会与其冲突（caddy validate 会报 duplicate site address）"
  warn "可改用面板寄宿域名以外的域名，或先自行移走该块（原文件已备份）"
fi
cat > "$SNIPPET" <<EOF
:443, $DOMAIN {
	handle $BASE_PATH/* {
		reverse_proxy 127.0.0.1:9000 {
			header_up X-NaivePanel-Key $PROXY_TOKEN
		}
	}
	redir $BASE_PATH $BASE_PATH/ 308

	root * /var/www/$DOMAIN
	encode gzip zstd
	file_server
}
EOF
chmod 644 "$CADDYFILE"
chmod 755 "$CADDY_SITES_DIR"

# 站点片段权限要和 caddy 服务进程的运行身份匹配（Debian/Ubuntu 官方 unit 是 User=caddy）：
# 0600 root:root 会让 caddy 在 import 时 permission denied 直接启动失败，
# 而下面以 root 跑的 validate 却依然通过，所以这里显式给 root:caddy 0640。
CADDY_SVC_USER="$(systemctl show caddy -p User --value 2>/dev/null || true)"
if [[ -z "$CADDY_SVC_USER" || "$CADDY_SVC_USER" == "root" ]]; then
  chmod 644 "$SNIPPET"
else
  chown root:"$CADDY_SVC_USER" "$SNIPPET" 2>/dev/null \
    && chmod 640 "$SNIPPET" \
    || chmod 644 "$SNIPPET"
fi
ok "站点片段权限: $(stat -c '%U:%G %a' "$SNIPPET")（caddy 服务用户: ${CADDY_SVC_USER:-root}）"

# 关键：以 caddy 服务用户身份再校验一次。root 能读不代表 caddy 能读，
# 只以 root 校验会漏掉权限问题，直到 systemd 启动失败才暴露。
validate_as_caddy_user() {
  local user="${CADDY_SVC_USER:-root}"
  [[ "$user" == "root" ]] && return 0
  command -v runuser >/dev/null || command -v su >/dev/null || return 0
  if command -v runuser >/dev/null; then
    runuser -u "$user" -- "$CADDY_BIN" validate --config "$CADDYFILE" --adapter caddyfile
  else
    su -s /bin/sh "$user" -c "'$CADDY_BIN' validate --config '$CADDYFILE' --adapter caddyfile"
  fi
}

"$CADDY_BIN" validate --config "$CADDYFILE" --adapter caddyfile \
  || die "初始 Caddy 配置校验失败，请检查 $CADDYFILE 与 $SNIPPET"
validate_as_caddy_user \
  || die "以 caddy 服务用户（${CADDY_SVC_USER}）校验失败：多半是 $CADDY_SITES_DIR 下片段/目录权限不足"

# 先 reload：跳过编译时 Caddy 正在对外服务，reload 不中断现有连接；不可用再 restart 兜底
if ! systemctl reload caddy 2>/dev/null; then
  systemctl restart caddy
fi
systemctl restart naivepanel
sleep 2
if ! systemctl is-active --quiet caddy; then
  journalctl -u caddy --no-pager -n 20 || true
  # 回退到安装前的配置，避免面板装好了却把在跑的站点搞挂
  if [[ -n "${CADDYFILE_BAK:-}" && -f "$CADDYFILE_BAK" ]]; then
    warn "回退主配置到安装前版本: $CADDYFILE_BAK"
    cp -a "$CADDYFILE_BAK" "$CADDYFILE"
  fi
  if [[ -n "${SNIPPET_BAK:-}" && -f "$SNIPPET_BAK" ]]; then
    warn "回退站点片段到安装前版本: $SNIPPET_BAK"
    cp -a "$SNIPPET_BAK" "$SNIPPET"
  fi
  if systemctl restart caddy 2>/dev/null; then
    die "新配置导致 caddy 启动失败，已回退到安装前配置并恢复服务"
  fi
  die "caddy 启动失败且回退未恢复服务: journalctl -xeu caddy"
fi
systemctl is-active --quiet naivepanel || die "naivepanel 启动失败: journalctl -u naivepanel"

# ---------- 10. 清理临时 swap ----------
if [[ "$SWAP_CREATED" -eq 1 ]]; then
  read -rp "编译用的临时 swap 已完成使命，是否删除？[Y/n]: " DEL_SWAP
  if [[ "${DEL_SWAP:-Y}" =~ ^[Yy]$ ]]; then
    swapoff /swapfile.naivepanel && rm -f /swapfile.naivepanel
    ok "临时 swap 已删除"
  fi
fi

# ---------- 完成 ----------
echo
echo "=================================================="
ok "NaivePanel 安装完成！"
echo
echo "  面板地址: ${GREEN}https://${DOMAIN}${BASE_PATH}/${NC}"
echo "  账号:     $ADMIN_USER"
echo "  密码:     （安装时输入的密码）"
echo
warn "面板路径只显示这一次，请立即保存！"
warn "建议登录后立刻到「设置」启用 MFA 两步验证。"
echo "=================================================="
