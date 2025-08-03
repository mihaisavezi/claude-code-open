package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/mihaisavezi/claude-code-open/internal/config"
)

const (
	AppName    = "claude-code-open"
	OldAppName = "claude-code-router" // For backward compatibility
	Version    = "0.3.0"
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

	// Setup directories with backward compatibility
	var err error

	homeDir, err = os.UserHomeDir()
	if err != nil {
		logger.Error("Failed to get home directory", "error", err)
		os.Exit(1)
	}

	baseDir = getConfigDirectory(homeDir)
	cfgMgr = config.NewManager(baseDir)
}

var rootCmd = &cobra.Command{
	Use:     "cco",
	Short:   "Claude Code Open - LLM Proxy Server",
	Long:    `A production-ready LLM proxy server that converts requests from various providers to Anthropic's Claude API format.`,
	Version: Version,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		logger.Error("Command execution failed", "error", err)
		os.Exit(1)
	}
}

// getConfigDirectory determines which config directory to use with backward compatibility
func getConfigDirectory(homeDir string) string {
	newDir := filepath.Join(homeDir, "."+AppName)
	oldDir := filepath.Join(homeDir, "."+OldAppName)

	// Check if old directory exists and has configuration files
	oldExists := directoryHasConfig(oldDir)
	newExists := directoryHasConfig(newDir)

	if newExists {
		// New directory exists and has config - use it
		return newDir
	}

	if oldExists {
		// Old directory exists with config - use it with a migration notice
		color.Yellow("Using existing configuration directory: %s", oldDir)
		color.Cyan("Consider migrating to the new directory: %s", newDir)
		color.Cyan("You can do this by running: mv %s %s", oldDir, newDir)

		return oldDir
	}

	// Neither exists - use new directory
	return newDir
}

// directoryHasConfig checks if a directory exists and contains configuration files
func directoryHasConfig(dir string) bool {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return false
	}

	// Check for common config files
	yamlConfig := filepath.Join(dir, "config.yaml")
	jsonConfig := filepath.Join(dir, "config.json")

	if _, err := os.Stat(yamlConfig); err == nil {
		return true
	}

	if _, err := os.Stat(jsonConfig); err == nil {
		return true
	}

	return false
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
		// Check if CCO_API_KEY is set - if so, allow running without config file
		if ccoAPIKey := os.Getenv("CCO_API_KEY"); ccoAPIKey != "" {
			color.Green("No configuration file found, but CCO_API_KEY is set - using minimal configuration")
			return nil
		}

		color.Yellow("Configuration not found, starting setup...")

		return promptForConfig()
	}

	return nil
}

func promptForConfig() error {
	// This will be implemented in the config command
	fmt.Println("Please run 'cco config init' to set up your configuration")
	return errors.New("configuration required")
}
