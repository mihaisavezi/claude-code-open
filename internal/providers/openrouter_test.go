package providers

import (
	"encoding/json"
	"testing"
)

func TestOpenRouterProvider_Transform(t *testing.T) {
	provider := NewOpenRouterProvider()

	// Test OpenRouter response format
	openRouterResponse := map[string]interface{}{
		"id":     "chatcmpl-123",
		"object": "chat.completion",
		"model":  "anthropic/claude-3.5-sonnet",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello! How can I help you today?",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     25,
			"completion_tokens": 8,
			"total_tokens":      33,
			"prompt_tokens_details": map[string]interface{}{
				"cached_tokens": 10,
			},
		},
	}

	inputData, _ := json.Marshal(openRouterResponse)
	result, err := provider.Transform(inputData)

	if err != nil {
		t.Fatalf("Transform failed: %v", err)
	}

	var anthropicResponse map[string]interface{}
	if err := json.Unmarshal(result, &anthropicResponse); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	// Verify Anthropic format
	if anthropicResponse["type"] != "message" {
		t.Errorf("Expected type 'message', got %v", anthropicResponse["type"])
	}

	if anthropicResponse["role"] != "assistant" {
		t.Errorf("Expected role 'assistant', got %v", anthropicResponse["role"])
	}

	// Check content format
	content, ok := anthropicResponse["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Errorf("Expected content array, got %v", anthropicResponse["content"])
	}

	firstContent := content[0].(map[string]interface{})
	if firstContent["type"] != "text" {
		t.Errorf("Expected content type 'text', got %v", firstContent["type"])
	}

	if firstContent["text"] != "Hello! How can I help you today?" {
		t.Errorf("Expected specific text, got %v", firstContent["text"])
	}

	// Check usage transformation
	usage, ok := anthropicResponse["usage"].(map[string]interface{})
	if !ok {
		t.Errorf("Expected usage object, got %v", anthropicResponse["usage"])
	}

	if usage["input_tokens"] != 25 {
		t.Errorf("Expected input_tokens 25, got %v", usage["input_tokens"])
	}

	if usage["output_tokens"] != 8 {
		t.Errorf("Expected output_tokens 8, got %v", usage["output_tokens"])
	}

	if usage["cache_read_input_tokens"] != 10 {
		t.Errorf("Expected cache_read_input_tokens 10, got %v", usage["cache_read_input_tokens"])
	}
}

func TestOpenRouterProvider_RemoveCacheControl(t *testing.T) {
	provider := NewOpenRouterProvider()

	// Test request with cache_control
	requestWithCache := map[string]interface{}{
		"model": "anthropic/claude-3.5-sonnet",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "Hello",
				"cache_control": map[string]interface{}{
					"type": "ephemeral",
				},
			},
		},
		"cache_control": map[string]interface{}{
			"type": "ephemeral",
		},
	}

	inputData, _ := json.Marshal(requestWithCache)
	result, err := provider.removeCacheControl(inputData)

	if err != nil {
		t.Fatalf("removeCacheControl failed: %v", err)
	}

	var cleanedRequest map[string]interface{}
	if err := json.Unmarshal(result, &cleanedRequest); err != nil {
		t.Fatalf("Failed to parse cleaned request: %v", err)
	}

	// Verify cache_control is removed from root
	if _, exists := cleanedRequest["cache_control"]; exists {
		t.Errorf("cache_control should be removed from root")
	}

	// Verify cache_control is removed from messages
	messages := cleanedRequest["messages"].([]interface{})
	firstMessage := messages[0].(map[string]interface{})
	if _, exists := firstMessage["cache_control"]; exists {
		t.Errorf("cache_control should be removed from messages")
	}

	// Verify other fields are preserved
	if cleanedRequest["model"] != "anthropic/claude-3.5-sonnet" {
		t.Errorf("Model should be preserved")
	}

	if firstMessage["content"] != "Hello" {
		t.Errorf("Message content should be preserved")
	}
}

func TestOpenRouterProvider_TransformStream(t *testing.T) {
	provider := NewOpenRouterProvider()
	state := &StreamState{}

	// Test streaming chunk
	chunk := map[string]interface{}{
		"id":    "chatcmpl-123",
		"model": "anthropic/claude-3.5-sonnet",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "Hello",
				},
			},
		},
	}

	chunkData, _ := json.Marshal(chunk)
	result, err := provider.TransformStream(chunkData, state)

	if err != nil {
		t.Fatalf("TransformStream failed: %v", err)
	}

	// Verify result contains SSE events
	resultStr := string(result)
	if !contains(resultStr, "event: message_start") {
		t.Errorf("Expected message_start event")
	}

	if !contains(resultStr, "event: content_block_start") {
		t.Errorf("Expected content_block_start event")
	}

	if !contains(resultStr, "event: content_block_delta") {
		t.Errorf("Expected content_block_delta event")
	}

	if !contains(resultStr, "text_delta") {
		t.Errorf("Expected text_delta in response")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}