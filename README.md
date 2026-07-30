# NaivePanel

Linux 服务器面板（Go 单二进制）：管理定制 Caddy（NaiveProxy forwardproxy）、多站点、
BypassCore 分流核心与 Geo 数据文件。

详细设计见 [DESIGN.md](DESIGN.md)。

## 功能

- **站点管理**：站点列表实时解析磁盘上的 Caddyfile 片段（手工添加/修改的配置直接可见、可编辑、
  可删除），静态站 / PHP 站 / 反向代理站 / 纯代理站，自定义 handle / handle_path 块，
  高级模式（原始 Caddyfile 片段），保存即 `caddy validate → 备份 → reload → 探活 → 失败回滚`
- **Caddy 运维**：服务控制（启动 / 停止 / 重启 / 重载）+ 配置查看（磁盘实况原文）
- **forward_proxy**：basic_auth 多账号、upstream 配置（可联动本机 BypassCore），
  hide_ip / hide_via / probe_resistance 固定注入
- **面板自更新**：设置页检查/安装新版本（GitHub release，SHA256SUMS 校验 + 安装前自检 +
  替换后自动重启），可开启每天自动更新
- **BypassCore**：一键安装/更新（GitHub release，amd64/arm64，SHA256SUMS 校验 + 安装后自检）、
  通用结构化 JSON 编辑（保留高级 JSON 模式与未知字段）、`-check-config` → 控制面事务热重载、
  服务控制、运行状态查看；存量配置未开启 control socket 时会给出明确诊断和一键启用入口
- **Geo 数据**：geoip.dat / geosite.dat 下载（sha256 校验 + 原子替换）、手动/每周自动更新、镜像源
- **服务日志**：面板内查看 Caddy / BypassCore 的 systemd journal（最近 100–1000 行）
- **面板安全**：经 Caddy 反代 HTTPS + 随机面板路径 + Argon2id 密码 + 登录锁定 + 可选 TOTP MFA（含恢复码）

从磁盘导入普通 Caddyfile 时，面板同时识别自身的 `route` 格式和常见的无 `route` 格式。
域名、建站类型、`forward_proxy`、`handle` 块仍以表单编辑；`tls` / `log` / `bind` 等站点选项
以及无法映射到简化字段的 HTTP 指令会保留在结构化表单的对应区域。只有无法安全拆分的片段才回退到整段原文模式。

## 安装

### 方式一：一体化脚本（推荐，全新服务器）

在 Debian 11+ / Ubuntu 20.04+（amd64 / arm64）的**全新或已有 Caddy** 服务器上：

```bash
sudo bash install.sh
```

脚本会：安装 Go → 交互询问域名/面板路径/管理员账号密码 → 检测并安装官方 Caddy →
xcaddy 编译含 `klzgrad/forwardproxy@naive` 的定制 Caddy → 部署面板并生成初始配置。

安装完成后访问 `https://<域名>/<面板路径>/` 登录（路径仅显示一次，请保存）。

### 方式二：预编译二进制（已有 Caddy / Caddy + forwardproxy 的服务器）

适用于 Caddy 已经在跑、不想动现有环境的机器。先确认你的 Caddy 属于哪种：

- **已含 `klzgrad/forwardproxy@naive` 插件**（自行 xcaddy 编译过，或按方式一装过）：全部功能可用
- **官方原版 Caddy**：静态站 / PHP 站 / 反向代理站均可用；站点一旦启用 forward_proxy，
  `caddy validate` 会失败并自动回滚。需要代理功能时重编译一个：

  ```bash
  xcaddy build v2.10.2 --with github.com/caddyserver/forwardproxy=github.com/klzgrad/forwardproxy@naive
  ```

以下以 Debian/Ubuntu + systemd + amd64 为例（arm64 把 `linux-amd64` 换成 `linux-arm64`），
把 `example.com`、管理员密码替换为你的实际值：

```bash
# 1. 下载 release 并校验完整性（版本号换成最新 tag）
VER=v0.3.5
cd /tmp
curl -fLO "https://github.com/kinmeic/NaivePanel/releases/download/${VER}/naivepanel-linux-amd64.tar.gz"
curl -fLO "https://github.com/kinmeic/NaivePanel/releases/download/${VER}/SHA256SUMS"
grep "linux-amd64.tar.gz" SHA256SUMS | sha256sum -c -
tar -xzf naivepanel-linux-amd64.tar.gz
sudo install -m 0755 naivepanel /usr/local/bin/naivepanel

# 2. 生成密码哈希、随机面板路径、反代共享密钥（密码经 stdin 输入，不留 ps/历史记录）
PASS_HASH="$(/usr/local/bin/naivepanel hash-password)"   # 回车后输入密码
BASE_PATH="$(/usr/local/bin/naivepanel gen-path)"
PROXY_TOKEN="$(openssl rand -hex 24)"
CADDY_BIN="$(command -v caddy)"
test -n "${CADDY_BIN}" && test "${CADDY_BIN#/}" != "${CADDY_BIN}" || {
  echo "未找到 Caddy 的绝对路径" >&2
  exit 1
}

# 3. 写面板配置
sudo mkdir -p /etc/naivepanel /etc/caddy/sites
sudo tee /etc/naivepanel/config.yaml >/dev/null <<EOF
listen: 127.0.0.1:9000
domain: example.com
base_path: ${BASE_PATH}
admin_user: admin
admin_pass_hash: '${PASS_HASH}'
totp_enabled: false
host_site: example.com
session_ttl_hours: 12
proxy_token: '${PROXY_TOKEN}'
caddy:
  bin: ${CADDY_BIN}
  main_file: /etc/caddy/Caddyfile
  sites_dir: /etc/caddy/sites
bypasscore:
  socks_port: 1080
  bin_path: /usr/local/bin/bypasscore
  config_path: /etc/bypasscore/config.json
  control_sock: /run/bypasscore/control.sock
  work_dir: /etc/bypasscore
geo:
  dir: /etc/bypasscore
  auto_update_weekly: false
backup_dir: /etc/naivepanel/backups
sites:
  - domain: example.com
    forward_proxy:
      enabled: false
      accounts: []
    web:
      type: static
      root: /var/www/example.com
EOF
sudo chmod 600 /etc/naivepanel/config.yaml
```

> - `caddy.bin` 取服务器上实际的 caddy 路径；`main_file` / `sites_dir` 按你的布局调整。
> - **注意**：面板保存站点时会把 `main_file` 重写为「email + import sites_dir」的极简形式。
>   如果你在主 Caddyfile 里有自定义全局配置，请先迁移到 `sites_dir` 的片段文件中。

```bash
# 4. 主 Caddyfile 导入片段目录（已有 import 行则跳过，不要覆盖现有配置）
grep -q 'import /etc/caddy/sites' /etc/caddy/Caddyfile 2>/dev/null || \
  printf '{\n\temail admin@example.com\n}\n\nimport /etc/caddy/sites/*.caddy\n' | sudo tee /etc/caddy/Caddyfile

# 5. 面板寄宿站点的引导片段（面板上线后由它接管渲染，此处仅为首次拉起面板）
sudo tee "/etc/caddy/sites/example.com.caddy" >/dev/null <<EOF
:443, example.com {
	route {
		handle ${BASE_PATH}/* {
			reverse_proxy 127.0.0.1:9000 {
				header_up X-NaivePanel-Key ${PROXY_TOKEN}
			}
		}
		redir ${BASE_PATH} ${BASE_PATH}/ 308

		root * /var/www/example.com
		encode gzip zstd
		file_server
	}
}
EOF
sudo chmod 600 /etc/caddy/sites/example.com.caddy
sudo mkdir -p /var/www/example.com

# 6. 校验并重载 Caddy，注册并启动面板服务
sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
sudo systemctl reload caddy || sudo systemctl restart caddy
sudo /usr/local/bin/naivepanel install -config /etc/naivepanel/config.yaml -bin /usr/local/bin/naivepanel
```

完成后访问 `https://example.com${BASE_PATH}/`，建议立即到「设置」启用 MFA。

## 服务管理子命令

二进制内置跨平台服务安装/卸载（自动识别 systemd / procd (OpenWrt) / launchd (macOS)）：

```bash
# 安装为系统服务（需 root；默认注册开机启动并立即启动）
naivepanel install [-config /etc/naivepanel/config.yaml] [-bin /usr/local/bin/naivepanel] [-start=false] [-dry-run]

# 卸载服务（保留二进制与配置；-purge 一并删除 /usr/local/bin/naivepanel 与 /etc/naivepanel）
naivepanel uninstall [-purge] [-dry-run]
```

- Linux（systemd）：写 `/etc/systemd/system/naivepanel.service` + enable
- OpenWrt（procd）：写 `/etc/init.d/naivepanel`（USE_PROCD，respawn）+ enable，二进制默认装到 `/usr/bin/naivepanel`
- macOS（launchd）：写 `/Library/LaunchDaemons/com.naivepanel.plist` + `launchctl bootstrap`
- 其他子命令：`hash-password`（生成 Argon2id 哈希，install.sh 用）、`gen-path`（生成随机面板路径）、`version`

## 开发

```bash
make build       # 构建本机二进制到 bin/
make build-all   # 交叉编译 linux/amd64 + linux/arm64 到 dist/
make test vet
```

## 目录结构

```
cmd/naivepanel/      二进制入口（hash-password / gen-path / install / uninstall / version 子命令）
internal/config/     面板配置模型与持久化
internal/auth/       Argon2id / session / CSRF / 登录锁定 / TOTP
internal/sites/      站点模型与 Caddyfile 渲染
internal/caddymgr/   Caddy 配置管线（validate/备份/reload/回滚/预览）
internal/bypasscore/ BypassCore 安装、配置管线、控制面客户端
internal/geo/        Geo 数据下载与校验
internal/service/    跨平台服务安装（systemd / procd / launchd）
internal/sysd/       systemctl 封装
internal/web/        HTTP 服务与内嵌 UI
install.sh           一体化安装脚本
```
