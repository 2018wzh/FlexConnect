# FlexConnect

FlexConnect 是一个跨平台可配置的 AnyConnect VPN 客户端，提供守护进程、桌面托盘和命令行接口，支持 Windows、Linux 和 macOS。

## 组件

- `flexconnectd`：本地守护进程，负责连接管理、状态维护和 API 服务
- `flextray`：桌面托盘入口，展示状态并提供常用操作
- `flexconnect`：命令行客户端，适合脚本和日常运维
- `client/local`：类型化本地 API 客户端
- `internal/vpn/anyconnect`：内建 AnyConnect 后端

## 能力

- 管理多个 Profile
- 发起和断开 VPN 连接
- 应用服务器路由与本地路由策略
- 提供本地 SOCKS5 代理；代理连接与域名解析只走已连接的 VPN 隧道
- 导出诊断信息
- 通过命令行完成 Profile 管理与路由配置

## 快速开始

### 启动守护进程和托盘

```bash
go run ./cmd/flexconnectd
go run ./cmd/flextray
```

### 常用 CLI

```bash
flexconnect status
flexconnect up
flexconnect down
flexconnect profile list
```

### 首次使用

1. 启动 `flexconnectd`
2. 运行 `flexconnect login --server <server> --user <user> --password <password> --name <profile-name>`
3. 创建或选择一个 Profile
4. 输入服务器、用户名和密码并连接

连接成功后，CLI 与托盘会显示当前状态、VPN 地址、DNS 和路由摘要。

## 命令示例

```bash
flexconnect login --server https://vpn.example.com --user alice --password <password> --name corp
flexconnect up -p corp
flexconnect down
flexconnect diag diag.json
flexconnect proxy status
flexconnect proxy enable 127.0.0.1:1080
flexconnect proxy disable
flexconnect logs
```

SOCKS5 代理是 VPN-only：启用后只支持 TCP CONNECT 和 IPv4 目标，域名通过 VPN DNS 解析，无法确认走 VPN 时会拒绝连接，不会回退到本机网络。当前不支持 UDP ASSOC、BIND 或 IPv6 代理目标。

## Docker 部署

Docker 镜像运行 `flexconnectd`，通过环境变量创建一个固定 ID 的 Profile 并立即连接。容器内需要 Linux TUN 能力，SOCKS5 默认监听 `0.0.0.0:1080` 并通过端口映射暴露给宿主机。

```bash
docker build -t flexconnect:local .
docker run --rm \
  --cap-add NET_ADMIN \
  --device /dev/net/tun \
  -p 1080:1080 \
  -e FLEXCONNECT_SERVER=https://vpn.example.com \
  -e FLEXCONNECT_USERNAME=alice \
  -e FLEXCONNECT_PASSWORD='<password>' \
  flexconnect:local
```

推荐用 Compose 和 Docker secret 注入密码：

```bash
mkdir -p secrets
printf '%s\n' '<password>' > secrets/flexconnect_password
FLEXCONNECT_SERVER=https://vpn.example.com FLEXCONNECT_USERNAME=alice docker compose -f docker-compose.example.yml up --build
```

容器启动失败会直接非零退出，包括缺少必填环境变量、密码文件不可读、VPN 连接失败、请求启用 SOCKS5 但代理未实际监听。让 Docker/Compose 的重启策略负责重试，不在应用启动流程里吞掉错误。

### 环境变量

| 变量 | 说明 |
| --- | --- |
| `FLEXCONNECT_SOCKET` | daemon 本地 Unix socket，镜像默认 `/run/flexconnect/flexconnect.sock` |
| `FLEXCONNECT_STATE` | 状态文件路径，镜像默认 `/var/lib/flexconnect/state.json` |
| `FLEXCONNECT_VERBOSE` | `true` 时启用 debug 日志 |
| `FLEXCONNECT_SECRET_STORE` | `keyring` 或 `memory`；镜像默认 `memory` |
| `FLEXCONNECT_CONNECT_ON_START` | `true` 时启动即 upsert Profile 并连接；镜像默认 `true` |
| `FLEXCONNECT_CONNECT_TIMEOUT` | 启动连接超时，例如 `45s`、`2m` |
| `FLEXCONNECT_PROFILE_ID` | 启动 Profile 的固定 ID，镜像默认 `docker` |
| `FLEXCONNECT_PROFILE_NAME` | 启动 Profile 名称，镜像默认 `docker` |
| `FLEXCONNECT_SERVER` | AnyConnect 服务器 URL，启动连接时必填 |
| `FLEXCONNECT_USERNAME` | 用户名，启动连接时必填 |
| `FLEXCONNECT_GROUP` | VPN group，可选 |
| `FLEXCONNECT_PASSWORD` | 密码；不可与 `FLEXCONNECT_PASSWORD_FILE` 同时设置 |
| `FLEXCONNECT_PASSWORD_FILE` | 密码文件路径；只去掉末尾换行，适合 Docker secret |
| `FLEXCONNECT_ACCEPT_SERVER_ROUTES` | 是否接受服务器下发路由 |
| `FLEXCONNECT_AUTO_RECONNECT` | 是否在异常断开后自动重连；镜像默认 `true` |
| `FLEXCONNECT_APPLY_DNS` | 是否应用 VPN DNS |
| `FLEXCONNECT_MTU` | TUN MTU |
| `FLEXCONNECT_DNS` | 逗号分隔的 DNS override |
| `FLEXCONNECT_INCLUDE_ROUTES` | 逗号分隔的自定义 include routes |
| `FLEXCONNECT_EXCLUDE_ROUTES` | 逗号分隔的自定义 exclude routes |
| `FLEXCONNECT_SOCKS5_ENABLED` | 是否启用 VPN-only SOCKS5；镜像默认 `true` |
| `FLEXCONNECT_SOCKS5_LISTEN` | SOCKS5 监听地址；镜像默认 `0.0.0.0:1080` |

## 构建与安装

### Windows 服务

```powershell
./scripts/install-windows-service.ps1
./scripts/uninstall-windows-service.ps1
```

### Linux / macOS 服务模板

```bash
./scripts/install-linux.sh
./scripts/install-macos.sh
```

### 统一打包

```bash
go run ./cmd/dist list
go run ./cmd/dist build --version 1.0.6 linux/amd64/tgz
go run ./cmd/dist build --version 1.0.6 linux/amd64/deb
go run ./cmd/dist build --version 1.0.6 linux/amd64/rpm
go run ./cmd/dist build --version 1.0.6 windows/amd64/zip
go run ./cmd/dist build --version 1.0.6 windows/amd64/msi
go run ./cmd/dist build --version 1.0.6 darwin/amd64/pkg
go run ./cmd/dist build --version 1.0.6 darwin/arm64/pkg
```

推送形如 `v1.0.6` 的 Git tag 后，GitHub Actions 会自动构建这些产物并创建对应的 GitHub Release。

## 运行与配置

- `--socket` 用于指定本地 IPC 端点
- `--state` 用于指定状态文件
- `-v` 或 `--verbose` 启用更详细日志
- Windows 上直接启动 `flexconnectd` 时会自动请求管理员权限
- 密码通过系统密钥库保存，状态文件只保存非敏感元数据
- 本地控制接口通过 Unix socket 或 Windows named pipe 提供，不暴露公网 TCP 端口

## 项目结构

- `assets/`：图标和 Windows 运行时资源
- `client/`：面向用户的客户端代码
- `cmd/`：可执行程序入口
- `docs/`：项目说明文档
- `internal/`：守护进程、API、路由、IPC、存储、日志和 AnyConnect 实现
- `release/`：Debian 和 RPM 生命周期脚本
- `scripts/`：构建、打包、安装和运行脚本

## Credits
* [Tailscale](https://tailscale.com/) - 架构参考与实现参考
* [sslcon](https://github.com/tlslink/sslcon) - AnyConnect 协议实现参考
