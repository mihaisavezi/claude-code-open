package cmd

import (
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/Davincible/claude-code-open/internal/process"
	"github.com/Davincible/claude-code-open/internal/server"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the router service",
	Long:  `Start the LLM proxy router service in the foreground.`,
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, _ []string) error {
	// Setup logging
	verbose, _ := cmd.Flags().GetBool("verbose")
	logFile, _ := cmd.Flags().GetBool("log-file")
	setupLogging(verbose, logFile)

	// Ensure configuration exists
	if err := ensureConfigExists(); err != nil {
		return err
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
