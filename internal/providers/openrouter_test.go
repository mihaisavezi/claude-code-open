package providers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	require.NoError(t, err, "transform should not fail")

	var anthropicResponse map[string]interface{}
	err = json.Unmarshal(result, &anthropicResponse)
	require.NoError(t, err, "should be able to parse result")

	// Verify Anthropic format
	assert.Equal(t, "message", anthropicResponse["type"], "should have message type")
	assert.Equal(t, "assistant", anthropicResponse["role"], "should have assistant role")

	// Check content format
	content, ok := anthropicResponse["content"].([]interface{})
	require.True(t, ok, "content should be an array")
	require.NotEmpty(t, content, "content should not be empty")

	firstContent, ok := content[0].(map[string]interface{})
	require.True(t, ok, "first content should be a map")
	assert.Equal(t, "text", firstContent["type"], "content type should be text")
	assert.Equal(t, "Hello! How can I help you today?", firstContent["text"], "text content should match")

	// Check usage transformation
	usage, ok := anthropicResponse["usage"].(map[string]interface{})
	require.True(t, ok, "usage should be an object")
	assert.Equal(t, float64(25), usage["input_tokens"], "input_tokens should match")
	assert.Equal(t, float64(8), usage["output_tokens"], "output_tokens should match")
	assert.Equal(t, float64(10), usage["cache_read_input_tokens"], "cache_read_input_tokens should match")
}

func TestOpenRouterProvider_SupportsStreaming(t *testing.T) {
	provider := NewOpenRouterProvider()
	assert.True(t, provider.SupportsStreaming(), "OpenRouter should support streaming")
}

func TestOpenRouterProvider_Name(t *testing.T) {
	provider := NewOpenRouterProvider()
	assert.Equal(t, "openrouter", provider.Name(), "provider name should be openrouter")
}

func TestOpenRouterProvider_IsStreaming(t *testing.T) {
	provider := NewOpenRouterProvider()

	testCases := []struct {
		name     string
		headers  map[string][]string
		expected bool
	}{
		{
			name: "content-type event-stream",
			headers: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			expected: true,
		},
		{
			name: "transfer-encoding chunked",
			headers: map[string][]string{
				"Transfer-Encoding": {"chunked"},
			},
			expected: true,
		},
		{
			name: "no streaming headers",
			headers: map[string][]string{
				"Content-Type": {"application/json"},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := provider.IsStreaming(tc.headers)
			assert.Equal(t, tc.expected, result, "streaming detection should match expected")
		})
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

	require.NoError(t, err, "TransformStream should not fail")

	// Verify result contains SSE events
	resultStr := string(result)
	assert.Contains(t, resultStr, "event: message_start", "should contain message_start event")
	assert.Contains(t, resultStr, "event: content_block_start", "should contain content_block_start event")
	assert.Contains(t, resultStr, "event: content_block_delta", "should contain content_block_delta event")
	assert.Contains(t, resultStr, "text_delta", "should contain text_delta in response")
}

func TestOpenRouterProvider_ConvertStopReason(t *testing.T) {
	provider := NewOpenRouterProvider()

	testCases := []struct {
		openaiReason     string
		anthropicReason string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"function_call", "tool_use"},
		{"content_filter", "stop_sequence"},
		{"null", "end_turn"},
		{"unknown", "end_turn"}, // default case
	}

	for _, tc := range testCases {
		t.Run(tc.openaiReason, func(t *testing.T) {
			result := provider.convertStopReason(tc.openaiReason)
			require.NotNil(t, result, "result should not be nil")
			assert.Equal(t, tc.anthropicReason, *result, "stop reason should be converted correctly")
		})
	}
}