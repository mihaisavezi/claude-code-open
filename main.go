package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

	"github.com/fatih/color"
	"github.com/fsnotify/fsnotify"
	"github.com/pkoukk/tiktoken-go"
	"github.com/spf13/cobra"
)

const (
	AppName               = "claude-code-router"
	Version               = "0.1.0"
	pidFilename           = ".claude-code-router.pid"
	refCountFilename      = "claude-code-reference-count.txt"
	DefaultPort           = 6969
	DefaultConfigFilename = "config.json"
)

var (
	logger      *slog.Logger
	homeDir, _  = os.UserHomeDir()
	baseDir     = filepath.Join(homeDir, "."+AppName)
	configPath  = filepath.Join(baseDir, DefaultConfigFilename)
	pidFilePath = filepath.Join(baseDir, pidFilename)
	refFilePath = filepath.Join(os.TempDir(), refCountFilename)
	configValue atomic.Value // stores Config
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

func init() {
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger = slog.New(h)
}

func main() {
	root := &cobra.Command{Use: "ccr", Short: "Claude Code Router CLI"}
	root.AddCommand(startCmd, stopCmd, statusCmd, codeCmd, versionCmd)
	if err := root.Execute(); err != nil {
		logger.Error("CLI execution failed", "error", err)
		os.Exit(1)
	}
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the router service",
	RunE:  runStart,
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the router service",
	RunE:  runStop,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show router service status",
	Run:   runStatus,
}

var codeCmd = &cobra.Command{
	Use:   "code [args...]",
	Short: "Execute code via the router service",
	Args:  cobra.ArbitraryArgs,
	RunE:  runCode,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run: func(cmd *cobra.Command, args []string) {
		color.Cyan("%s version %s", AppName, Version)
	},
}

func runStart(cmd *cobra.Command, _ []string) error {
	color.Green("Starting %s...", AppName)
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	configValue.Store(cfg)
	go watchConfigFile()
	return runServiceLoop()
}

func runStop(cmd *cobra.Command, _ []string) error {
	color.Yellow("Stopping %s...", AppName)
	if err := stopService(); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	color.Green("Service stopped successfully")
	return nil
}

func runStatus(cmd *cobra.Command, _ []string) {
	running, pid := isServiceRunning(), 0
	if running {
		pid = readPid()
	}
	color.Blue("Status for %s:\n", AppName)
	fmt.Printf("  %-10s: %t\n", "Running", running)
	fmt.Printf("  %-10s: %d\n", "PID", pid)
}

func runCode(cmd *cobra.Command, args []string) error {
	cfg := getConfig()
	if !isServiceRunning() {
		color.Yellow("Service not running, starting...")
		if err := exec.Command(os.Args[0], "start").Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
		if ok := waitForService(10*time.Second, 500*time.Millisecond); !ok {
			return errors.New("service startup timeout")
		}
	}
	return executeCode(cfg, args)
}

func runServiceLoop() error {
	cfg := getConfig()
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{Addr: addr, Handler: setupMux()}
	if err := os.MkdirAll(baseDir, 0o755); err == nil {
		os.WriteFile(pidFilePath, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	color.Green("Listening on %s", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen error", "error", err)
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	color.Yellow("Shutdown signal received, exiting...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	cleanupPid()
	color.Green("Service exited cleanly")
	return nil
}

func setupMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/", proxyHandler)
	return mux
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
	logger.Debug("health check", "remote", r.RemoteAddr)
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIResponse struct {
	Usage *openAIUsage `json:"usage"`
	// other fields ignored
}

func countInputTokensCl100k(text string) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		log.Printf("failed to get encoding: %v", err)
		return 0
	}
	return len(tke.Encode(text, nil, nil))
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()

	if err := authenticate(cfg, r); err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		logger.Error("Unauthorized request",
			"remote_addr", r.RemoteAddr,
			"error", err,
		)
		return
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, http.StatusBadRequest, "read body: %v", err)
		logger.Error("Failed to read request body", "error", err)
		return
	}
	bodyStr := string(body)
	inputTokens := countInputTokensCl100k(bodyStr)

	// Prepare upstream request
	url := cfg.Providers[0].APIBase
	req, err := http.NewRequest(r.Method, url, strings.NewReader(bodyStr))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "create request: %v", err)
		logger.Error("Failed to create upstream request", "error", err)
		return
	}
	req.Header = r.Header.Clone()
	if key := cfg.Providers[0].APIKey; key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	logger.Info("Sending LLM proxy request",
		"url", url,
		"method", r.Method,
		"input_tokens", inputTokens,
		"remote_addr", r.RemoteAddr,
		"content_length", len(bodyStr),
	)

	// Perform upstream call
	rs, err := http.DefaultClient.Do(req)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream error: %v", err)
		logger.Error("Upstream request failed", "error", err)
		return
	}
	defer rs.Body.Close()

	// Read and buffer upstream response
	respBody, err := io.ReadAll(rs.Body)
	if err != nil {
		httpError(w, http.StatusBadGateway, "read upstream response: %v", err)
		logger.Error("Failed to read upstream response body", "error", err)
		return
	}

	// Attempt to parse OpenAI-style token usage
	var usage *openAIUsage
	var parsedResp openAIResponse
	if err := json.Unmarshal(respBody, &parsedResp); err == nil && parsedResp.Usage != nil {
		usage = parsedResp.Usage
	}

	w.WriteHeader(rs.StatusCode)
	w.Write(respBody)

	logFields := []any{
		"url", url,
		"method", r.Method,
		"status", rs.StatusCode,
		"input_tokens", inputTokens,
		"remote_addr", r.RemoteAddr,
		"response_length", len(respBody),
	}

	if usage != nil {
		logFields = append(logFields,
			"output_tokens", usage.CompletionTokens,
			"prompt_tokens_reported", usage.PromptTokens,
			"total_tokens", usage.TotalTokens,
		)
	}

	if rs.StatusCode != http.StatusOK {
		logFields = append(logFields, "response_body", string(respBody))
		logger.Error("LLM proxy returned non-200", logFields...)
	} else {
		logger.Info("LLM proxy completed successfully", logFields...)
	}
}

func httpError(w http.ResponseWriter, code int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	w.WriteHeader(code)
	w.Write([]byte(msg))
	logger.Error(msg)
}

func loadConfig() (Config, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	logger.Debug("config loaded", "host", cfg.Host, "port", cfg.Port)
	return cfg, nil
}

func getConfig() Config {
	if v := configValue.Load(); v != nil {
		return v.(Config)
	}
	cfg, _ := loadConfig()
	return cfg
}

func authenticate(cfg Config, r *http.Request) error {
	if r.URL.Path == "/" || r.URL.Path == "/health" || cfg.APIKey == "" {
		return nil
	}
	// head := r.Header.Get("Authorization")
	// var token string
	// if strings.HasPrefix(head, "Bearer ") {
	// 	token = strings.TrimPrefix(head, "Bearer ")
	// } else if h := r.Header.Get("X-API-Key"); h != "" {
	// 	token = h
	// }
	// if token != cfg.APIKey {
	// 	return errors.New("invalid API key")
	// }
	return nil
}

func selectModel(tokenCount int, r *RouterConfig) string {
	if tokenCount > 60000 && r.LongContext != "" {
		return r.LongContext
	}
	return r.Default
}

func stopService() error {
	if !isServiceRunning() {
		return errors.New("service not running")
	}
	if err := syscall.Kill(readPid(), syscall.SIGTERM); err != nil {
		return err
	}
	cleanupPid()
	return nil
}

func isServiceRunning() bool {
	pid := readPid()
	if pid == 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		cleanupPid()
		return false
	}
	return true
}

func readPid() int {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func cleanupPid() {
	if err := os.Remove(pidFilePath); err != nil {
		logger.Warn("remove pid file", "error", err)
	}
}

func waitForService(timeout, delay time.Duration) bool {
	expire := time.Now().Add(timeout)
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for time.Now().Before(expire) {
		if isServiceRunning() {
			return true
		}
		<-ticker.C
	}
	return false
}

func executeCode(cfg Config, args []string) error {
	incrementRef()
	cmd := exec.Command("claude", args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	decrementRef()
	if readRef() == 0 {
		stopService()
	}
	if err != nil {
		return fmt.Errorf("execute code: %w", err)
	}
	return nil
}

func incrementRef() { writeRef(readRef() + 1) }
func decrementRef() {
	if c := readRef(); c > 0 {
		writeRef(c - 1)
	}
}
func readRef() int {
	if data, err := os.ReadFile(refFilePath); err == nil {
		if c, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			return c
		}
	}
	return 0
}
func writeRef(count int) {
	os.WriteFile(refFilePath, []byte(strconv.Itoa(count)), 0o644)
}

func watchConfigFile() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error("init config watcher", "error", err)
		return
	}
	defer watcher.Close()
	if err := watcher.Add(configPath); err != nil {
		logger.Error("add config watcher", "error", err)
		return
	}
	for {
		select {
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				color.Yellow("config change detected, reloading...")
				if cfg, err := loadConfig(); err == nil {
					configValue.Store(cfg)
					color.Green("config reloaded")
				} else {
					logger.Error("reload config", "error", err)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Error("watcher error", "error", err)
		}
	}
}
