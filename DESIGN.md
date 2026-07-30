# NaivePanel 技术设计文档

## 1. 项目概述

NaivePanel 是一个 Linux 服务器面板应用（Go 单二进制），用于管理：

- **定制 Caddy**：xcaddy 编译、内置 `klzgrad/forwardproxy@naive` 分支（NaiveProxy 服务端）
- **站点**：多站点 Caddyfile 片段的结构化管理（静态站 / PHP 站 / 反向代理站 / 纯代理站 + 自定义 handle 块）
- **forward_proxy**：basic_auth 多账号、upstream 配置（hide_ip / hide_via / probe_resistance 固定注入，不在 UI 展示）
- **BypassCore**（github.com/kinmeic/BypassCore）：安装、配置管理、控制面事务热重载
- **GeoData**：geoip.dat / geosite.dat 下载更新（Loyalsoldier/v2ray-rules-dat）

支持系统：Debian 11+ / Ubuntu 20.04+（apt 系），x86_64 + arm64，全程 root 运行。

## 2. 总体架构

```
公网 443 (TLS, Caddy 自动签发证书)
  │
  ├─ https://domain.com/manage-<random>/  ──→ reverse_proxy 127.0.0.1:<panel_port>
  │                                                  │
  │                                            naivepanel (Go 单二进制, root, systemd)
  │                                                  │
  │        ┌─────────────────────────────────────────┼──────────────────────────────┐
  │        │                                         │                              │
  │   /etc/caddy/sites/*.caddy              /etc/bypasscore/config.json      /etc/bypasscore/
  │   caddy validate → reload → 回滚        -check-config → 控制面热重载      geoip.dat / geosite.dat
  │        │                                         │                     (sha256 校验, 原子替换)
  │   systemctl reload caddy              unix socket /run/bypasscore/control.sock
  │
  └─ 各站点 forward_proxy (basic_auth 多账号)
        │ upstream
        ├─ 无 BypassCore: https://user:pass@upstream:443 或 socks5://...
        └─ 有 BypassCore: socks5://127.0.0.1:<socks_port> ──→ bypasscore 规则分流
                                                              (direct / proxy / wireguard / blackhole)
```

## 3. 目录与文件布局

| 路径 | 内容 | 权限 |
|---|---|---|
| `/usr/local/bin/naivepanel` | 面板二进制 | 0755 |
| `/etc/naivepanel/config.yaml` | 面板配置（含站点模型、账号哈希、TOTP 密钥） | 0600 |
| `/etc/naivepanel/backups/` | Caddy/BypassCore 配置变更前的自动备份（保留最近 20 份） | 0700 |
| `/usr/bin/caddy` | xcaddy 编译的定制 Caddy，**直接覆盖官方二进制**；原版备份于 `/usr/bin/caddy.official`（仅首次） | 0755 |
| `/etc/caddy/Caddyfile` | 主配置，`import /etc/caddy/sites/*.caddy` | 0644 |
| `/etc/caddy/sites/<domain>.caddy` | 面板渲染的站点片段（每站一文件） | 0600 |
| `/usr/local/bin/bypasscore` | BypassCore 二进制 | 0755 |
| `/etc/bypasscore/config.json` | BypassCore 配置 | 0600 |
| `/etc/bypasscore/geoip.dat` `geosite.dat` | GeoData（BypassCore 工作目录） | 0644 |
| `/run/bypasscore/control.sock` | BypassCore 控制面 Unix Socket | 0660 |
| `/var/www/<domain>/` | 站点 web 根目录 | 0755 |

systemd units：

- `naivepanel.service` — `ExecStart=/usr/local/bin/naivepanel -config /etc/naivepanel/config.yaml`，`Restart=always`
- `caddy.service` — 沿用官方包 unit（ExecStart 指向 `/usr/bin/caddy`，已是定制二进制）；`apt-mark hold caddy` 防止官方包升级把定制二进制覆盖回去
- `bypasscore.service` — `ExecStart=/usr/local/bin/bypasscore -run -config /etc/bypasscore/config.json -log-level warning`，`WorkingDirectory=/etc/bypasscore`（geo 文件按工作目录加载），`Restart=on-failure`

## 4. 面板自身配置模型

`/etc/naivepanel/config.yaml`：

```yaml
listen: 127.0.0.1:9000          # 仅 loopback，由 Caddy 反代暴露
domain: example.com             # 主域名（首个站点 = 面板寄宿站点）
base_path: /manage-x7k2q9       # 面板路径，初始化随机生成
admin_user: admin
admin_pass_hash: argon2id$v=19$m=65536,t=3,p=4$...   # Argon2id
totp_enabled: false
totp_secret: ""                 # Base32，启用 MFA 时生成
recovery_hashes: []             # 恢复码 SHA-256 哈希列表
host_site: example.com          # 面板寄宿站点（删除保护）
session_ttl_hours: 12
proxy_token: ""                 # HTTPS 门禁共享密钥，安装时生成；Caddy 反代注入 header_up
sites: []                       # 站点模型列表（见第 6 节）
bypasscore:
  socks_port: 1080              # 与站点 upstream 联动的本机 SOCKS5 端口
  bin_path: /usr/local/bin/bypasscore
  config_path: /etc/bypasscore/config.json
  control_sock: /run/bypasscore/control.sock
geo:
  dir: /etc/bypasscore
  mirror: ""                    # 可选 GitHub 镜像前缀，如 https://mirror.ghproxy.com/
  auto_update_weekly: false
```

## 5. 认证与安全

- **密码**：Argon2id（`golang.org/x/crypto/argon2`，m=64MiB, t=3, p=4），PHC 字符串格式存储
- **会话**：登录成功签发 256-bit 随机 token，内存 session 表（重启即失效），Cookie：`HttpOnly; Secure; SameSite=Strict; Path=<base_path>`，空闲超时 + 绝对过期（默认 12h）
- **登录保护**：按 账号+IP 计数，连续失败 5 次锁定 15 分钟；密码比较恒定时间
- **MFA（TOTP, RFC 6238，`github.com/pquerna/otp`）**：
  - 设置页生成 Base32 密钥 + otpauth:// QR 码（服务端 PNG 渲染），输入一次 6 位验证码确认后生效
  - 生效时发放 10 个一次性恢复码（仅存 SHA-256 哈希），仅展示一次
  - 登录流程：密码正确 → 已启用 TOTP → 二次验证页（支持 TOTP 或恢复码）
  - 关闭/重置 TOTP 需重新输入密码 + 当前 TOTP
- **CSRF**：每 session 一个 token，所有变更表单携带隐藏字段 `_csrf`，服务端校验
- **安全响应头**：`X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`（防面板路径经 Referer 泄漏）、严格 CSP
- **请求来源**：面板只监听 127.0.0.1；不信任任何 X-Forwarded-* 用于权限判断
- **仅 HTTPS 门禁**：面板拒绝一切未经 Caddy HTTPS 反代的请求。安装时生成共享密钥 `proxy_token`（config.yaml，0600），Caddy 面板反代块注入 `header_up X-NaivePanel-Key <token>`；面板中间件校验该头，缺失/错误一律返回 404（与路径不存在不可区分）。即使面板端口被误暴露或本机直连也无法访问。高级模式（raw）的寄宿站点必须自行在面板 reverse_proxy 块中携带该 header_up，否则校验报错并给出指令示例
- **面板寄宿保护**：`host_site` 站点不可删除，除非先迁移面板寄宿到其他站点（设置页操作）

## 6. 站点模型与 Caddyfile 渲染

### 6.1 站点模型

```yaml
- domain: example.com
  forward_proxy:
    enabled: true
    accounts: [{user: abcdefg, pass: "1234567890"}]   # 多账号
    use_bypasscore: true                               # true → upstream 固定 socks5://127.0.0.1:<socks_port>
    upstream: ""                                       # use_bypasscore=false 时的自定义 upstream
  web:
    type: static            # none | static | php | reverse_proxy
    root: /var/www/example.com
    php_socket: unix//run/php/php8.3-fpm.sock
    proxy_to: 127.0.0.1:3000
  extra_blocks:             # 自定义 handle / handle_path 块，按序渲染
    - type: handle_path     # handle | handle_path
      matcher: /api/*
      content: |
        reverse_proxy 127.0.0.1:8080
  raw_mode: false           # 高级模式：直接维护该站点的原始 Caddyfile 片段
  raw: ""
```

### 6.2 渲染规则（结构化模式）

站点片段整体包裹在 `route {}` 中保证执行顺序：

1. **面板块**（仅 `host_site`）：`handle <base_path>/* { reverse_proxy <listen> }` + `redir <base_path> <base_path>/ 308`
2. **extra_blocks**（按用户定义顺序，handle / handle_path）
3. **forward_proxy**（enabled 时）：逐行 `basic_auth <user> <pass>`，固定注入 `hide_ip` / `hide_via` / `probe_resistance`；upstream 一行（use_bypasscore → `socks5://127.0.0.1:<socks_port>`）
4. **web 部分**：
   - `static`：`root * <root>` + `encode gzip zstd` + `file_server`
   - `php`：static + `php_fastcgi <socket>`（file_server 在其后）
   - `reverse_proxy`：`reverse_proxy <proxy_to>`
   - `none`：无

渲染示例：

```caddyfile
:443, example.com {
    route {
        handle /manage-x7k2q9/* {
            reverse_proxy 127.0.0.1:9000
        }
        redir /manage-x7k2q9 /manage-x7k2q9/ 308

        handle_path /api/* {
            reverse_proxy 127.0.0.1:8080
        }

        forward_proxy {
            basic_auth abcdefg 1234567890
            hide_ip
            hide_via
            probe_resistance
            upstream socks5://127.0.0.1:1080
        }

        root * /var/www/example.com
        encode gzip zstd
        php_fastcgi unix//run/php/php8.3-fpm.sock
        file_server
    }
}
```

主 Caddyfile：

```caddyfile
{
    email admin@example.com
}

import /etc/caddy/sites/*.caddy
```

### 6.3 配置预览

- **单站预览**：站点编辑页实时展示该站点将生成的 `.caddyfile` 片段（保存前确认）
- **全局预览**：独立页面展示主 Caddyfile + 全部站点片段的合并视图，只读、带行号
- 预览为纯渲染，不落盘、不 reload

## 7. 配置变更管线（Caddy 与 BypassCore 共用模式）

```
渲染/编辑新配置
  → 写入临时文件
  → 校验（caddy validate --config / bypasscore -check-config / POST /v1/config/validate）
  → 备份当前配置（caddy → /etc/caddy/backup/<时间戳>/，bypasscore → /etc/naivepanel/backups/bypasscore/<时间戳>/）
  → 落盘生效
  → 重载（caddy reload --config / POST /v1/config/reload）
  → 探活（caddy: 本地 HTTPS 请求站点根路径 / bypasscore: GET /v1/ready）
  → 失败：自动恢复备份 + 再次 reload + 页面告警（保留错误输出）
```

备份目录保留最近 20 份，超出自动清理。

## 8. BypassCore 集成

### 8.1 安装/更新

- GitHub API 查询 `kinmeic/BypassCore` 最新 release，按架构下载 `bypasscore-linux-{amd64,arm64}.tar.gz`
- 解压至 `/usr/local/bin/bypasscore`，写入 systemd unit，enable --now
- 无 release 可用时回退：本机 Go 源码编译（`go install` 或 git clone + make build）

### 8.2 配置管理

- **结构化辅助**（首版范围）：
  - 站点开启 `use_bypasscore` 时，面板确保 BypassCore config 中存在 `{"tag":"caddy-forward","type":"socks","listen":"127.0.0.1","port":<socks_port>,"network":"tcp"}` 入站，缺失则自动追加（validate → reload）
  - 状态面板：版本、运行状态、readiness、入站/出站概览（`GET /v1/status`）
- **全量编辑**：config.json 文本编辑器（页内），保存走完整管线：`-check-config` → 备份 → 落盘 → `POST /v1/config/reload`（返回 `restart_required` 时改为 systemctl restart）→ `GET /v1/ready` 探活
- **控制面自修复**：新建最小配置时自动写入 `control.enabled=true` 与面板配置的 socket；
  对存量配置区分“服务未运行 / 控制面未启用 / socket 路径不一致 / socket 暂不可达”，并提供一键启用
- 服务控制：start / stop / restart（systemctl）

### 8.3 控制面客户端

HTTP over Unix Socket（`/run/bypasscore/control.sock`），面板以 root 直接访问。使用端点：`/v1/status`、`/v1/ready`、`/v1/config/validate`、`/v1/config/reload`。

### 8.4 GeoData

- 来源：`https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/{geoip.dat,geosite.dat,*.sha256sum}`，支持镜像前缀
- 流程：下载到临时文件 → sha256 校验 → 原子替换（rename）→ BypassCore reload → 展示文件版本日期
- 手动更新按钮 + 可选每周定时（面板内 goroutine ticker）

## 9. 安装脚本（install.sh）

流程（bash，root，set -euo pipefail）：

1. 环境检测：apt 系发行版、架构（amd64/arm64）、内存（< 1.5G 自动创建 2G 临时 swap，编译完成后询问保留与否）
2. 交互询问：服务器域名、面板路径（默认随机）、管理员账号、管理员密码（二次确认）、GOPROXY 镜像选择（默认 goproxy.cn）
3. 安装 Go（按架构官方 tarball → /usr/local/go，幂等：已存在且版本 >= 1.22 则跳过）
4. Caddy 检测与安装：
   - 已安装：提示用户决策（使用现有 / 重新安装 / 中止）
   - 未安装：官方 apt 仓库安装，启动一次生成默认配置后停止
5. `go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest`，`~/go/bin/xcaddy build <pin版本> --with github.com/caddyserver/forwardproxy=github.com/klzgrad/forwardproxy@naive`（Caddy 版本钉住，记录在脚本变量中）
6. 备份官方二进制为 `/usr/bin/caddy.official`（仅首次），产物直接覆盖 `/usr/bin/caddy`；`apt-mark hold caddy` 防止 apt 升级回覆盖
7. 部署面板二进制（优先 repo 内 `make build` 产物；支持 `-r` 从 release 下载）+ naivepanel.service
8. 调用 `naivepanel hash-password` 生成 Argon2id 哈希，写 `/etc/naivepanel/config.yaml`（0600）
9. 渲染主 Caddyfile + 首站点片段（含面板块 + forward_proxy 表单留待面板内配置）→ `caddy validate` → 启动 caddy / naivepanel
10. 输出访问信息：`https://<domain><base_path>/` + 账号，提示路径只显示一次

## 10. 面板 HTTP 路由表

| 方法 | 路径（相对 base_path） | 说明 | 认证 |
|---|---|---|---|
| GET/POST | /login | 登录 | 否 |
| GET/POST | /login/totp | TOTP 二次验证 | 半会话 |
| POST | /logout | 登出 | 是 |
| GET | / | 仪表盘（服务状态总览） | 是 |
| GET | /sites | 站点列表 | 是 |
| GET/POST | /sites/new | 新建站点 | 是 |
| GET/POST | /sites/{domain}/edit | 编辑站点（含实时预览） | 是 |
| GET | /sites/{domain}/preview | 单站片段预览 | 是 |
| POST | /sites/{domain}/delete | 删除站点（寄宿保护） | 是 |
| GET | /caddy/preview | 全局配置预览 | 是 |
| POST | /caddy/reload | 手动重载 Caddy | 是 |
| GET | /bypasscore | 状态 + 版本 + 服务控制 | 是 |
| POST | /bypasscore/install | 安装/更新 BypassCore | 是 |
| GET/POST | /bypasscore/config | 通用结构化 JSON / 高级 JSON 配置编辑（validate+reload 管线） | 是 |
| POST | /bypasscore/service/{start,stop,restart} | 服务控制 | 是 |
| GET/POST | /geo | Geo 文件状态 + 手动更新 | 是 |
| GET/POST | /settings | 改密码、TOTP 设置/开关、寄宿站点迁移、面板信息 | 是 |

所有 POST 均校验 CSRF token。

## 11. 技术选型

| 依赖 | 用途 |
|---|---|
| 标准库 net/http | Web 服务（无框架） |
| html/template + embed | 内嵌 UI（服务端渲染，少量原生 JS） |
| golang.org/x/crypto | Argon2id |
| github.com/pquerna/otp | TOTP + QR PNG |
| gopkg.in/yaml.v3 | 面板配置 |

CGO_ENABLED=0，交叉编译 linux/amd64 + linux/arm64。

## 12. 里程碑

- M1 安装脚本（Go + Caddy + xcaddy + 面板部署）— Debian 12 / Ubuntu 22.04 实机验证
- M2 面板骨架：认证 + TOTP + 站点 CRUD + 配置管线
- M3 forward_proxy 表单 + 预览 + BypassCore 联动
- M4 BypassCore 安装/配置/服务控制
- M5 GeoData 管理 + 定时更新
