# NaivePanel

Linux 服务器面板（Go 单二进制）：管理定制 Caddy（NaiveProxy forwardproxy）、多站点、
BypassCore 分流核心与 Geo 数据文件。

详细设计见 [DESIGN.md](DESIGN.md)。

## 功能

- **站点管理**：静态站 / PHP 站 / 反向代理站 / 纯代理站，自定义 handle / handle_path 块，
  高级模式（原始 Caddyfile 片段），保存即 `caddy validate → 备份 → reload → 探活 → 失败回滚`
- **forward_proxy**：basic_auth 多账号、upstream 配置（可联动本机 BypassCore），
  hide_ip / hide_via / probe_resistance 固定注入
- **配置预览**：单站片段实时预览 + 全局 Caddy 配置合并预览
- **BypassCore**：一键安装/更新（GitHub release，amd64/arm64）、配置编辑
  （`-check-config` → 控制面事务热重载）、服务控制、运行状态查看
- **Geo 数据**：geoip.dat / geosite.dat 下载（sha256 校验 + 原子替换）、手动/每周自动更新、镜像源
- **面板安全**：经 Caddy 反代 HTTPS + 随机面板路径 + Argon2id 密码 + 登录锁定 + 可选 TOTP MFA（含恢复码）

## 安装

在 Debian 11+ / Ubuntu 20.04+（amd64 / arm64）的**全新或已有 Caddy** 服务器上：

```bash
sudo bash install.sh
```

脚本会：安装 Go → 交互询问域名/面板路径/管理员账号密码 → 检测并安装官方 Caddy →
xcaddy 编译含 `klzgrad/forwardproxy@naive` 的定制 Caddy → 部署面板并生成初始配置。

安装完成后访问 `https://<域名>/<面板路径>/` 登录（路径仅显示一次，请保存）。

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
