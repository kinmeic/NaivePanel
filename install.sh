#!/usr/bin/env bash
# NaivePanel 安装脚本 — Debian 11+ / Ubuntu 20.04+ (amd64 / arm64)
# 用法: sudo bash install.sh
set -euo pipefail

# ============ 可调参数 ============
GO_VERSION="1.26.5"
# 钉住的 Caddy 版本。已实测 v2.10.2 + klzgrad/forwardproxy@naive 编译通过
# 且包含 http.handlers.forward_proxy；升级前先本地试编译再改。
CADDY_PIN="v2.10.2"
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

# ---------- 0. 内存检查（xcaddy 编译需要 ~1.5G+） ----------
SWAP_CREATED=0
TOTAL_MEM_MB=$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo)
if [[ "$TOTAL_MEM_MB" -lt 1500 ]]; then
  warn "内存 ${TOTAL_MEM_MB}MB，编译 Caddy 可能 OOM，创建 2G 临时 swap"
  if [[ ! -f /swapfile.naivepanel ]]; then
    fallocate -l 2G /swapfile.naivepanel || dd if=/dev/zero of=/swapfile.naivepanel bs=1M count=2048
    chmod 600 /swapfile.naivepanel
    mkswap /swapfile.naivepanel >/dev/null
    swapon /swapfile.naivepanel
    SWAP_CREATED=1
  fi
fi

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
apt-get install -y -qq curl wget tar gnupg lsb-release ca-certificates debian-keyring debian-archive-keyring apt-transport-https >/dev/null
ok "基础依赖就绪"

# ---------- 3. 安装 Go ----------
install_go() {
  info "下载并安装 Go ${GO_VERSION} (${GO_ARCH})..."
  local tarball="go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
  curl -fsSL "https://go.dev/dl/${tarball}" -o "/tmp/${tarball}" \
    || curl -fsSL "https://dl.google.com/go/${tarball}" -o "/tmp/${tarball}"
  curl -fsSL "https://go.dev/dl/${tarball}.sha256" -o "/tmp/${tarball}.sha256" \
    || curl -fsSL "https://dl.google.com/go/${tarball}.sha256" -o "/tmp/${tarball}.sha256"
  local go_sha
  go_sha="$(tr -d '[:space:]' < "/tmp/${tarball}.sha256")"
  [[ "$go_sha" =~ ^[0-9a-fA-F]{64}$ ]] || die "Go 下载校验文件格式异常"
  echo "${go_sha}  /tmp/${tarball}" | sha256sum -c - >/dev/null \
    || die "Go 下载包 SHA256 校验失败"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${tarball}"
  rm -f "/tmp/${tarball}" "/tmp/${tarball}.sha256"
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
CADDY_DECISION="install"
if command -v caddy >/dev/null || dpkg -l caddy 2>/dev/null | grep -q '^ii'; then
  warn "检测到已安装 Caddy: $(command -v caddy || echo '(包已装,二进制未知)')"
  echo "  1) 使用现有安装（推荐，仅替换二进制为定制版）"
  echo "  2) 重新安装官方 Caddy"
  echo "  3) 中止安装"
  read -rp "请选择 [1]: " CADDY_DECISION
  CADDY_DECISION="${CADDY_DECISION:-1}"
fi

case "$CADDY_DECISION" in
  1|install)
    if ! command -v caddy >/dev/null; then
      info "通过官方仓库安装 Caddy..."
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
        | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
        > /etc/apt/sources.list.d/caddy-stable.list
      apt-get update -qq
      apt-get install -y -qq caddy >/dev/null
    fi
    ;;
  2)
    info "重新安装官方 Caddy..."
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
      > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq
    apt-get install -y -qq --reinstall caddy >/dev/null
    ;;
  3) die "用户中止安装" ;;
  *) die "无效选择" ;;
esac

# 启动一次生成默认配置与目录结构，然后停止
info "初始化 Caddy 默认配置..."
systemctl enable caddy >/dev/null 2>&1 || true
systemctl start caddy 2>/dev/null || true
sleep 2
systemctl stop caddy 2>/dev/null || true
ok "Caddy 官方安装完成"

# 以官方包实际安装路径为准（通常为 /usr/bin/caddy）
CADDY_BIN="$(command -v caddy)"

# ---------- 5. xcaddy 编译定制 Caddy ----------
info "安装 xcaddy..."
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
XCADDY="$(go env GOPATH)/bin/xcaddy"
[[ -x "$XCADDY" ]] || XCADDY="/root/go/bin/xcaddy"

info "编译定制 Caddy（含 forwardproxy naive 分支，约 3-8 分钟）..."
BUILD_ARGS=(build)
[[ -n "$CADDY_PIN" ]] && BUILD_ARGS+=("$CADDY_PIN")
BUILD_ARGS+=(--with "github.com/caddyserver/forwardproxy=github.com/klzgrad/forwardproxy@naive")
TMP_CADDY="$(mktemp -d)/caddy"
(cd "$(dirname "$TMP_CADDY")" && "$XCADDY" "${BUILD_ARGS[@]}")
mv "$(dirname "$TMP_CADDY")/caddy" "$TMP_CADDY"

# 备份官方二进制（仅首次），然后用定制构建直接覆盖官方路径
[[ -f "${CADDY_BIN}.official" ]] || cp -a "$CADDY_BIN" "${CADDY_BIN}.official"
install -m 0755 "$TMP_CADDY" "$CADDY_BIN"
rm -rf "$(dirname "$TMP_CADDY")"
ok "定制 Caddy 已覆盖官方二进制 $CADDY_BIN（原版备份于 ${CADDY_BIN}.official）"

# 锁定官方包，防止 apt 升级把定制二进制覆盖回去；清理旧方案的 drop-in（如有）
apt-mark hold caddy >/dev/null
rm -f /etc/systemd/system/caddy.service.d/naivepanel.conf
systemctl daemon-reload
ok "caddy.service 沿用官方 unit，二进制已是定制版（apt-mark hold 已锁定官方包）"

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
  auto_update_weekly: false
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
cat > /etc/caddy/Caddyfile <<EOF
{
	email admin@$DOMAIN
}

import $CADDY_SITES_DIR/*.caddy
EOF

cat > "$CADDY_SITES_DIR/$DOMAIN.caddy" <<EOF
:443, $DOMAIN {
	route {
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
}
EOF
chmod 600 "$CADDY_SITES_DIR/$DOMAIN.caddy"

"$CADDY_BIN" validate --config /etc/caddy/Caddyfile --adapter caddyfile \
  || die "初始 Caddy 配置校验失败，请检查 $CADDY_SITES_DIR/$DOMAIN.caddy"

systemctl restart caddy
systemctl restart naivepanel
sleep 2
systemctl is-active --quiet caddy || die "caddy 启动失败: journalctl -u caddy"
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
