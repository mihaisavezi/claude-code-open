package main

import (
	"bufio"
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
	DefaultPort           = 6970
	DefaultConfigFilename = "config.json"
)

var (
	logger      *slog.Logger
	homeDir, _  = os.UserHomeDir()
	baseDir     = filepath.Join(homeDir, "."+AppName)
	configPath  = filepath.Join(baseDir, DefaultConfigFilename)
	pidFilePath = filepath.Join(baseDir, pidFilename)
	refFilePath = filepath.Join(os.TempDir(), refCountFilename)
	configValue atomic.Value
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
	root.PersistentFlags().BoolP("log", "l", false, "enable file logging")
	root.AddCommand(startCmd, stopCmd, statusCmd, codeCmd, versionCmd)
	if err := root.Execute(); err != nil {
		logger.Error("CLI execution failed", "error", err)
		os.Exit(1)
	}
}

// -- Commands Definition ---------------------------------------------------

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

// -- Command Runners --------------------------------------------------------

func runStart(cmd *cobra.Command, _ []string) error {
	// Enable file logging if requested
	if logFlag, _ := cmd.Flags().GetBool("log"); logFlag {
		enableFileLogging()
	}
	// Bootstrap config if missing
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := promptForConfig(); err != nil {
			return err
		}
	}

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

	// clean ref file
	os.Remove(refFilePath)

	color.Green("Service stopped successfully")

	return nil
}

func runStatus(cmd *cobra.Command, _ []string) {
	running, pid := isServiceRunning(), readPid()
	cfg := getConfig()
	color.Blue("Status for %s:", AppName)
	fmt.Printf("  %-15s: %v\n", "Running", running)
	fmt.Printf("  %-15s: %d\n", "PID", pid)
	fmt.Printf("  %-15s: %s\n", "Host", cfg.Host)
	fmt.Printf("  %-15s: %d\n", "Port", cfg.Port)
	fmt.Printf("  %-15s: %s\n", "Endpoint", fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port))
	fmt.Printf("  %-15s: %s\n", "PID File", pidFilePath)
	fmt.Printf("  %-15s: %d\n", "References", readRef())
}

func runCode(cmd *cobra.Command, args []string) error {
	cfg := getConfig()

	// Ensure service is running
	if !isServiceRunning() {
		color.Yellow("Service not running, starting...")
		if err := exec.Command(os.Args[0], "start").Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
		if ok := waitForService(10*time.Second, 500*time.Millisecond); !ok {
			return errors.New("service startup timeout")
		}
	}

	// Set up environment variables
	env := os.Environ()
	if cfg.APIKey != "" {
		env = filterEnv(env, "ANTHROPIC_AUTH_TOKEN")
		env = append(env, "ANTHROPIC_API_KEY="+cfg.APIKey)
	} else {
		env = append(env, "ANTHROPIC_AUTH_TOKEN=test")
	}
	env = append(env, "ANTHROPIC_BASE_URL=http://"+cfg.Host+":"+strconv.Itoa(cfg.Port))
	env = append(env, "API_TIMEOUT_MS=600000")

	// Spawn Claude TUI process with inherited I/O and custom environment
	incrementRef()
	claudeCmd := exec.Command("claude", args...)
	claudeCmd.Env = env
	claudeCmd.Stdin = os.Stdin
	claudeCmd.Stdout = os.Stdout
	claudeCmd.Stderr = os.Stderr

	err := claudeCmd.Run()

	// Always decrement ref and possibly stop service afterwards
	decrementRef()
	if readRef() == 0 {
		stopService()
	}

	// Propagate exit code or error
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("claude exited with code %d", exitErr.ExitCode())
		}
		return fmt.Errorf("execute claude command: %w", err)
	}

	fmt.Println("QUITTINGGGG")

	return nil
}

// -- Service ----------------------------------------------------------------

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

// -- HTTP Handlers ----------------------------------------------------------

func setupMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	// mux.HandleFunc("/", proxyHandler)
	mux.HandleFunc("/", streamingProxyHandler)
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
}

// Updated main proxy handler with proper streaming support
func streamingProxyHandler(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()

	if err := authenticate(cfg, r); err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		logger.Error("Unauthorized request", "remote_addr", r.RemoteAddr, "error", err)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, http.StatusBadRequest, "read body: %v", err)
		logger.Error("Failed to read request body", "error", err)
		return
	}

	input := string(body)
	inputTokens := countInputTokensCl100k(input)
	input, model := selectModel(body, inputTokens, &cfg.Router)

	providerName := strings.Split(model, ",")

	// Find the provider based on the model name
	var provider Provider
	for _, p := range cfg.Providers {
		if p.Name == providerName[0] {
			provider = p
			break
		}
	}

	if provider.Name == "" {
		slog.Error("Provider not set. You need to set <provider>,<model> in the config, can't forward request", "model", model)
		return
	}

	// Clean cache control from request if needed
	if isOpenRouter(provider.APIBase) {
		_input, err := removeCacheControl([]byte(input))
		if err != nil {
			fmt.Println("Failed to remove cache control from OpenRouter request", err)
		} else {
			input = string(_input)
		}
	}

	// Create upstream request
	req, err := http.NewRequest(r.Method, provider.APIBase, strings.NewReader(input))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "create request: %v", err)
		logger.Error("Failed to create upstream request", "error", err)
		return
	}
	req.Header = r.Header.Clone()
	if provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	logger.Info("Proxy request", "url", provider.APIBase, "model", model, "input_tokens", inputTokens)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream error: %v", err)
		logger.Error("Upstream request failed", "error", err)
		return
	}
	defer resp.Body.Close()

	// Check if this is a streaming response
	isStreaming := isStreamingResponse(resp)

	if isOpenRouter(provider.APIBase) && isStreaming {
		// Handle OpenRouter streaming with transformation
		handleStreamingOpenRouter(w, resp, inputTokens)
	} else if isOpenRouter(provider.APIBase) {
		// Handle non-streaming OpenRouter with transformation
		handleNonStreamingOpenRouter(w, resp, inputTokens)
	} else {
		// Direct proxy for non-OpenRouter providers
		handleDirectStream(w, resp, inputTokens)
	}
}

// -- Utils ------------------------------------------------------------------

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

	// defaults
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}

	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}

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

	var token string

	head := r.Header.Get("Authorization")
	if strings.HasPrefix(head, "Bearer ") {
		token = strings.TrimPrefix(head, "Bearer ")
	} else if key := r.Header.Get("X-API-Key"); key != "" {
		token = key
	}

	if token != cfg.APIKey {
		return errors.New("invalid API key")
	}

	return nil
}

func selectModel(inputBody []byte, tokens int, r *RouterConfig) (string, string) {
	var modelBody map[string]any

	if err := json.Unmarshal(inputBody, &modelBody); err != nil {
		fmt.Println("Failed to unmarshal model from input body", err)
	}

	setModel := func() string {
		model, ok := modelBody["model"].(string)

		// longContext
		if tokens > 60000 && r.LongContext != "" {
			return r.LongContext
		}

		// background
		if ok && strings.HasPrefix(model, "claude-3-5-haiku") && r.Background != "" {
			return r.Background
		}

		// think
		if r.Think != "" {
			return r.Think
		}

		// webSearch
		if r.WebSearch != "" {
			return r.WebSearch
		}

		if len(model) > 0 {
			return model
		}

		return r.Default
	}

	model := setModel()
	if m := strings.Split(model, ","); len(m) > 1 {
		modelBody["model"] = m[1]
	} else {
		modelBody["model"] = model
	}

	_inputBody, err := json.Marshal(modelBody)
	if err != nil {
		fmt.Println("Failed to marshal body after model update", err)
		return string(inputBody), model
	}

	return string(_inputBody), model
}

func stopService() error {
	if !isServiceRunning() {
		return nil
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
	os.Remove(pidFilePath)
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

func countInputTokensCl100k(text string) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		log.Printf("failed to get encoding: %v", err)
		return 0
	}
	return len(tke.Encode(text, nil, nil))
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

// -- Helpers --------------------------------------------------------------

func promptForConfig() error {
	r := bufio.NewReader(os.Stdin)
	fmt.Print("Provider Name: ")
	name, _ := r.ReadString('\n')
	fmt.Print("API Key: ")
	apiKey, _ := r.ReadString('\n')
	fmt.Print("API Base URL: ")
	baseURL, _ := r.ReadString('\n')
	fmt.Print("Default Model: ")
	model, _ := r.ReadString('\n')

	cfg := Config{
		Host:   "127.0.0.1",
		Port:   DefaultPort,
		APIKey: strings.TrimSpace(apiKey),
		Providers: []Provider{{
			Name:    strings.TrimSpace(name),
			APIBase: strings.TrimSpace(baseURL),
			APIKey:  strings.TrimSpace(apiKey),
			Models:  []string{strings.TrimSpace(model)},
		}},
		Router: RouterConfig{Default: strings.TrimSpace(model)},
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(configPath, b, 0o644)
}

func enableFileLogging() {
	// Placeholder: integrate file logging setup
	// e.g., set slog handler to write to file
}

func filterEnv(env []string, key string) []string {
	var filtered []string
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// removeCacheControl removes all "cache_control" fields from a JSON byte slice
func removeCacheControl(jsonData []byte) ([]byte, error) {
	var data interface{}

	// Unmarshal JSON into generic interface
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	// Recursively remove cache_control fields
	cleaned := removeCacheControlRecursive(data)

	// Marshal back to JSON
	result, err := json.Marshal(cleaned)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return result, nil
}

// removeCacheControlRecursive recursively walks through the data structure
// and removes any "cache_control" keys from maps
func removeCacheControlRecursive(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		// Create a new map without cache_control
		result := make(map[string]interface{})
		for key, value := range v {
			if key != "cache_control" {
				result[key] = removeCacheControlRecursive(value)
			}
		}
		return result
	case []interface{}:
		// Process each element in the slice
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = removeCacheControlRecursive(item)
		}
		return result
	default:
		// Return primitive values as-is
		return v
	}
}
