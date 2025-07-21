package providers

import (
	"testing"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	registry := NewRegistry()
	provider := NewOpenRouterProvider()

	// Register provider
	registry.Register(provider)

	// Get provider
	retrievedProvider, exists := registry.Get("openrouter")
	if !exists {
		t.Errorf("Provider should exist after registration")
	}

	if retrievedProvider.Name() != "openrouter" {
		t.Errorf("Expected provider name 'openrouter', got %s", retrievedProvider.Name())
	}
}

func TestRegistry_GetByDomain(t *testing.T) {
	registry := NewRegistry()
	registry.Initialize()

	testCases := []struct {
		domain   string
		expected string
	}{
		{"https://openrouter.ai/api/v1/chat/completions", "openrouter"},
		{"https://api.openrouter.ai/api/v1/chat/completions", "openrouter"},
		{"https://api.openai.com/v1/chat/completions", "openai"},
		{"https://api.anthropic.com/v1/messages", "anthropic"},
	}

	for _, tc := range testCases {
		provider, err := registry.GetByDomain(tc.domain)
		if err != nil {
			t.Errorf("Failed to get provider for domain %s: %v", tc.domain, err)
			continue
		}

		if provider.Name() != tc.expected {
			t.Errorf("Expected provider %s for domain %s, got %s", tc.expected, tc.domain, provider.Name())
		}
	}
}

func TestRegistry_GetByDomain_InvalidURL(t *testing.T) {
	registry := NewRegistry()
	registry.Initialize()

	_, err := registry.GetByDomain("invalid-url")
	if err == nil {
		t.Errorf("Expected error for invalid URL")
	}
}

func TestRegistry_GetByDomain_UnknownDomain(t *testing.T) {
	registry := NewRegistry()
	registry.Initialize()

	_, err := registry.GetByDomain("https://unknown-provider.com/api")
	if err == nil {
		t.Errorf("Expected error for unknown domain")
	}
}

func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()
	registry.Initialize()

	providers := registry.List()
	
	expectedProviders := []string{"openrouter", "openai", "anthropic"}
	if len(providers) != len(expectedProviders) {
		t.Errorf("Expected %d providers, got %d", len(expectedProviders), len(providers))
	}

	// Check that all expected providers are present
	providerMap := make(map[string]bool)
	for _, provider := range providers {
		providerMap[provider] = true
	}

	for _, expected := range expectedProviders {
		if !providerMap[expected] {
			t.Errorf("Expected provider %s not found in list", expected)
		}
	}
}

func TestRegistry_GetNonExistent(t *testing.T) {
	registry := NewRegistry()

	_, exists := registry.Get("nonexistent")
	if exists {
		t.Errorf("Non-existent provider should not exist")
	}
}