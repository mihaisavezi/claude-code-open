package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/Davincible/claude-code-router-go/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration",
	Long:  `Manage the LLM proxy router configuration.`,
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize configuration interactively",
	Long:  `Initialize configuration by prompting for provider details.`,
	RunE:  runConfigInit,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	Long:  `Display the current configuration.`,
	RunE:  runConfigShow,
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate configuration",
	Long:  `Validate the current configuration for errors.`,
	RunE:  runConfigValidate,
}

func init() {
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configValidateCmd)
}

func runConfigInit(cmd *cobra.Command, _ []string) error {
	color.Blue("Claude Code Router Configuration Setup")
	color.Yellow("Follow the prompts to configure your LLM providers.")
	
	reader := bufio.NewReader(os.Stdin)
	
	// Get provider details
	fmt.Print("\nProvider Name (e.g., openrouter, openai): ")
	providerName, _ := reader.ReadString('\n')
	providerName = strings.TrimSpace(providerName)
	
	fmt.Print("API Key: ")
	apiKey, _ := reader.ReadString('\n')
	apiKey = strings.TrimSpace(apiKey)
	
	fmt.Print("API Base URL: ")
	baseURL, _ := reader.ReadString('\n')
	baseURL = strings.TrimSpace(baseURL)
	
	fmt.Print("Default Model: ")
	model, _ := reader.ReadString('\n')
	model = strings.TrimSpace(model)
	
	// Optional router API key
	fmt.Print("Router API Key (optional, for authentication): ")
	routerAPIKey, _ := reader.ReadString('\n')
	routerAPIKey = strings.TrimSpace(routerAPIKey)
	
	// Create configuration
	cfg := &config.Config{
		Host:   config.DefaultHost,
		Port:   config.DefaultPort,
		APIKey: routerAPIKey,
		Providers: []config.Provider{
			{
				Name:    providerName,
				APIBase: baseURL,
				APIKey:  apiKey,
				Models:  []string{model},
			},
		},
		Router: config.RouterConfig{
			Default: fmt.Sprintf("%s,%s", providerName, model),
		},
	}
	
	// Save configuration
	if err := cfgMgr.Save(cfg); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	
	color.Green("Configuration saved successfully to: %s", cfgMgr.GetPath())
	color.Cyan("You can now start the router with: ccr start")
	
	return nil
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	if !cfgMgr.Exists() {
		color.Yellow("No configuration found. Run 'ccr config init' to create one.")
		return nil
	}
	
	cfg, err := cfgMgr.Load()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	
	color.Blue("Current Configuration:")
	fmt.Printf("  %-15s: %s\n", "Host", cfg.Host)
	fmt.Printf("  %-15s: %d\n", "Port", cfg.Port)
	fmt.Printf("  %-15s: %s\n", "API Key", maskString(cfg.APIKey))
	fmt.Printf("  %-15s: %s\n", "Config Path", cfgMgr.GetPath())
	
	fmt.Println("\nProviders:")
	for _, provider := range cfg.Providers {
		fmt.Printf("  - Name: %s\n", provider.Name)
		fmt.Printf("    API Base: %s\n", provider.APIBase)
		fmt.Printf("    API Key: %s\n", maskString(provider.APIKey))
		fmt.Printf("    Models: %v\n", provider.Models)
		fmt.Println()
	}
	
	fmt.Println("Router Configuration:")
	fmt.Printf("  %-15s: %s\n", "Default", cfg.Router.Default)
	if cfg.Router.Think != "" {
		fmt.Printf("  %-15s: %s\n", "Think", cfg.Router.Think)
	}
	if cfg.Router.Background != "" {
		fmt.Printf("  %-15s: %s\n", "Background", cfg.Router.Background)
	}
	if cfg.Router.LongContext != "" {
		fmt.Printf("  %-15s: %s\n", "Long Context", cfg.Router.LongContext)
	}
	if cfg.Router.WebSearch != "" {
		fmt.Printf("  %-15s: %s\n", "Web Search", cfg.Router.WebSearch)
	}
	
	return nil
}

func runConfigValidate(cmd *cobra.Command, _ []string) error {
	if !cfgMgr.Exists() {
		return fmt.Errorf("no configuration found")
	}
	
	cfg, err := cfgMgr.Load()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	
	// Validation logic
	var errors []string
	
	if len(cfg.Providers) == 0 {
		errors = append(errors, "no providers configured")
	}
	
	for i, provider := range cfg.Providers {
		if provider.Name == "" {
			errors = append(errors, fmt.Sprintf("provider %d: name is required", i))
		}
		if provider.APIBase == "" {
			errors = append(errors, fmt.Sprintf("provider %d: API base URL is required", i))
		}
		if provider.APIKey == "" {
			errors = append(errors, fmt.Sprintf("provider %d: API key is required", i))
		}
	}
	
	if cfg.Router.Default == "" {
		errors = append(errors, "default router model is required")
	}
	
	if len(errors) > 0 {
		color.Red("Configuration validation failed:")
		for _, err := range errors {
			fmt.Printf("  - %s\n", err)
		}
		return fmt.Errorf("configuration validation failed")
	}
	
	color.Green("Configuration is valid!")
	return nil
}

func maskString(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}