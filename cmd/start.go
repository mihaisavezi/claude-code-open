package cmd

import (
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/mihaisavezi/claude-code-open/internal/process"
	"github.com/mihaisavezi/claude-code-open/internal/server"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the router service",
	Long:  `Start the LLM proxy router service in the foreground.`,
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, _ []string) error {
	// Setup logging
	verbose, err := cmd.Flags().GetBool("verbose")
	if err != nil {
		return err
	}

	logFile, err := cmd.Flags().GetBool("log-file")
	if err != nil {
		return err
	}

	setupLogging(verbose, logFile)

	// Ensure configuration exists
	if configErr := ensureConfigExists(); configErr != nil {
		return configErr
	}

	// Load configuration
	cfg, err := cfgMgr.Load()
	if err != nil {
		return err
	}

	color.Green("Starting %s v%s...", AppName, Version)
	logger.Info("Starting server",
		"host", cfg.Host,
		"port", cfg.Port,
		"providers", len(cfg.Providers),
	)

	// Setup process management
	procMgr := process.NewManager(baseDir)
	if err := procMgr.WritePID(); err != nil {
		return err
	}
	defer procMgr.CleanupPID()

	// Create and start server
	srv := server.New(cfgMgr, logger)

	return srv.Start()
}
