package cmd

import (
	"os"
	"os/exec"
	"strconv"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/Davincible/claude-code-router-go/internal/process"
)

var codeCmd = &cobra.Command{
	Use:   "code [args...]",
	Short: "Execute Claude Code via the router service",
	Long:  `Start the router service if needed and execute Claude Code with the router as the backend.`,
	Args:  cobra.ArbitraryArgs,
	RunE:  runCode,
}

func runCode(cmd *cobra.Command, args []string) error {
	procMgr := process.NewManager(baseDir)
	cfg := cfgMgr.Get()

	// Ensure service is running and track if we started it
	serviceStartedByUs, err := procMgr.StartServiceIfNeeded()
	if err != nil {
		return err
	}

	// Set up environment variables for Claude Code
	env := os.Environ()
	
	// Remove any existing Anthropic auth tokens
	env = filterEnv(env, "ANTHROPIC_AUTH_TOKEN")
	env = filterEnv(env, "ANTHROPIC_API_KEY")
	
	// Set router as the API endpoint
	if cfg.APIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+cfg.APIKey)
	} else {
		env = append(env, "ANTHROPIC_AUTH_TOKEN=proxy")
	}
	
	env = append(env, "ANTHROPIC_BASE_URL=http://"+cfg.Host+":"+strconv.Itoa(cfg.Port))
	env = append(env, "API_TIMEOUT_MS=600000")

	// Track reference count
	procMgr.IncrementRef()
	defer func() {
		procMgr.DecrementRef()
		// Only stop service if we started it and no more references
		if serviceStartedByUs && procMgr.ReadRef() == 0 {
			color.Yellow("No more active sessions, stopping auto-started service...")
			procMgr.Stop()
		}
	}()

	// Execute Claude Code
	claudeCmd := exec.Command("claude", args...)
	claudeCmd.Env = env
	claudeCmd.Stdin = os.Stdin
	claudeCmd.Stdout = os.Stdout
	claudeCmd.Stderr = os.Stderr

	return claudeCmd.Run()
}

func filterEnv(env []string, key string) []string {
	var filtered []string
	prefix := key + "="
	for _, e := range env {
		if !startsWith(e, prefix) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}