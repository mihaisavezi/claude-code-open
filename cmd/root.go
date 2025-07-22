package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/Davincible/claude-code-router-go/internal/config"
)

const (
	AppName = "claude-code-router"
	Version = "0.2.0"
)

var (
	logger  *slog.Logger
	homeDir string
	baseDir string
	cfgMgr  *config.Manager
)

func init() {
	// Initialize logger
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger = slog.New(handler)

	// Setup directories
	var err error
	homeDir, err = os.UserHomeDir()
	if err != nil {
		logger.Error("Failed to get home directory", "error", err)
		os.Exit(1)
	}

	baseDir = filepath.Join(homeDir, "."+AppName)
	cfgMgr = config.NewManager(baseDir)
}

var rootCmd = &cobra.Command{
	Use:     "ccr",
	Short:   "Claude Code Router - LLM Proxy Server",
	Long:    `A production-ready LLM proxy server that converts requests from various providers to Anthropic's Claude API format.`,
	Version: Version,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		logger.Error("Command execution failed", "error", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "enable verbose logging")
	rootCmd.PersistentFlags().BoolP("log-file", "l", false, "enable file logging")

	// Add subcommands
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(codeCmd)
	rootCmd.AddCommand(configCmd)
}

func setupLogging(verbose, logFile bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: level}

	if logFile {
		// TODO: Implement file logging
		color.Yellow("File logging not yet implemented, using stdout")
	}

	handler := slog.NewTextHandler(os.Stdout, opts)
	logger = slog.New(handler)
}

func ensureConfigExists() error {
	if !cfgMgr.Exists() {
		color.Yellow("Configuration not found, starting setup...")
		return promptForConfig()
	}
	return nil
}

func promptForConfig() error {
	// This will be implemented in the config command
	fmt.Println("Please run 'ccr config init' to set up your configuration")
	return fmt.Errorf("configuration required")
}
