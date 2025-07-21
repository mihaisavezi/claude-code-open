package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

const (
	DefaultPort           = 6970
	DefaultConfigFilename = "config.json"
	DefaultHost           = "127.0.0.1"
)

type Provider struct {
	Name    string   `json:"name"`
	APIBase string   `json:"api_base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
}

type RouterConfig struct {
	Default     string `json:"default"`
	Think       string `json:"think,omitempty"`
	Background  string `json:"background,omitempty"`
	LongContext string `json:"longContext,omitempty"`
	WebSearch   string `json:"webSearch,omitempty"`
}

type Config struct {
	Host      string       `json:"HOST,omitempty"`
	Port      int          `json:"PORT,omitempty"`
	APIKey    string       `json:"APIKEY,omitempty"`
	Providers []Provider   `json:"Providers"`
	Router    RouterConfig `json:"Router"`
}

type Manager struct {
	configPath  string
	configValue atomic.Value
}

func NewManager(baseDir string) *Manager {
	return &Manager{
		configPath: filepath.Join(baseDir, DefaultConfigFilename),
	}
}

func (m *Manager) Load() (*Config, error) {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Set defaults
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}

	m.configValue.Store(&cfg)
	return &cfg, nil
}

func (m *Manager) Get() *Config {
	if v := m.configValue.Load(); v != nil {
		return v.(*Config)
	}

	cfg, err := m.Load()
	if err != nil {
		// Return a config with defaults if loading fails
		return &Config{
			Host: DefaultHost,
			Port: DefaultPort,
		}
	}
	return cfg
}

func (m *Manager) Save(cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(m.configPath, data, 0644); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	m.configValue.Store(cfg)
	return nil
}

func (m *Manager) GetPath() string {
	return m.configPath
}

func (m *Manager) Exists() bool {
	_, err := os.Stat(m.configPath)
	return err == nil
}