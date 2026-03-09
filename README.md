# NjtechAutoLogin - 泥工校园网自动登录工具
> 此项目大量代码使用AI生成。

**目前无法使用`--install`运行，我也不知道为啥，反正先用`--start`罢，又不是不能用**

[![Go Version](https://img.shields.io/github/go-mod/go-version/hex-0xd4c0/NjtechAutoLogin)](https://golang.org/)

`NjtechAutoLogin` 是一个专为 OpenWrt 路由器设计的校园网自动登录与保活工具，支持南京工业大学的 Dr.COM 认证系统。它可以作为系统服务后台运行，也可以在前台以绿色模式运行，自动检测网络状态并在掉线时重连。

## ✨ 功能特性

- **自动登录**：模拟 Edge 浏览器完整 HTTP 请求流程，支持电信（`telecom`）和移动（`cmcc`）运营商。
- **网络保活**：定时检测互联网连通性（使用微软 NCSI 或苹果 Captive Portal），掉线后自动重试。
- **智能重试**：采用指数退避策略（1s→60s），遇到“账号停机”等错误时等待 5 分钟再试，避免频繁请求导致封禁。
- **日志管理**：日志按日期轮转，保留最近 25 天，默认保存在当前目录的 `logs` 文件夹（绿色模式）或 `/var/log/njtechlogin`（服务模式）。
- **灵活部署**：支持作为 OpenWRT 服务安装（开机自启）或直接运行绿色模式。
- **配置管理**：YAML 配置文件，支持通过命令行参数或交互式输入设置账号密码。
- **状态查看**：通过 `--show` 命令查看运行状态和当前配置（密码隐藏）。

## 🔧 系统要求

- **操作系统**：OpenWRT（Linux）或其他 Linux 发行版（需修改 IP 获取方式）
- **依赖**：
  - `pgrep` / `pkill`（通常已包含）
  - 如果编译需要 Go 环境，请使用 Go 1.16 以上版本

## 📦 安装方法（两种 2选1）

### 1. 下载 Release

### 2. 手动编译

在具有 Go 环境的机器上执行交叉编译：

```bash
# 克隆或下载源码
git clone https://github.com/hex-0xd4c0/NjtechAutoLogin.git
cd NjtechAutoLogin

# 如需编译Darwin（macOS）版本请先cd
# cd darwin

# 交叉编译到 OpenWRT（aarch64）：
GOOS=linux GOARCH=arm64 go build -o repo/linux-arm64/njtech-autologin
# 或为Darwin（macOS）编译：
# GOOS=darwin GOARCH=arm64 go build -o ../repo/darwin-arm64/njtech-autologin
```

在`repo`目录下找到生成的 `njtech-autologin` 上传到 OpenWRT 路由器（如`scp`）。

## 🍽️ 食用方法

### 1. 安装为系统服务（推荐）

以 root 身份登录路由器，执行：

```bash
./njtech-autologin --install
```

程序将引导你输入账号、密码和运营商，并自动完成以下操作：
- 创建配置文件 `/etc/njtechlogin/config.yml`
- 复制自身到 `/usr/bin/njtechlogin`
- 创建 init 脚本 `/etc/init.d/njtechlogin`
- 启用开机自启并启动服务

### 2. 绿色模式（直接运行）

如果不想安装为服务，可以直接运行：

```bash
./njtech-autologin --start
# 注意，此命令会占用终端
```

程序会在当前目录的 `logs` 文件夹中生成日志，并按 `Ctrl+C` 停止。

## ⚙️ 配置说明

配置文件采用 YAML 格式，默认路径为 `/etc/njtechlogin/config.yml`。示例如下：

```yaml
username: "************"
password: "********"
provider: "telecom"      # telecom 或 cmcc
interface: "eth0"        # 可选，指定网卡接口
log_file: ""             # 可选，指定日志文件路径
```

- `interface`：如果路由器有多个接口，可以指定使用的网卡名（如 `eth0`、`wan`），否则自动选择默认路由接口。
- `log_file`：服务模式下默认使用 `/var/log/njtechlogin/` 轮转日志，无需设置；绿色模式默认使用当前目录下的 `logs` 文件夹。

## 🚀 使用方法

### 命令概览

| 命令 | 说明 |
|------|------|
| `--install` | 安装为系统服务（需 root） |
| `--uninstall` | 卸载服务并删除相关文件 |
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
| `--log` <string> | 指定日志文件路径（绿色模式默认自动轮转） |

### 示例

#### 安装服务（交互式输入）
```bash
njtech-autologin --install
```

#### 安装服务（命令行参数）
```bash
njtech-autologin --install --usr ************ --pwd ******** --provider telecom
```

#### 启动已安装的服务
```bash
njtech-autologin --start
```

#### 绿色模式运行（带参数）
```bash
njtech-autologin --start --usr ************ --pwd ******** --provider cmcc
```

#### 查看状态
```bash
njtech-autologin --show
```

#### 停止服务或绿色进程
```bash
njtech-autologin --stop
```

#### 卸载服务
```bash
njtech-autologin --uninstall
```

## 📄 日志管理

- **服务模式**：日志保存在 `/var/log/njtechlogin/`，按日期轮转（`njtechlogin-YYYY-MM-DD.log`），保留 25 天。
- **绿色模式**：默认在当前工作目录的 `logs` 文件夹中生成类似日志文件；如果无法创建则输出到终端。
- 可通过 `--log` 参数指定日志文件路径（例如 `--log /tmp/njtech.log`），但轮转规则仍按日期生成带日期的文件。

## ❓ 常见问题

### Q: 为什么登录失败，提示“宽带账号停机或状态异常”？
A: 这通常是账号被暂时封禁，程序会自动等待 5 分钟后重试。请保持程序运行，或稍等片刻后手动重启。

### Q: 绿色模式无法创建 PID 文件？
A: 绿色模式尝试在 `/var/run/` 创建 PID 文件，可能需要 root 权限，但该文件并非必需，不影响正常运行。

### Q: 如何指定使用某个网卡？
A: 在配置文件中添加 `interface: "eth0"`，或通过命令行参数（但绿色模式不支持直接传递 interface，需写入配置文件）。

### Q: 编译时出现 `undefined: cookiejar`？
A: 请确保导入 `net/http/cookiejar`，并执行 `go mod tidy` 下载依赖。

## 🗑️ 卸载

运行以下命令即可完全移除：

```bash
njtech-autologin --uninstall
```

程序会停止服务、禁用开机自启、删除 init 脚本和可执行文件，并询问是否删除配置及日志目录。

## 📝 许可证

本项目采用 Apache-2.0 许可证，详情请查看 [LICENSE](LICENSE) 文件。

---

**注意**：本工具仅供学习交流使用，请勿用于非法用途。使用前请确保你拥有校园网的合法使用权。使用本软件造成的一切后果由使用者承担。
