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
- 提供本地 SOCKS5 代理
- 导出诊断信息
- 通过命令行完成 Profile 管理与路由配置

## 快速开始

### 启动守护进程和托盘

```powershell
go run .\cmd\flexconnectd
go run .\cmd\flextray
```

### 常用 CLI

```powershell
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

```powershell
flexconnect login --server https://vpn.example.com --user alice --password <password> --name corp
flexconnect up -p corp
flexconnect down
flexconnect diag diag.json
flexconnect proxy status
flexconnect proxy enable 127.0.0.1:1080
flexconnect proxy disable
flexconnect logs
```

## 构建与安装

### Windows 服务

```powershell
.\scripts\install-windows-service.ps1
.\scripts\uninstall-windows-service.ps1
```

### Linux / macOS 服务模板

```bash
./scripts/install-linux.sh
./scripts/install-macos.sh
```

### 统一打包

```powershell
go run .\cmd\dist list
go run .\cmd\dist build --version 1.0.0 linux/amd64/tgz
go run .\cmd\dist build --version 1.0.0 linux/amd64/deb
go run .\cmd\dist build --version 1.0.0 linux/amd64/rpm
go run .\cmd\dist build --version 1.0.0 windows/amd64/zip
go run .\cmd\dist build --version 1.0.0 windows/amd64/msi
go run .\cmd\dist build --version 1.0.0 darwin/amd64/pkg
go run .\cmd\dist build --version 1.0.0 darwin/arm64/pkg
```

推送形如 `v1.0.0` 的 Git tag 后，GitHub Actions 会自动构建这些产物并创建对应的 GitHub Release。

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
