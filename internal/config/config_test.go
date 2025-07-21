package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	err := manager.Save(cfg)
	require.NoError(t, err, "should be able to save config")

	// Verify file exists
	assert.True(t, manager.Exists(), "config file should exist after saving")

	// Load configuration
	loadedCfg, err := manager.Load()
	require.NoError(t, err, "should be able to load config")

	// Verify loaded configuration
	assert.Equal(t, cfg.Host, loadedCfg.Host, "host should match")
	assert.Equal(t, cfg.Port, loadedCfg.Port, "port should match")
	assert.Equal(t, cfg.APIKey, loadedCfg.APIKey, "API key should match")
	
	require.Len(t, loadedCfg.Providers, 1, "should have 1 provider")
	
	provider := loadedCfg.Providers[0]
	assert.Equal(t, "openrouter", provider.Name, "provider name should match")
	assert.Equal(t, "https://openrouter.ai/api/v1/chat/completions", provider.APIBase, "API base should match")
	assert.Equal(t, "openrouter,anthropic/claude-3.5-sonnet", loadedCfg.Router.Default, "default router should match")
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
	err := manager.Save(cfg)
	require.NoError(t, err)
	
	loadedCfg, err := manager.Load()
	require.NoError(t, err, "should be able to load config")

	// Verify defaults are applied
	assert.Equal(t, DefaultPort, loadedCfg.Port, "should apply default port")
	assert.Equal(t, DefaultHost, loadedCfg.Host, "should apply default host")
}

func TestConfig_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Create invalid JSON file
	configPath := filepath.Join(tmpDir, DefaultConfigFilename)
	os.WriteFile(configPath, []byte("invalid json"), 0644)

	// Try to load
	_, err := manager.Load()
	assert.Error(t, err, "should get error when loading invalid JSON")
}

func TestConfig_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Try to load non-existent file
	_, err := manager.Load()
	assert.Error(t, err, "should get error when loading non-existent file")

	// Check exists
	assert.False(t, manager.Exists(), "non-existent config should not exist")
}

func TestConfig_GetWithoutLoad(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir)

	// Get without loading should return defaults
	cfg := manager.Get()
	assert.NotNil(t, cfg, "should not return nil config")
	assert.Equal(t, DefaultPort, cfg.Port, "should return default port")
	assert.Equal(t, DefaultHost, cfg.Host, "should return default host")
}