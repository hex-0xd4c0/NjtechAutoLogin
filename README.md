# NjtechAutoLogin - 泥工校园网自动登录工具

> 此项目大量代码使用AI生成。

[![Go Version](https://img.shields.io/github/go-mod/go-version/hex-0xd4c0/NjtechAutoLogin)](https://golang.org/)

`NjtechAutoLogin` 是一个校园网自动登录与保活工具，支持 Linux（OpenWRT 路由器）和 macOS 双平台。针对南京工业大学的 Dr.COM 认证系统设计。它可以作为系统服务后台运行（Linux 自守护进程），也可以在前台绿色模式运行，自动检测网络状态并在掉线时重连。

## ✨ 功能特性

- **自动登录**：模拟浏览器完整 HTTP 请求流程，支持电信（`telecom`）和移动（`cmcc`）运营商。
- **多 ISP 在线检测**：依次尝试 Microsoft NCSI、Apple Captive Portal、百度、小米 204、腾讯、网易等多个检测端点，任一成功即视为在线，避免单点故障。
- **保活状态机**：
  - 在线状态：每 5 分钟检测一次，掉线自动重连 2 次
  - 离线状态：每 15 分钟重试一次，每次重连 2 次
  - 初始连接：首次尝试 3 次
- **自守护进程**（Linux）：程序内置 `daemonize` 能力（fork + setsid + stdio→/dev/null），无需依赖外部 `start-stop-daemon` 工具，脱离控制终端独立运行。
- **日志管理**：日志按日期轮转，保留最近 25 天。服务模式保存到 `/var/log/njtechlogin`，绿色模式直接输出到终端屏幕（可通过 `--log` 写入文件）。
- **灵活部署**：Linux 下支持作为 OpenWRT 服务安装（开机自启），macOS 使用绿色模式前台运行。
- **配置管理**：YAML 配置文件，支持通过命令行参数或交互式输入设置账号密码。
- **状态查看**：通过 `--show` 命令查看运行状态和当前配置（密码隐藏）。

## 🔧 系统要求

- **操作系统**：OpenWRT / Linux（或 macOS）
- **依赖**：Go 1.21 以上版本（仅编译需要，运行时无需）

## 📦 安装方法（两种 2选1）

### 1. 下载 Release

前往 [Releases](https://github.com/hex-0xd4c0/NjtechAutoLogin/releases) 页面下载对应架构的二进制文件。

### 2. 手动编译

在具有 Go 环境的机器上执行编译：

```bash
# 克隆或下载源码
git clone https://github.com/hex-0xd4c0/NjtechAutoLogin.git
cd NjtechAutoLogin

# 编译当前平台版本
cd src
go build -o njtechlogin .

# 交叉编译 Linux 版（在 macOS 上编译给 OpenWRT 使用）
GOOS=linux GOARCH=arm64 go build -o njtechlogin-linux-arm64 .
GOOS=linux GOARCH=amd64 go build -o njtechlogin-linux-amd64 .
```

编译后得到的二进制文件即可上传到路由器使用。

## 🍽️ 食用方法

### 1. 安装为系统服务（Linux/OpenWRT 推荐）

以 root 身份登录路由器，执行：

```bash
./njtechlogin --install
```

程序将引导你输入账号、密码和运营商，并自动完成以下操作：
- 创建配置文件 `/etc/njtechlogin/config.yml`
- 复制自身到 `/usr/bin/njtechlogin`
- 创建 init 脚本 `/etc/init.d/njtechlogin`
- 启用开机自启并启动服务

如需重新安装，可以加上 `--force` 跳过确认：

```bash
njtechlogin --install --force --usr ************ --pwd ******** --provider telecom
```

### 2. 绿色模式（直接前台运行，macOS / Linux 通用）

如果不想安装为服务，可以直接在前台运行：

```bash
./njtechlogin --start --usr ************ --pwd ******** --provider telecom
```

所有日志会实时输出到终端屏幕，按 `Ctrl+C` 停止。如需将日志写入文件，使用 `--log` 参数：

```bash
./njtechlogin --start --usr ************ --pwd ******** --provider cmcc --log /tmp/njtech.log
```

## ⚙️ 配置说明

配置文件采用 YAML 格式，默认路径为 `/etc/njtechlogin/config.yml`。示例如下：

```yaml
username: "************"
password: "********"
provider: "telecom"      # telecom 或 cmcc
interface: ""            # 可选，仅 Linux 下自动获取
log_file: ""             # 可选，服务模式自动写入轮转日志
```

- `interface`：Linux 版本会自动从路由表获取默认路由接口，通常无需手动配置。
- `log_file`：服务模式下默认写入 `/var/log/njtechlogin/` 轮转日志，无需设置；绿色模式默认输出到终端。

## 🚀 使用方法

### 命令概览

| 命令 | 说明 |
|------|------|
| `--install` | 安装为系统服务（仅 Linux，需 root） |
| `--uninstall` | 卸载服务并删除相关文件（仅 Linux） |
| `--start` | 启动服务（已安装）或绿色模式前台运行 |
| `--stop` | 停止服务或绿色模式进程 |
| `--show` | 查看当前运行状态和配置 |
| `--help, -h` | 显示帮助信息 |

### 选项（与 `--install` 或 `--start` 配合）

| 选项 | 说明 |
|------|------|
| `--usr` <string> | 校园网账号 |
| `--pwd` <string> | 密码 |
| `--provider` <string> | 运营商：`telecom` 或 `cmcc` |
| `--config` <string> | 指定配置文件路径（默认 `/etc/njtechlogin/config.yml`） |
| `--log` <string> | 指定日志文件路径（绿色模式默认终端输出） |
| `--force` | 与 `--install` / `--uninstall` 配合，跳过确认提示 |

### 示例

#### 安装服务（交互式输入）
```bash
njtechlogin --install
```

#### 安装服务（命令行参数）
```bash
njtechlogin --install --usr ************ --pwd ******** --provider telecom
```

#### 启动已安装的服务
```bash
njtechlogin --start
```

#### macOS/Linux 绿色模式前台运行（输出到屏幕）
```bash
njtechlogin --start --usr ************ --pwd ******** --provider cmcc
```

#### 查看状态
```bash
njtechlogin --show
```

#### 停止服务或绿色进程
```bash
njtechlogin --stop
```

#### 卸载服务（带确认）
```bash
njtechlogin --uninstall
```

#### 强制卸载（跳过所有确认）
```bash
njtechlogin --uninstall --force
```

## 📄 日志管理

- **服务模式**（Linux）：日志保存在 `/var/log/njtechlogin/`，按日期轮转（`njtechlogin-YYYY-MM-DD.log`），保留 25 天。
- **绿色模式**：默认直接输出到终端屏幕，可通过 `--log` 参数指定日志目录。
- 守护进程初始化失败时会回退到系统日志（`log.Printf`），确保关键错误不丢失。

## ❓ 常见问题

### Q: 为什么登录失败，提示"宽带账号停机或状态异常"？
A: 这通常是账号被暂时封禁，程序会自动放弃本次重试，等待 15 分钟后重新尝试。

### Q: 绿色模式无法创建 PID 文件？
A: 绿色模式尝试在 `/var/run/` 创建 PID 文件，可能需要 root 权限，但该文件并非必需，不影响正常运行。

### Q: macOS 上运行提示 `--install` 不可用？
A: `--install` / `--uninstall` 仅支持 Linux（OpenWRT）系统服务管理。macOS 请使用绿色模式 `--start` 前台运行。

### Q: 编译时出现 `undefined: cookiejar`？
A: 请确保导入 `net/http/cookiejar`，并执行 `go mod tidy` 下载依赖。

### Q: 如何编译适用于 OpenWRT ARM64 路由器的版本？
A: 使用交叉编译：`cd src && GOOS=linux GOARCH=arm64 go build -o njtechlogin .`

## 🗑️ 卸载（仅 Linux）

运行以下命令即可完全移除：

```bash
njtechlogin --uninstall
```

程序会停止服务、禁用开机自启、删除 init 脚本和可执行文件，并询问是否删除配置及日志目录。使用 `--force` 可跳过所有确认：

```bash
njtechlogin --uninstall --force
```

## 📝 许可证

本项目采用 Apache-2.0 许可证，详情请查看 [LICENSE](LICENSE) 文件。

---

**注意**：本工具仅供学习交流使用，请勿用于非法用途。使用前请确保你拥有校园网的合法使用权。使用本软件造成的一切后果由使用者承担。
