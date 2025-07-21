package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	AppName               = "claude-code-router"
	Version               = "0.1.0"
	PidFilename           = ".claude-code-router.pid"
	RefCountFilename      = "claude-code-reference-count.txt"
	DefaultPort           = 3456
	DefaultConfigFilename = "config.json"
)

type Provider struct {
	Name    string   `json:"name"`
	APIBase string   `json:"api_base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
}

type RouterConfig struct {
	Default     string `json:"default"`
	Think       string `json:"think,omitempty"`
	Background  string `json:"background,omitempty"`
	LongContext string `json:"longContext,omitempty"`
	WebSearch   string `json:"webSearch,omitempty"`
}

type Config struct {
	Host      string       `json:"HOST,omitempty"`
	Port      int          `json:"PORT,omitempty"`
	APIKey    string       `json:"APIKEY,omitempty"`
	Providers []Provider   `json:"Providers"`
	Router    RouterConfig `json:"Router"`
}

var (
	homeDir, _  = os.UserHomeDir()
	baseDir     = filepath.Join(homeDir, ".", AppName)
	configPath  = filepath.Join(baseDir, DefaultConfigFilename)
	pidFilePath = filepath.Join(baseDir, PidFilename)
	refFilePath = filepath.Join(os.TempDir(), RefCountFilename)
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "ccr",
		Short: "Claude Code Router",
	}

	rootCmd.AddCommand(startCmd, stopCmd, statusCmd, codeCmd, versionCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// start command
var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runService()
	},
}

// stop command
var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop service",
	Run: func(cmd *cobra.Command, args []string) {
		if err := stopService(); err != nil {
			fmt.Println("Failed to stop the service:", err)
		} else {
			fmt.Println("Service stopped.")
		}
	},
}

// status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service status",
	Run: func(cmd *cobra.Command, args []string) {
		showStatus()
	},
}

// code command
var codeCmd = &cobra.Command{
	Use:   "code [args...]",
	Short: "Execute code command",
	Run: func(cmd *cobra.Command, args []string) {
		if !isServiceRunning() {
			fmt.Println("Service not running, starting...")
			// start in background
			execCmd := exec.Command(os.Args[0], "start")
			execCmd.Stdout = nil
			execCmd.Stderr = nil
			execCmd.Stdin = nil
			execCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := execCmd.Start(); err != nil {
				fmt.Fprintln(os.Stderr, "Failed to start service:", err)
				os.Exit(1)
			}

			if ok := waitForService(10*time.Second, 1*time.Second); !ok {
				fmt.Fprintln(os.Stderr, "Service startup timeout, please run 'ccr start'")
				os.Exit(1)
			}
		}
		executeCodeCommand(args)
	},
}

// version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version info",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("%s version %s\n", AppName, Version)
	},
}

func ensureDirs() error {
	return os.MkdirAll(baseDir, 0o755)
}

func loadConfig() (*Config, error) {
	if err := ensureDirs(); err != nil {
		return nil, err
	}
	data, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	return &cfg, nil
}

func runService() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if isServiceRunning() {
		fmt.Println("Service already running")
		return nil
	}
	// save pid
	if err := ioutil.WriteFile(pidFilePath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return err
	}
	// cleanup on exit
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		cleanupPid()
		os.Exit(0)
	}()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if err := apiKeyAuth(cfg, r); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(err.Error()))
			return
		}
		handleRouter(w, r, cfg)
	})

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	fmt.Println("Service listening on", addr)
	return http.ListenAndServe(addr, nil)
}

func stopService() error {
	if !isServiceRunning() {
		cleanupPid()
		return errors.New("no running service")
	}
	pidData, err := ioutil.ReadFile(pidFilePath)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(pidData))
	if err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	cleanupPid()
	return nil
}

func showStatus() {
	running := isServiceRunning()
	pid := getPid()
	cfg, _ := loadConfig()

	fmt.Println("\n📊 Claude Code Router Status")
	fmt.Println(strings.Repeat("=", 40))
	if running {
		fmt.Println("✅ Status: Running")
		fmt.Println("🆔 PID:", pid)
		fmt.Println("🌐 Port:", cfg.Port)
		fmt.Printf("📡 Endpoint: http://%s:%d\n", cfg.Host, cfg.Port)
		fmt.Println("📄 PID File:", pidFilePath)
		fmt.Println()
		fmt.Println("🚀 Ready!\n  ccr code    # Code with Claude")
	} else {
		fmt.Println("❌ Status: Not Running")
		fmt.Println("\n💡 To start: ccr start")
	}
}

func getPid() int {
	data, err := ioutil.ReadFile(pidFilePath)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(data))
	return pid
}

func isServiceRunning() bool {
	dat, err := ioutil.ReadFile(pidFilePath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(dat))
	if err != nil {
		cleanupPid()
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		cleanupPid()
		return false
	}
	return true
}

func cleanupPid() {
	os.Remove(pidFilePath)
}

func waitForService(timeout, initialDelay time.Duration) bool {
	time.Sleep(initialDelay)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isServiceRunning() {
			time.Sleep(500 * time.Millisecond)
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func incrementRef() {
	count := 0
	if data, err := ioutil.ReadFile(refFilePath); err == nil {
		count, _ = strconv.Atoi(string(data))
	}
	count++
	ioutil.WriteFile(refFilePath, []byte(strconv.Itoa(count)), 0o644)
}

func decrementRef() {
	count := 0
	if data, err := ioutil.ReadFile(refFilePath); err == nil {
		count, _ = strconv.Atoi(string(data))
	}
	if count > 0 {
		count--
	}
	ioutil.WriteFile(refFilePath, []byte(strconv.Itoa(count)), 0o644)
}

func executeCodeCommand(args []string) {
	cfg, _ := loadConfig()
	env := os.Environ()
	env = append(env, fmt.Sprintf("ANTHROPIC_BASE_URL=http://%s:%d", cfg.Host, cfg.Port))
	env = append(env, "API_TIMEOUT_MS=600000")
	if cfg.APIKey != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_API_KEY=%s", cfg.APIKey))
	}

	incrementRef()
	exe := exec.Command("claude", args...)
	exe.Env = env
	exe.Stdout = os.Stdout
	exe.Stderr = os.Stderr
	exe.Stdin = os.Stdin
	exe.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	error := exe.Run()
	decrementRef()
	// stop service if no refs
	if count := readRefCount(); count == 0 {
		stopService()
	}
	if error != nil {
		os.Exit(1)
	}
}

func readRefCount() int {
	if data, err := ioutil.ReadFile(refFilePath); err == nil {
		c, _ := strconv.Atoi(string(data))
		return c
	}
	return 0
}

func apiKeyAuth(cfg *Config, r *http.Request) error {
	if r.URL.Path == "/" || r.URL.Path == "/health" {
		return nil
	}
	if cfg.APIKey == "" {
		return nil
	}
	var token string
	header := r.Header.Get("Authorization")
	if header == "" {
		headers := r.Header["X-API-Key"]
		if len(headers) > 0 {
			token = headers[0]
		}
	} else if strings.HasPrefix(header, "Bearer ") {
		token = strings.TrimPrefix(header, "Bearer ")
	} else {
		token = header
	}
	if token != cfg.APIKey {
		return errors.New("Invalid API key")
	}
	return nil
}

func handleRouter(w http.ResponseWriter, r *http.Request, cfg *Config) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// naive token count
	tokenCount := len(strings.Fields(string(body)))
	model := selectModel(tokenCount, cfg)
	// proxy to provider
	proxyURL := fmt.Sprintf("%s/v1/claude?model=%s", cfg.Providers[0].APIBase, model)
	req, err := http.NewRequest(r.Method, proxyURL, strings.NewReader(string(body)))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header = r.Header.Clone()
	if cfg.Providers[0].APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Providers[0].APIKey)
	}

	rs, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer rs.Body.Close()
	w.WriteHeader(rs.StatusCode)
	io.Copy(w, rs.Body)
}

func selectModel(tokenCount int, cfg *Config) string {
	rc := cfg.Router
	if tokenCount > 60000 && rc.LongContext != "" {
		return rc.LongContext
	}
	// additional logic omitted for brevity
	return rc.Default
}
