package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
	programName = "njtechlogin"
	version     = "1.0.3" // 版本升级
)

// 用户目录下的配置目录
var (
	homeDir, _ = os.UserHomeDir()
	configDir  = filepath.Join(homeDir, ".njtechlogin")
	configFile = filepath.Join(configDir, "config.yml")
	logDir     = filepath.Join(configDir, "logs")
	pidFile    = filepath.Join(configDir, "pid")
)

// 配置结构
type Config struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Provider string `yaml:"provider"`            // telecom 或 cmcc
	Iface    string `yaml:"interface,omitempty"` // 指定接口，如 en0
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
	case "--start":
		cmdStart(args)
	case "--stop":
		cmdStop(args)
	case "--show":
		cmdShow(args)
	default:
		fmt.Printf("未知命令: %s\n", cmd)
		printHelp()
	}
}

func printHelp() {
	fmt.Printf(`%s 版本 %s
校园网自动登录与保活工具（macOS 版）

用法:
  %s [命令] [选项]

命令:
  --start               以绿色模式前台运行（可带参数）
  --stop                停止正在运行的绿色模式进程
  --show                查看当前运行状态和配置
  --help, -h            显示此帮助

选项（与start配合）:
  --usr 用户名          校园网账号
  --pwd 密码            校园网密码
  --provider 提供商     运营商: telecom 或 cmcc
  --config 配置文件     指定配置文件路径（默认为 ~/.njtechlogin/config.yml）
  --log 日志文件        指定日志文件路径（默认自动轮转）

示例:
  %s --start --usr 202521110072 --pwd Liyue0528 --provider telecom
  %s --show
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

// 检查是否已配置（配置文件是否存在）
func isConfigured() bool {
	_, err := os.Stat(configFile)
	return err == nil
}

// 检查是否已有绿色模式进程在运行（通过PID文件或进程名）
func isRunning() bool {
	// 先尝试PID文件
	data, err := os.ReadFile(pidFile)
	if err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			process, err := os.FindProcess(pid)
			if err == nil {
				// 发送信号0检查进程是否存在
				err = process.Signal(syscall.Signal(0))
				if err == nil {
					return true
				}
			}
		}
	}
	// PID文件无效，尝试用pgrep查找进程
	cmd := exec.Command("pgrep", "-f", programName)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	pids := strings.Fields(string(out))
	// 排除当前进程
	currentPid := strconv.Itoa(os.Getpid())
	for _, p := range pids {
		if p != currentPid {
			return true
		}
	}
	return false
}

// 创建PID文件（确保目录存在）
func writePidFile() error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	pid := os.Getpid()
	return os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644)
}

// 删除PID文件
func removePidFile() {
	os.Remove(pidFile)
}

// 获取出口IP（优先使用指定接口，否则自动选择第一个可用的非回环IPv4地址）
func getOutboundIP(ifaceName string) (net.IP, string, error) {
	// 如果指定了接口，直接使用该接口
	if ifaceName != "" {
		iface, err := net.InterfaceByName(ifaceName)
		if err != nil {
			return nil, "", fmt.Errorf("指定接口 %s 不存在: %v", ifaceName, err)
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, "", err
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP, iface.Name, nil
			}
		}
		return nil, "", fmt.Errorf("接口 %s 没有有效的IPv4地址", ifaceName)
	}

	// 否则自动选择：遍历所有 up 且非 loopback 的接口，取第一个有 IPv4 地址的接口
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, "", err
	}

	for _, iface := range interfaces {
		// 只考虑已启用的接口
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		// 排除回环接口
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP, iface.Name, nil
			}
		}
	}

	// 如果没找到合适接口，回退到UDP方法
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, "", fmt.Errorf("无法获取出口IP: %v", err)
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP, "", nil
}

// 模拟浏览器HTTP客户端（启用CookieJar以自动处理Cookie）
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return nil // 允许重定向
	},
}

// 初始化时设置CookieJar（Go默认Client没有Jar，需要手动创建）
func init() {
	// 创建一个CookieJar
	jar, err := cookiejar.New(nil)
	if err != nil {
		// 如果失败，使用默认（无cookie）
		return
	}
	httpClient.Jar = jar
}

// 创建带有完整浏览器头的请求
func newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	// 完整模拟 Edge on macOS
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Cache-Control", "max-age=0")
	// Referer 将在具体请求中根据情况设置
	return req, nil
}

// 访问门户首页，模拟浏览器初始请求
func visitHomepage() error {
	homepageURL := "http://10.50.255.11/"
	req, err := newRequest("GET", homepageURL, nil)
	if err != nil {
		return err
	}
	// 首页的Referer通常为空或为自身，这里不加Referer
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("访问首页失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	globalLogger.Printf("首页访问完成，状态码: %d，内容长度: %d", resp.StatusCode, len(body))
	// 可选：记录部分内容用于调试，但避免过多日志
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

	ip, iface, err := getOutboundIP(config.Iface)
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
	req.Header.Set("Referer", "http://10.50.255.11/") // 设置Referer
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("loadConfig请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
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
	req.Header.Set("Referer", "http://10.50.255.11/") // 设置Referer
	resp, err = httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("登录请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)

	// 输出原始响应以便调试
	globalLogger.Printf("登录响应原始内容: %s", string(body))

	// 提取JSON部分（处理可能的前后空格）
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
		// 检测页面一般不需要Referer
		resp, err := httpClient.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
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

	// 确保配置目录存在（用于PID文件）
	if err := os.MkdirAll(configDir, 0755); err != nil {
		globalLogger.Printf("警告: 无法创建配置目录: %v", err)
	} else {
		if err := writePidFile(); err != nil {
			globalLogger.Printf("警告: 无法创建PID文件: %v", err)
		} else {
			defer removePidFile()
		}
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

// 命令: --start
func cmdStart(args []string) {
	username, password, provider, cfgPath, logPath := parseCommonFlags(args)

	// 如果未提供账号密码，尝试从配置文件读取
	if username == "" || password == "" || provider == "" {
		if isConfigured() {
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
					// 保存接口配置供后续使用
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

	if isRunning() {
		fmt.Println("已有实例在运行，请先停止或使用 --stop")
		os.Exit(1)
	}

	runGreen(username, password, provider, logPath)
}

// 命令: --stop
func cmdStop(args []string) {
	if !isRunning() {
		fmt.Println("没有找到运行中的实例")
		return
	}

	// 尝试通过PID文件停止
	data, err := os.ReadFile(pidFile)
	if err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			process, err := os.FindProcess(pid)
			if err == nil {
				process.Signal(syscall.SIGTERM)
				time.Sleep(2 * time.Second)
				err = process.Signal(syscall.Signal(0))
				if err != nil {
					fmt.Println("已停止")
					os.Remove(pidFile)
					return
				}
			}
		}
	}

	// 回退到pkill
	cmd := exec.Command("pkill", "-f", programName)
	cmd.Run()
	time.Sleep(2 * time.Second)
	if isRunning() {
		fmt.Println("停止失败，请手动检查")
	} else {
		fmt.Println("已停止")
		os.Remove(pidFile)
	}
}

// 命令: --show
func cmdShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	var cfgPath string
	fs.StringVar(&cfgPath, "config", configFile, "配置文件路径")
	fs.SetOutput(io.Discard)
	fs.Parse(args)

	configured := isConfigured()
	running := isRunning()

	if configured {
		fmt.Println("状态: 已配置")
	} else {
		fmt.Println("状态: 未配置")
	}
	if running {
		fmt.Println("运行状态: 运行中")
	} else {
		fmt.Println("运行状态: 未运行")
	}

	if configured {
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
		fmt.Println("提示: 可以使用 --start 并带上参数运行，或创建配置文件")
	}
}

func maskPassword(pwd string) string {
	if len(pwd) <= 4 {
		return "****"
	}
	return pwd[:2] + "****" + pwd[len(pwd)-2:]
}
