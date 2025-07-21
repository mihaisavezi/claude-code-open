package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

const (
	AppName               = ".claude-code-router"
	Version               = "0.1.0"
	PidFilename           = ".claude-code-router.pid"
	RefCountFilename      = "claude-code-reference-count.txt"
	DefaultPort           = 6969
	DefaultConfigFilename = "config.json"
)

var (
	homeDir, _  = os.UserHomeDir()
	baseDir     = filepath.Join(homeDir, ".", AppName)
	configPath  = filepath.Join(baseDir, DefaultConfigFilename)
	pidFilePath = filepath.Join(baseDir, PidFilename)
	refFilePath = filepath.Join(os.TempDir(), RefCountFilename)
)

// Global logger and atomic config
var logger *slog.Logger
var configValue atomic.Value // *Config

func init() {
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger = slog.New(h)
}

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

func main() {
	root := &cobra.Command{Use: "ccr", Short: "Claude Code Router"}
	root.AddCommand(startCmd, stopCmd, statusCmd, codeCmd, versionCmd)
	if err := root.Execute(); err != nil {
		logger.Error("CLI execution failed", "error", err)
		os.Exit(1)
	}
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start service",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger.Info("Starting service")
		cfg, err := loadConfig()
		if err != nil {
			logger.Error("Load config failed", "error", err)
			return err
		}
		configValue.Store(cfg)
		// watch config in background
		go watchConfigFile()
		return runServiceLoop()
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop service",
	Run: func(cmd *cobra.Command, args []string) {
		logger.Info("Stopping service")
		if err := stopService(); err != nil {
			logger.Error("Stop failed", "error", err)
			os.Exit(1)
		}
		logger.Info("Service stopped")
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service status",
	Run: func(cmd *cobra.Command, args []string) {
		logger.Info("Fetching status")
		showStatus()
	},
}

var codeCmd = &cobra.Command{
	Use:   "code [args...]",
	Short: "Execute code through service",
	Run: func(cmd *cobra.Command, args []string) {
		logger.Info("Code command", "args", args)
		if !isServiceRunning() {
			logger.Warn("Service not running, starting")
			execCmd := exec.Command(os.Args[0], "start")
			execCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := execCmd.Start(); err != nil {
				logger.Error("Failed start", "error", err)
				os.Exit(1)
			}
			if ok := waitForService(10*time.Second, 1*time.Second); !ok {
				logger.Error("Startup timeout")
				os.Exit(1)
			}
		}
		executeCode(args)
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version info",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("%s version %s\n", AppName, Version)
	},
}

func runServiceLoop() error {
	cfg := getConfig()
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	// HTTP server with graceful shutdown
	srv := &http.Server{Addr: addr, Handler: httpMux()}

	// write PID
	if err := os.WriteFile(pidFilePath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		logger.Error("PID file write failed", "error", err)
		return err
	}
	logger.Info("PID written", "pid", os.Getpid())

	// start server
	go func() {
		logger.Info("Listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Listen error", "error", err)
		}
	}()

	// wait signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs
	logger.Info("Shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("Graceful shutdown failed", "error", err)
		return err
	}
	cleanupPid()
	logger.Info("Shutdown complete")
	return nil
}

func httpMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/", proxyHandler)
	return mux
}

func watchConfigFile() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error("Watcher init failed", "error", err)
		return
	}
	defer watcher.Close()

	if err := watcher.Add(configPath); err != nil {
		logger.Error("Watch add failed", "error", err)
		return
	}

	for {
		select {
		case ev := <-watcher.Events:
			if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				logger.Info("Config change detected")
				if cfg, err := loadConfig(); err == nil {
					configValue.Store(cfg)
					logger.Info("Config reloaded")
				} else {
					logger.Error("Reload failed", "error", err)
				}
			}
		case err := <-watcher.Errors:
			logger.Error("Watcher error", "error", err)
		}
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
	logger.Debug("Health check", "remote", r.RemoteAddr)
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	if err := apiKeyAuth(cfg, r); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(err.Error()))
		logger.Warn("Unauthorized", "remote", r.RemoteAddr)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		logger.Error("Read body failed", "error", err)
		return
	}
	count := len(strings.Fields(string(body)))
	model := selectModel(count, cfg)
	url := fmt.Sprintf("%s/v1/claude?model=%s", cfg.Providers[0].APIBase, model)
	logger.Debug("Proxying", "url", url, "tokens", count)

	req, _ := http.NewRequest(r.Method, url, io.NopCloser(strings.NewReader(string(body))))
	req.Header = r.Header.Clone()
	if key := cfg.Providers[0].APIKey; key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rs, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		logger.Error("Upstream error", "error", err)
		return
	}
	defer rs.Body.Close()
	w.WriteHeader(rs.StatusCode)
	io.Copy(w, rs.Body)
	logger.Debug("Response forwarded", "status", rs.StatusCode)
}

func loadConfig() (*Config, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(configPath)
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
	logger.Debug("Config loaded", "cfg", cfg)
	return &cfg, nil
}

func getConfig() *Config {
	if v := configValue.Load(); v != nil {
		return v.(*Config)
	}
	cfg, _ := loadConfig()
	return cfg
}

func apiKeyAuth(cfg *Config, r *http.Request) error {
	if r.URL.Path == "/" || r.URL.Path == "/health" {
		return nil
	}
	if cfg.APIKey == "" {
		return nil
	}
	head := r.Header.Get("Authorization")
	var token string
	if strings.HasPrefix(head, "Bearer ") {
		token = strings.TrimPrefix(head, "Bearer ")
	} else if head != "" {
		token = head
	} else if keys := r.Header["X-API-Key"]; len(keys) > 0 {
		token = keys[0]
	}
	if token != cfg.APIKey {
		return errors.New("Invalid API key")
	}
	return nil
}

func selectModel(tokenCount int, cfg *Config) string {
	r := cfg.Router
	if tokenCount > 60000 && r.LongContext != "" {
		return r.LongContext
	}
	// additional routing omitted
	return r.Default
}

func stopService() error {
	if !isServiceRunning() {
		cleanupPid()
		return errors.New("no service running")
	}
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return err
	}
	pid, _ := strconv.Atoi(string(data))
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	cleanupPid()
	return nil
}

func showStatus() {
	running := isServiceRunning()
	pid := 0
	if running {
		if data, err := os.ReadFile(pidFilePath); err == nil {
			pid, _ = strconv.Atoi(string(data))
		}
	}
	fmt.Println("\n📊 Claude Code Router Status")
	fmt.Println(strings.Repeat("=", 40))
	if running {
		fmt.Println("✅ Running, PID:", pid)
	} else {
		fmt.Println("❌ Not Running")
	}
}

func isServiceRunning() bool {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(data))
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
	if err := os.Remove(pidFilePath); err != nil {
		logger.Warn("Remove PID failed", "error", err)
	}
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

func executeCode(args []string) {
	incrementRef()
	cmd := exec.Command("claude", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	decrementRef()
	if readRef() == 0 {
		stopService()
	}
	if err != nil {
		logger.Error("Code exec failed", "error", err)
		os.Exit(1)
	}
}

func incrementRef() {
	c := readRef()
	c++
	os.WriteFile(refFilePath, []byte(strconv.Itoa(c)), 0o644)
}

func decrementRef() {
	c := readRef()
	if c > 0 {
		c--
	}
	os.WriteFile(refFilePath, []byte(strconv.Itoa(c)), 0o644)
}

func readRef() int {
	if data, err := os.ReadFile(refFilePath); err == nil {
		if v, err := strconv.Atoi(string(data)); err == nil {
			return v
		}
	}
	return 0
}
