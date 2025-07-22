package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/Davincible/claude-code-router-go/internal/process"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show router service status",
	Long:  `Display the current status of the LLM proxy router service.`,
	Run:   runStatus,
}

func runStatus(cmd *cobra.Command, _ []string) {
	procMgr := process.NewManager(baseDir)
	cfg := cfgMgr.Get()

	running := procMgr.IsRunning()
	pid := procMgr.ReadPID()
	refs := procMgr.ReadRef()

	color.Blue("Status for %s:", AppName)
	fmt.Printf("  %-15s: %v\n", "Running", running)
	fmt.Printf("  %-15s: %d\n", "PID", pid)

	if cfg != nil {
		fmt.Printf("  %-15s: %s\n", "Host", cfg.Host)
		fmt.Printf("  %-15s: %d\n", "Port", cfg.Port)
		fmt.Printf("  %-15s: %s\n", "Endpoint", fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port))
		fmt.Printf("  %-15s: %d\n", "Providers", len(cfg.Providers))
	}

	fmt.Printf("  %-15s: %s\n", "Config Path", cfgMgr.GetPath())
	fmt.Printf("  %-15s: %d\n", "References", refs)
	fmt.Printf("  %-15s: v%s\n", "Version", Version)
}
