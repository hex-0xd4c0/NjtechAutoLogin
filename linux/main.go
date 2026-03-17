package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

const (
	programName    = "njtechlogin"
	configDir      = "/etc/njtechlogin"
	configFile     = configDir + "/config.yml"
	initScriptPath = "/etc/init.d/" + programName
	logDir         = "/var/log/" + programName
	pidFile        = "/var/run/" + programName + ".pid"
	version        = "1.1.0"
)

// 配置结构
type Config struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Provider string `yaml:"provider"` // telecom 或 cmcc
	Iface    string `yaml:"interface,omitempty"`
	LogFile  string `yaml:"log_file,omitempty"`
}

// 日志管理器（支持文件和标准输出）
type Logger struct {
	out       io.Writer     // 实际输出目标
	file      *os.File      // 仅当使用文件时有效
	date      string        // 当前日志日期
	logger    *log.Logger   // 底层log.Logger
	logDir    string        // 日志目录（仅文件模式）
	cleanupCh chan struct{} // 清理循环停止信号
}

// 创建文件日志器
func NewFileLogger(logDir string) (*Logger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	l := &Logger{
		logDir:    logDir,
		cleanupCh: make(chan struct{}),
	}
	if err := l.rotate(); err != nil {
		return nil, err
	}
	go l.cleanupLoop()
	return l, nil
}

// 创建标准输出日志器（绿色模式使用）
func NewStdoutLogger() *Logger {
	return &Logger{
		out:    os.Stdout,
		logger: log.New(os.Stdout, "", log.LstdFlags),
	}
}

// 轮转日志（仅文件模式有效）
func (l *Logger) rotate() error {
	if l.out == os.Stdout { // 标准输出不轮转
		return nil
	}
	now := time.Now()
	newDate := now.Format("2006-01-02")
	if l.date == newDate && l.file != nil {
		return nil
	}
	// 关闭旧文件
	if l.file != nil {
		l.file.Close()
	}
	filename := filepath.Join(l.logDir, fmt.Sprintf("%s-%s.log", programName, newDate))
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	l.file = f
	l.out = f
	l.date = newDate
	l.logger = log.New(l.out, "", log.LstdFlags)
	return nil
}

// 输出一行日志（自动轮转）
func (l *Logger) Printf(format string, v ...interface{}) {
	if l.out == os.Stdout {
		l.logger.Printf(format, v...)
		return
	}
	l.rotate()
	l.logger.Printf(format, v...)
}

func (l *Logger) Println(v ...interface{}) {
	if l.out == os.Stdout {
		l.logger.Println(v...)
		return
	}
	l.rotate()
	l.logger.Println(v...)
}

// 清理过期日志（仅文件模式）
func (l *Logger) cleanupLoop() {
	if l.out == os.Stdout {
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.cleanup()
		case <-l.cleanupCh:
			return
		}
	}
}

func (l *Logger) cleanup() {
	files, err := filepath.Glob(filepath.Join(l.logDir, programName+"-*.log"))
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -25)
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		if fi.ModTime().Before(cutoff) {
			os.Remove(f)
		}
	}
}

// 关闭日志（仅文件模式）
func (l *Logger) Close() {
	if l.out == os.Stdout {
		return
	}
	close(l.cleanupCh)
	if l.file != nil {
		l.file.Close()
	}
}

var (
	globalLogger *Logger
	config       Config
	ctx          context.Context
	cancel       context.CancelFunc
)

func main() {
	if len(os.Args) < 2 {
		printHelp()
		return
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "--help", "-h", "help":
		printHelp()
	case "--install":
		cmdInstall(args)
	case "--uninstall":
		cmdUninstall(args)
	case "--start":
		cmdStart(args)
	case "--stop":
		cmdStop(args)
	case "--show":
		cmdShow(args)
	case "--daemon": // 守护模式专用参数，由 init 脚本调用
		runDaemon()
	default:
		fmt.Printf("未知命令: %s\n", cmd)
		printHelp()
	}
}

func printHelp() {
	fmt.Printf(`%s 版本 %s
校园网自动登录与保活工具（OpenWRT 版）

用法:
  %s [命令] [选项]

命令:
  --install             安装为系统服务（需要root）
  --uninstall           卸载服务并删除相关文件
  --start               启动服务（如果已安装）或绿色模式前台运行
  --stop                停止服务或绿色模式进程
  --show                查看当前运行状态和配置（如果已安装）
  --help, -h            显示此帮助

选项（与install或start配合）:
  --usr 用户名          校园网账号
  --pwd 密码            校园网密码
  --provider 提供商     运营商: telecom 或 cmcc
  --config 配置文件     指定配置文件路径（默认为/etc/njtechlogin/config.yml）
  --log 日志文件        指定日志文件路径（守护模式默认自动轮转）

示例:
  %s --install --usr 202521110072 --pwd Liyue0528 --provider telecom
  %s --start --usr 202521110072 --pwd Liyue0528 --provider cmcc   # 绿色模式运行
`, programName, version, programName, programName, programName)
}

// 解析子命令公共选项
func parseCommonFlags(args []string) (username, password, provider, cfgPath, logPath string) {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.StringVar(&username, "usr", "", "校园网账号")
	fs.StringVar(&password, "pwd", "", "密码")
	fs.StringVar(&provider, "provider", "", "运营商: telecom/cmcc")
	fs.StringVar(&cfgPath, "config", configFile, "配置文件路径")
	fs.StringVar(&logPath, "log", "", "日志文件路径")
	fs.SetOutput(io.Discard)
	fs.Parse(args)
	return
}

// 检查是否已安装（判断init脚本和配置文件是否存在）
func isInstalled() bool {
	_, err1 := os.Stat(initScriptPath)
	_, err2 := os.Stat(configFile)
	return err1 == nil && err2 == nil
}

// 检查是否已有实例在运行（通过PID文件）
func isRunning() bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// 发送信号0检查进程是否存在
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// 创建PID文件
func writePidFile() error {
	pid := os.Getpid()
	return os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644)
}

// 删除PID文件
func removePidFile() {
	os.Remove(pidFile)
}

// 获取默认路由接口的IP
func getOutboundIP() (net.IP, string, error) {
	// 解析 /proc/net/route 获取默认路由接口
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil, "", err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return nil, "", errors.New("无法解析路由表")
	}
	var iface string
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// 目标地址为 00000000 表示默认路由
		if fields[1] == "00000000" {
			iface = fields[0]
			break
		}
	}
	if iface == "" {
		return nil, "", errors.New("未找到默认路由接口")
	}
	// 获取接口IP
	ief, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, "", err
	}
	addrs, err := ief.Addrs()
	if err != nil {
		return nil, "", err
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP, iface, nil
		}
	}
	return nil, "", fmt.Errorf("接口 %s 没有有效的IPv4地址", iface)
}

// 模拟浏览器HTTP客户端（启用CookieJar）
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return nil // 允许重定向
	},
}

func init() {
	jar, err := cookiejar.New(nil)
	if err == nil {
		httpClient.Jar = jar
	}
}

// 创建带有完整浏览器头的请求
func newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	// 完整模拟 Edge on Linux (OpenWRT 通常为 Linux)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Cache-Control", "max-age=0")
	// Referer 将在具体请求中设置
	return req, nil
}

// 读取响应体并自动解压（如果内容为gzip）
func readResponseBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// 检查是否gzip压缩（通过魔数）
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		globalLogger.Printf("检测到gzip压缩数据，尝试解压")
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("创建gzip reader失败: %v", err)
		}
		defer gr.Close()
		decompressed, err := io.ReadAll(gr)
		if err != nil {
			return nil, fmt.Errorf("gzip解压失败: %v", err)
		}
		return decompressed, nil
	}
	return body, nil
}

// 访问门户首页，模拟浏览器初始请求
func visitHomepage() error {
	homepageURL := "http://10.50.255.11/"
	req, err := newRequest("GET", homepageURL, nil)
	if err != nil {
		return err
	}
	// 首页的Referer通常为空
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("访问首页失败: %v", err)
	}
	body, err := readResponseBody(resp)
	if err != nil {
		return fmt.Errorf("读取首页响应失败: %v", err)
	}
	globalLogger.Printf("首页访问完成，状态码: %d，内容长度: %d", resp.StatusCode, len(body))
	if len(body) > 200 {
		globalLogger.Printf("首页内容前200字节: %s", body[:200])
	}
	return nil
}

// 登录校园网
func login(username, password, provider string) error {
	// 第一步：模拟访问首页
	if err := visitHomepage(); err != nil {
		globalLogger.Printf("访问首页失败（不影响登录尝试）: %v", err)
		// 即使首页失败，也继续尝试登录
	}

	ip, iface, err := getOutboundIP()
	if err != nil {
		return fmt.Errorf("获取本机IP失败: %v", err)
	}
	ipStr := ip.String()
	globalLogger.Printf("使用IP: %s (接口: %s)", ipStr, iface)

	// 第二步：获取配置（模拟完整流程）
	loadConfigURL := fmt.Sprintf("http://10.50.255.11:801/eportal/portal/page/loadConfig?callback=dr1001&program_index=&wlan_vlan_id=1&wlan_user_ip=%s&wlan_user_ipv6=&wlan_user_ssid=&wlan_user_areaid=&wlan_ac_ip=&wlan_ap_mac=000000000000&gw_id=000000000000&jsVersion=4.X&v=%d&lang=zh",
		base64.StdEncoding.EncodeToString([]byte(ipStr)), time.Now().UnixNano()/1e6)
	req, err := newRequest("GET", loadConfigURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Referer", "http://10.50.255.11/")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("loadConfig请求失败: %v", err)
	}
	body, err := readResponseBody(resp)
	if err != nil {
		return fmt.Errorf("读取loadConfig响应失败: %v", err)
	}
	if !bytes.Contains(body, []byte(`"code":1`)) {
		globalLogger.Printf("loadConfig返回异常: %s", string(body))
		// 继续尝试登录
	}

	// 第三步：登录
	userAccount := fmt.Sprintf(",0,%s@%s", username, provider)
	loginURL := fmt.Sprintf("http://10.50.255.11:801/eportal/portal/login?callback=dr1003&login_method=1&user_account=%s&user_password=%s&wlan_user_ip=%s&wlan_user_ipv6=&wlan_user_mac=000000000000&wlan_ac_ip=&wlan_ac_name=&jsVersion=4.1.3&terminal_type=1&lang=zh-cn&v=%d&lang=zh",
		url.QueryEscape(userAccount), url.QueryEscape(password), ipStr, time.Now().UnixNano()/1e6)
	req, err = newRequest("GET", loginURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Referer", "http://10.50.255.11/")
	resp, err = httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("登录请求失败: %v", err)
	}
	body, err = readResponseBody(resp)
	if err != nil {
		return fmt.Errorf("读取登录响应失败: %v", err)
	}

	globalLogger.Printf("登录响应原始内容: %s", string(body))

	// 提取JSON部分
	re := regexp.MustCompile(`dr1003\s*\(\s*({.*?})\s*\)\s*`)
	matches := re.FindSubmatch(body)
	if len(matches) < 2 {
		return fmt.Errorf("登录响应解析失败，无法提取JSON: %s", string(body))
	}
	var result struct {
		Result int    `json:"result"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(matches[1], &result); err != nil {
		return fmt.Errorf("JSON解析失败: %v, 原始片段: %s", err, string(matches[1]))
	}
	if result.Result != 1 {
		// 特殊处理账号停用等错误，避免频繁重试
		if strings.Contains(result.Msg, "停机") || strings.Contains(result.Msg, "状态异常") {
			return fmt.Errorf("登录失败: %s (账号可能被暂时封禁，请等待几分钟后再试)", result.Msg)
		}
		return fmt.Errorf("登录失败: %s", result.Msg)
	}
	globalLogger.Println("登录成功")
	return nil
}

// 检测互联网连接状态
func checkInternet() bool {
	urls := []string{
		"http://www.msftncsi.com/ncsi.txt",
		"http://captive.apple.com/hotspot-detect.html",
	}
	for _, u := range urls {
		req, err := newRequest("GET", u, nil)
		if err != nil {
			continue
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			continue
		}
		body, err := readResponseBody(resp)
		if err != nil {
			continue
		}
		if u == "http://www.msftncsi.com/ncsi.txt" && strings.TrimSpace(string(body)) == "Microsoft NCSI" {
			return true
		}
		if u == "http://captive.apple.com/hotspot-detect.html" && strings.Contains(string(body), "Success") {
			return true
		}
		if strings.Contains(string(body), "Dr.COMWebLoginID") {
			return false
		}
	}
	return false
}

// 监控循环
func monitorLoop(ctx context.Context) {
	// 先尝试登录一次
	if err := login(config.Username, config.Password, config.Provider); err != nil {
		globalLogger.Printf("初始登录失败: %v", err)
		// 如果是账号封禁类错误，等待较长时间再重试
		if strings.Contains(err.Error(), "停机") || strings.Contains(err.Error(), "状态异常") {
			globalLogger.Println("检测到账号异常，等待5分钟后重试...")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Minute):
				// 继续循环，会再次尝试登录
			}
		}
	} else {
		globalLogger.Println("初始登录成功")
	}

	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second
	const checkInterval = 30 * time.Second

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			globalLogger.Println("收到退出信号，停止监控")
			return
		case <-ticker.C:
			if checkInternet() {
				backoff = 1 * time.Second
				globalLogger.Println("网络连接正常")
			} else {
				globalLogger.Println("检测到网络掉线，尝试重新登录")
				for {
					err := login(config.Username, config.Password, config.Provider)
					if err == nil {
						globalLogger.Println("重新登录成功")
						backoff = 1 * time.Second
						break
					}
					globalLogger.Printf("重新登录失败: %v，%v后重试", err, backoff)
					// 如果是账号封禁类错误，直接跳到较长的等待
					if strings.Contains(err.Error(), "停机") || strings.Contains(err.Error(), "状态异常") {
						globalLogger.Println("账号异常，等待5分钟后再试")
						select {
						case <-ctx.Done():
							return
						case <-time.After(5 * time.Minute):
						}
						backoff = 1 * time.Second // 重置退避
						break                     // 退出内层循环，回到外层循环重新检查网络
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}
		}
	}
}

// 绿色模式前台运行
func runGreen(username, password, provider, logPath string) {
	// 确定日志输出目标
	if logPath == "" {
		// 默认在当前目录下的 logs 文件夹中创建日志
		wd, err := os.Getwd()
		if err == nil {
			defaultLogDir := filepath.Join(wd, "logs")
			if err := os.MkdirAll(defaultLogDir, 0755); err == nil {
				var lerr error
				globalLogger, lerr = NewFileLogger(defaultLogDir)
				if lerr == nil {
					defer globalLogger.Close()
				} else {
					fmt.Printf("无法创建日志文件，回退到终端输出: %v\n", lerr)
					globalLogger = NewStdoutLogger()
				}
			} else {
				fmt.Printf("无法创建日志目录，回退到终端输出: %v\n", err)
				globalLogger = NewStdoutLogger()
			}
		} else {
			fmt.Printf("无法获取当前目录，回退到终端输出: %v\n", err)
			globalLogger = NewStdoutLogger()
		}
	} else {
		// 用户指定了日志文件，使用文件日志
		logDirForFile := filepath.Dir(logPath)
		var err error
		globalLogger, err = NewFileLogger(logDirForFile)
		if err != nil {
			fmt.Printf("初始化日志失败: %v\n", err)
			os.Exit(1)
		}
		defer globalLogger.Close()
	}
	globalLogger.Printf("绿色模式启动，按 Ctrl+C 停止")
	globalLogger.Printf("日志文件保存在: %s", getLoggerPath())

	config = Config{
		Username: username,
		Password: password,
		Provider: provider,
	}

	// 绿色模式也尝试创建PID文件（/var/run/ 可能需要root权限，忽略错误）
	if err := writePidFile(); err != nil {
		globalLogger.Printf("警告: 无法创建PID文件: %v (绿色模式无需此文件)", err)
	} else {
		defer removePidFile()
	}

	ctx, cancel = context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		globalLogger.Println("收到退出信号")
		cancel()
	}()

	monitorLoop(ctx)
}

// 守护模式（后台运行）
func runDaemon() {
	// 检查是否已有实例运行
	if isRunning() {
		fmt.Println("已有实例在运行")
		os.Exit(1)
	}
	if err := writePidFile(); err != nil {
		fmt.Printf("无法创建PID文件: %v\n", err)
		os.Exit(1)
	}
	defer removePidFile()

	// 初始化日志（守护模式使用 /var/log/njtechlogin/）
	var err error
	globalLogger, err = NewFileLogger(logDir)
	if err != nil {
		fmt.Printf("初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	defer globalLogger.Close()

	globalLogger.Printf("守护模式启动，PID=%d", os.Getpid())

	// 加载配置
	data, err := os.ReadFile(configFile)
	if err != nil {
		globalLogger.Printf("读取配置文件失败: %v", err)
		os.Exit(1)
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		globalLogger.Printf("解析配置文件失败: %v", err)
		os.Exit(1)
	}

	ctx, cancel = context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		globalLogger.Println("收到退出信号")
		cancel()
	}()

	monitorLoop(ctx)
	globalLogger.Println("程序退出")
}

// 获取当前日志文件路径（用于提示）
func getLoggerPath() string {
	if globalLogger == nil {
		return "未知"
	}
	if globalLogger.file != nil {
		return globalLogger.file.Name()
	}
	return "终端输出"
}

// 命令: --install
func cmdInstall(args []string) {
	username, password, provider, cfgPath, _ := parseCommonFlags(args)

	// 如果未提供账号密码，交互式输入
	if username == "" {
		fmt.Print("请输入校园网账号: ")
		fmt.Scanln(&username)
	}
	if password == "" {
		fmt.Print("请输入密码: ")
		bytePwd, _ := term.ReadPassword(int(syscall.Stdin))
		password = string(bytePwd)
		fmt.Println()
	}
	if provider == "" {
		fmt.Print("请输入运营商 (telecom/cmcc): ")
		fmt.Scanln(&provider)
	}
	// 验证provider
	if provider != "telecom" && provider != "cmcc" {
		fmt.Println("运营商必须为 telecom 或 cmcc")
		os.Exit(1)
	}

	// 创建配置目录
	if err := os.MkdirAll(configDir, 0755); err != nil {
		fmt.Printf("创建配置目录失败: %v\n", err)
		os.Exit(1)
	}

	// 写入配置文件
	cfg := Config{
		Username: username,
		Password: password,
		Provider: provider,
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		fmt.Printf("序列化配置失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		fmt.Printf("写入配置文件失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("配置文件已写入 %s\n", cfgPath)

	// 复制自身到 /usr/bin
	selfPath, err := os.Executable()
	if err != nil {
		fmt.Printf("获取自身路径失败: %v\n", err)
		os.Exit(1)
	}
	destPath := "/usr/bin/" + programName
	if err := copyFile(selfPath, destPath); err != nil {
		fmt.Printf("复制可执行文件失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.Chmod(destPath, 0755); err != nil {
		fmt.Printf("设置可执行权限失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("可执行文件已复制到 %s\n", destPath)

	// 创建init脚本
	initScript := `#!/bin/sh /etc/rc.common

START=99
STOP=10

USE_PROCD=0

start() {
    start-stop-daemon -S -b -q -m -p /var/run/` + programName + `.pid -x /usr/bin/` + programName + ` -- --daemon
}

stop() {
    start-stop-daemon -K -q -p /var/run/` + programName + `.pid
    rm -f /var/run/` + programName + `.pid
}

restart() {
    stop
    sleep 1
    start
}
`
	if err := os.WriteFile(initScriptPath, []byte(initScript), 0755); err != nil {
		fmt.Printf("创建init脚本失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("init脚本已创建 %s\n", initScriptPath)

	// 启用开机自启
	cmd := exec.Command("/etc/init.d/"+programName, "enable")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("启用开机自启失败: %v\n输出: %s", err, out)
		os.Exit(1)
	}
	fmt.Println("开机自启已启用")

	// 启动服务
	cmd = exec.Command("/etc/init.d/"+programName, "start")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("启动服务失败: %v\n输出: %s", err, out)
		os.Exit(1)
	}
	fmt.Println("服务已启动")
}

// 复制文件
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// 命令: --uninstall
func cmdUninstall(args []string) {
	// 停止服务
	stopService()
	// 禁用开机自启
	exec.Command("/etc/init.d/"+programName, "disable").Run()
	// 删除init脚本
	os.Remove(initScriptPath)
	// 删除可执行文件
	os.Remove("/usr/bin/" + programName)
	// 删除配置目录（可选，提示用户）
	fmt.Print("是否删除配置目录 " + configDir + " ? (y/N): ")
	var ans string
	fmt.Scanln(&ans)
	if strings.ToLower(ans) == "y" {
		os.RemoveAll(configDir)
		fmt.Println("配置目录已删除")
	}
	// 删除日志目录
	fmt.Print("是否删除日志目录 " + logDir + " ? (y/N): ")
	fmt.Scanln(&ans)
	if strings.ToLower(ans) == "y" {
		os.RemoveAll(logDir)
		fmt.Println("日志目录已删除")
	}
	// 删除PID文件
	os.Remove(pidFile)
	fmt.Println("卸载完成")
}

// 命令: --start
func cmdStart(args []string) {
	username, password, provider, cfgPath, logPath := parseCommonFlags(args)

	if isInstalled() {
		// 已安装，启动服务
		if isRunning() {
			fmt.Println("服务已在运行")
			return
		}
		startService()
	} else {
		// 未安装，绿色模式
		// 尝试从配置文件读取（如果存在）
		if username == "" || password == "" || provider == "" {
			if _, err := os.Stat(cfgPath); err == nil {
				data, err := os.ReadFile(cfgPath)
				if err == nil {
					var cfg Config
					if err := yaml.Unmarshal(data, &cfg); err == nil {
						if username == "" {
							username = cfg.Username
						}
						if password == "" {
							password = cfg.Password
						}
						if provider == "" {
							provider = cfg.Provider
						}
						config.Iface = cfg.Iface
					}
				}
			}
		}
		// 如果仍缺少，交互式输入
		if username == "" {
			fmt.Print("请输入校园网账号: ")
			fmt.Scanln(&username)
		}
		if password == "" {
			fmt.Print("请输入密码: ")
			bytePwd, _ := term.ReadPassword(int(syscall.Stdin))
			password = string(bytePwd)
			fmt.Println()
		}
		if provider == "" {
			fmt.Print("请输入运营商 (telecom/cmcc): ")
			fmt.Scanln(&provider)
		}
		if provider != "telecom" && provider != "cmcc" {
			fmt.Println("运营商必须为 telecom 或 cmcc")
			os.Exit(1)
		}
		runGreen(username, password, provider, logPath)
	}
}

// 启动服务
func startService() {
	cmd := exec.Command("/etc/init.d/"+programName, "start")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("启动服务失败: %v\n输出: %s", err, out)
		os.Exit(1)
	}
	fmt.Println("服务已启动")
}

// 停止服务
func stopService() {
	cmd := exec.Command("/etc/init.d/"+programName, "stop")
	cmd.Run()
	// 等待进程退出
	time.Sleep(2 * time.Second)
}

// 命令: --stop
func cmdStop(args []string) {
	if isInstalled() {
		// 已安装，停止服务
		if !isRunning() {
			fmt.Println("服务未运行")
			return
		}
		stopService()
		if isRunning() {
			fmt.Println("服务停止失败")
		} else {
			fmt.Println("服务已停止")
		}
	} else {
		// 未安装，尝试停止绿色模式进程
		pid := os.Getpid()
		cmd := exec.Command("pgrep", "-f", programName)
		out, err := cmd.Output()
		if err != nil {
			fmt.Println("未找到运行中的绿色模式进程")
			return
		}
		pids := strings.Fields(string(out))
		found := false
		for _, p := range pids {
			if p == strconv.Itoa(pid) {
				continue
			}
			if proc, err := os.FindProcess(strToInt(p)); err == nil {
				proc.Signal(syscall.SIGTERM)
				found = true
				fmt.Printf("已向进程 %s 发送终止信号\n", p)
			}
		}
		if !found {
			fmt.Println("未找到其他运行中的绿色模式进程")
		}
	}
}

// 命令: --show
func cmdShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	var cfgPath string
	fs.StringVar(&cfgPath, "config", configFile, "配置文件路径")
	fs.SetOutput(io.Discard)
	fs.Parse(args)

	installed := isInstalled()
	running := isRunning()

	// 检测绿色进程
	greenRunning := false
	pid := os.Getpid()
	cmd := exec.Command("pgrep", "-f", programName)
	out, err := cmd.Output()
	if err == nil {
		pids := strings.Fields(string(out))
		for _, p := range pids {
			if p != strconv.Itoa(pid) {
				greenRunning = true
				break
			}
		}
	} else {
		// pgrep 失败，可能没有该命令，尝试用 ps
		psCmd := exec.Command("ps", "aux")
		psOut, err := psCmd.Output()
		if err == nil {
			lines := strings.Split(string(psOut), "\n")
			for _, line := range lines {
				if strings.Contains(line, programName) && !strings.Contains(line, "grep") && !strings.Contains(line, "show") {
					fields := strings.Fields(line)
					if len(fields) > 1 {
						p := fields[1]
						if p != strconv.Itoa(pid) {
							greenRunning = true
							break
						}
					}
				}
			}
		}
	}

	if installed {
		if running {
			fmt.Println("状态: 已安装服务，正在运行")
		} else {
			fmt.Println("状态: 已安装服务，未运行")
		}
		// 打印配置
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			fmt.Printf("读取配置文件失败: %v\n", err)
			return
		}
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			fmt.Printf("解析配置文件失败: %v\n", err)
			return
		}
		fmt.Println("配置信息:")
		fmt.Printf("  账号: %s\n", cfg.Username)
		if cfg.Password != "" {
			fmt.Printf("  密码: %s\n", maskPassword(cfg.Password))
		}
		fmt.Printf("  运营商: %s\n", cfg.Provider)
		if cfg.Iface != "" {
			fmt.Printf("  接口: %s\n", cfg.Iface)
		}
		if cfg.LogFile != "" {
			fmt.Printf("  日志文件: %s\n", cfg.LogFile)
		}
		fmt.Printf("  配置文件路径: %s\n", cfgPath)
	} else {
		if greenRunning {
			fmt.Println("状态: 绿色模式运行中")
		} else {
			fmt.Println("状态: 未安装，也未运行")
		}
	}
}

func maskPassword(pwd string) string {
	if len(pwd) <= 4 {
		return "****"
	}
	return pwd[:2] + "****" + pwd[len(pwd)-2:]
}

func strToInt(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}
