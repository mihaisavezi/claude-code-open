package cmd

import (
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/Davincible/claude-code-router-go/internal/process"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the router service",
	Long:  `Stop the running LLM proxy router service.`,
	RunE:  runStop,
}

func runStop(cmd *cobra.Command, _ []string) error {
	color.Yellow("Stopping %s...", AppName)

	procMgr := process.NewManager(baseDir)

	if !procMgr.IsRunning() {
		color.Yellow("Service is not running")
		return nil
	}

	if err := procMgr.Stop(); err != nil {
		return err
	}

	// Clean up reference count file
	procMgr.CleanupRef()

	color.Green("Service stopped successfully")
	return nil
}