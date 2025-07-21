package providers

import (
	"strings"
)

type AnthropicProvider struct {
	name     string
	endpoint string
	apiKey   string
}

func NewAnthropicProvider() *AnthropicProvider {
	return &AnthropicProvider{
		name: "anthropic",
	}
}

func (p *AnthropicProvider) Name() string {
	return p.name
}

func (p *AnthropicProvider) SupportsStreaming() bool {
	return true
}

func (p *AnthropicProvider) GetEndpoint() string {
	return p.endpoint
}

func (p *AnthropicProvider) SetAPIKey(key string) {
	p.apiKey = key
}

func (p *AnthropicProvider) IsStreaming(headers map[string][]string) bool {
	if contentType, ok := headers["Content-Type"]; ok {
		for _, ct := range contentType {
			if ct == "text/event-stream" || strings.Contains(ct, "stream") {
				return true
			}
		}
	}
	return false
}

func (p *AnthropicProvider) Transform(request []byte) ([]byte, error) {
	// Anthropic format doesn't need transformation
	return request, nil
}

func (p *AnthropicProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
	// Anthropic format doesn't need transformation for streaming
	return chunk, nil
}