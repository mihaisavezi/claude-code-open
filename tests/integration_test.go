package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Davincible/claude-code-router-go/internal/config"
	"github.com/Davincible/claude-code-router-go/internal/handlers"
	"github.com/Davincible/claude-code-router-go/internal/providers"
	"log/slog"
)

func TestProxyIntegration(t *testing.T) {
	// Create test configuration
	cfg := &config.Config{
		Host:   "127.0.0.1",
		Port:   8080,
		APIKey: "test-key",
		Providers: []config.Provider{
			{
				Name:    "test",
				APIBase: "http://example.com/api/v1/chat/completions",
				APIKey:  "test-provider-key",
				Models:  []string{"test-model"},
			},
		},
		Router: config.RouterConfig{
			Default: "test,test-model",
		},
	}

	// Create temporary config manager
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)
	cfgMgr.Save(cfg)

	// Setup logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Setup registry with mock provider
	registry := providers.NewRegistry()
	registry.Register(&mockProvider{})

	// Create proxy handler
	handler := handlers.NewProxyHandler(cfgMgr, registry, logger)

	// Create test request
	requestBody := map[string]interface{}{
		"model": "test-model",
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": "Hello, world!",
			},
		},
	}

	jsonBody, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	// Record response
	rr := httptest.NewRecorder()

	// Execute request
	handler.ServeHTTP(rr, req)

	// Verify response
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	// Parse response body
	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Verify Anthropic format
	if response["type"] != "message" {
		t.Errorf("Expected type 'message', got %v", response["type"])
	}

	if response["role"] != "assistant" {
		t.Errorf("Expected role 'assistant', got %v", response["role"])
	}

	if content, ok := response["content"].([]interface{}); !ok || len(content) == 0 {
		t.Errorf("Expected content array, got %v", response["content"])
	}
}

// mockProvider implements a test provider
type mockProvider struct{}

func (m *mockProvider) Name() string { return "test" }
func (m *mockProvider) SupportsStreaming() bool { return false }
func (m *mockProvider) GetEndpoint() string { return "http://example.com" }
func (m *mockProvider) SetAPIKey(key string) {}

func (m *mockProvider) IsStreaming(headers map[string][]string) bool {
	return false
}

func (m *mockProvider) Transform(request []byte) ([]byte, error) {
	// Return mock Anthropic response
	response := map[string]interface{}{
		"id":   "msg_test123",
		"type": "message",
		"role": "assistant",
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": "Hello! This is a test response.",
			},
		},
		"model":       "test-model",
		"stop_reason": "end_turn",
		"usage": map[string]interface{}{
			"input_tokens":  10,
			"output_tokens": 8,
		},
	}

	return json.Marshal(response)
}

func (m *mockProvider) TransformStream(chunk []byte, state *providers.StreamState) ([]byte, error) {
	return chunk, nil
}