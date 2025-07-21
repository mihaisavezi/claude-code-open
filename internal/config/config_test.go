package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_LoadAndSave(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Create test configuration
	cfg := &Config{
		Host:   "127.0.0.1",
		Port:   8080,
		APIKey: "test-key",
		Providers: []Provider{
			{
				Name:    "openrouter",
				APIBase: "https://openrouter.ai/api/v1/chat/completions",
				APIKey:  "test-provider-key",
				Models:  []string{"anthropic/claude-3.5-sonnet"},
			},
		},
		Router: RouterConfig{
			Default:     "openrouter,anthropic/claude-3.5-sonnet",
			Think:       "openrouter,anthropic/claude-3.5-sonnet",
			LongContext: "openrouter,anthropic/claude-3.5-sonnet-20241022",
		},
	}

	// Save configuration
	if err := manager.Save(cfg); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Verify file exists
	if !manager.Exists() {
		t.Errorf("Config file should exist after saving")
	}

	// Load configuration
	loadedCfg, err := manager.Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify loaded configuration
	if loadedCfg.Host != cfg.Host {
		t.Errorf("Expected host %s, got %s", cfg.Host, loadedCfg.Host)
	}

	if loadedCfg.Port != cfg.Port {
		t.Errorf("Expected port %d, got %d", cfg.Port, loadedCfg.Port)
	}

	if loadedCfg.APIKey != cfg.APIKey {
		t.Errorf("Expected API key %s, got %s", cfg.APIKey, loadedCfg.APIKey)
	}

	if len(loadedCfg.Providers) != 1 {
		t.Errorf("Expected 1 provider, got %d", len(loadedCfg.Providers))
	}

	provider := loadedCfg.Providers[0]
	if provider.Name != "openrouter" {
		t.Errorf("Expected provider name 'openrouter', got %s", provider.Name)
	}

	if provider.APIBase != "https://openrouter.ai/api/v1/chat/completions" {
		t.Errorf("Expected specific API base, got %s", provider.APIBase)
	}

	if loadedCfg.Router.Default != "openrouter,anthropic/claude-3.5-sonnet" {
		t.Errorf("Expected specific default router, got %s", loadedCfg.Router.Default)
	}
}

func TestConfig_Defaults(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Create minimal configuration
	cfg := &Config{
		Providers: []Provider{
			{
				Name:    "test",
				APIBase: "http://example.com",
				APIKey:  "key",
				Models:  []string{"model"},
			},
		},
		Router: RouterConfig{
			Default: "test,model",
		},
	}

	// Save and load
	manager.Save(cfg)
	loadedCfg, err := manager.Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify defaults are applied
	if loadedCfg.Port != DefaultPort {
		t.Errorf("Expected default port %d, got %d", DefaultPort, loadedCfg.Port)
	}

	if loadedCfg.Host != DefaultHost {
		t.Errorf("Expected default host %s, got %s", DefaultHost, loadedCfg.Host)
	}
}

func TestConfig_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Create invalid JSON file
	configPath := filepath.Join(tmpDir, DefaultConfigFilename)
	os.WriteFile(configPath, []byte("invalid json"), 0644)

	// Try to load
	_, err := manager.Load()
	if err == nil {
		t.Errorf("Expected error when loading invalid JSON")
	}
}

func TestConfig_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Try to load non-existent file
	_, err := manager.Load()
	if err == nil {
		t.Errorf("Expected error when loading non-existent file")
	}

	// Check exists
	if manager.Exists() {
		t.Errorf("Non-existent config should not exist")
	}
}

func TestConfig_GetWithoutLoad(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Get without loading should return nil or attempt to load
	cfg := manager.Get()
	// Should not panic and either return nil or a default config
	if cfg != nil && cfg.Port != 0 && cfg.Port != DefaultPort {
		t.Errorf("Unexpected config returned: %+v", cfg)
	}
}