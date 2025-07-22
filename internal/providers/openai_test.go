package providers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIProvider_BasicMethods(t *testing.T) {
	provider := NewOpenAIProvider()

	assert.Equal(t, "openai", provider.Name())
	assert.True(t, provider.SupportsStreaming())

	provider.SetAPIKey("test-key")
	assert.Equal(t, "test-key", provider.apiKey)
}

func TestOpenAIProvider_IsStreaming(t *testing.T) {
	provider := NewOpenAIProvider()

	tests := []struct {
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.IsStreaming(tt.headers)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestOpenAIProvider_Transform(t *testing.T) {
	provider := NewOpenAIProvider()

	openaiResponse := map[string]interface{}{
		"id":      "chatcmpl-123",
		"object":  "chat.completion",
		"created": 1677652288,
		"model":   "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello! How can I help you today?",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     9,
			"completion_tokens": 12,
			"total_tokens":      21,
		},
	}

	openaiJSON, err := json.Marshal(openaiResponse)
	require.NoError(t, err)

	result, err := provider.Transform(openaiJSON)
	require.NoError(t, err)

	var anthropicResp map[string]interface{}
	err = json.Unmarshal(result, &anthropicResp)
	require.NoError(t, err)

	// Check basic structure
	assert.Equal(t, "chatcmpl-123", anthropicResp["id"])
	assert.Equal(t, "message", anthropicResp["type"])
	assert.Equal(t, "assistant", anthropicResp["role"])
	assert.Equal(t, "gpt-4", anthropicResp["model"])

	// Check content
	content, ok := anthropicResp["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, content, 1)

	textBlock := content[0].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	text, ok := textBlock["text"]
	require.True(t, ok)
	if textPtr, isPtr := text.(*string); isPtr {
		assert.Equal(t, "Hello! How can I help you today?", *textPtr)
	} else {
		assert.Equal(t, "Hello! How can I help you today?", text.(string))
	}

	// Check usage
	usage, ok := anthropicResp["usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(9), usage["input_tokens"])
	assert.Equal(t, float64(12), usage["output_tokens"])

	// Check stop reason
	stopReason, ok := anthropicResp["stop_reason"]
	require.True(t, ok)
	if stopPtr, isPtr := stopReason.(*string); isPtr {
		assert.Equal(t, "end_turn", *stopPtr)
	} else {
		assert.Equal(t, "end_turn", stopReason.(string))
	}
}

func TestOpenAIProvider_ConvertStopReason(t *testing.T) {
	provider := NewOpenAIProvider()

	tests := []struct {
		openaiReason      string
		expectedAnthropic string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"function_call", "tool_use"},
		{"content_filter", "stop_sequence"},
		{"null", "end_turn"},
		{"unknown", "end_turn"},
	}

	for _, tt := range tests {
		t.Run(tt.openaiReason, func(t *testing.T) {
			result := provider.convertStopReason(tt.openaiReason)
			assert.Equal(t, tt.expectedAnthropic, *result)
		})
	}
}

func TestOpenAIProvider_ToolCallsTransform(t *testing.T) {
	provider := NewOpenAIProvider()

	openaiResponse := map[string]interface{}{
		"id":      "chatcmpl-123",
		"object":  "chat.completion",
		"created": 1677652288,
		"model":   "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]interface{}{
						{
							"id":   "call_abc123",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "get_weather",
								"arguments": "{\"location\":\"San Francisco\",\"unit\":\"celsius\"}",
							},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     9,
			"completion_tokens": 12,
			"total_tokens":      21,
		},
	}

	openaiJSON, err := json.Marshal(openaiResponse)
	require.NoError(t, err)

	result, err := provider.Transform(openaiJSON)
	require.NoError(t, err)

	var anthropicResp map[string]interface{}
	err = json.Unmarshal(result, &anthropicResp)
	require.NoError(t, err)

	// Check content contains tool use
	content, ok := anthropicResp["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, content, 1)

	toolBlock := content[0].(map[string]interface{})
	assert.Equal(t, "tool_use", toolBlock["type"])

	id, ok := toolBlock["id"]
	require.True(t, ok)
	if idPtr, isPtr := id.(*string); isPtr {
		assert.Equal(t, "toolu_abc123", *idPtr)
	} else {
		assert.Equal(t, "toolu_abc123", id.(string))
	}

	name, ok := toolBlock["name"]
	require.True(t, ok)
	if namePtr, isPtr := name.(*string); isPtr {
		assert.Equal(t, "get_weather", *namePtr)
	} else {
		assert.Equal(t, "get_weather", name.(string))
	}

	// Check tool input
	input, ok := toolBlock["input"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "San Francisco", input["location"])
	assert.Equal(t, "celsius", input["unit"])

	// Check stop reason
	stopReason, ok := anthropicResp["stop_reason"]
	require.True(t, ok)
	if stopPtr, isPtr := stopReason.(*string); isPtr {
		assert.Equal(t, "tool_use", *stopPtr)
	} else {
		assert.Equal(t, "tool_use", stopReason.(string))
	}
}

func TestOpenAIProvider_ErrorHandling(t *testing.T) {
	provider := NewOpenAIProvider()

	errorResponse := map[string]interface{}{
		"error": map[string]interface{}{
			"message": "Invalid API key",
			"type":    "authentication_error",
			"code":    "invalid_api_key",
		},
	}

	errorJSON, err := json.Marshal(errorResponse)
	require.NoError(t, err)

	result, err := provider.Transform(errorJSON)
	require.NoError(t, err)

	var anthropicResp map[string]interface{}
	err = json.Unmarshal(result, &anthropicResp)
	require.NoError(t, err)

	assert.Equal(t, "error", anthropicResp["type"])

	errorInfo, ok := anthropicResp["error"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "authentication_error", errorInfo["type"])
	assert.Equal(t, "Invalid API key", errorInfo["message"])
}

func TestOpenAIProvider_TransformStream(t *testing.T) {
	provider := NewOpenAIProvider()
	state := &StreamState{}

	// Test message start chunk
	messageStartChunk := map[string]interface{}{
		"id":    "chatcmpl-123",
		"model": "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"role": "assistant",
				},
			},
		},
	}

	chunkJSON, err := json.Marshal(messageStartChunk)
	require.NoError(t, err)

	events, err := provider.TransformStream(chunkJSON, state)
	require.NoError(t, err)

	// Should generate message_start event
	eventStr := string(events)
	assert.Contains(t, eventStr, "event: message_start")
	assert.Contains(t, eventStr, "chatcmpl-123")
	assert.True(t, state.MessageStartSent)

	// Test text content chunk
	textChunk := map[string]interface{}{
		"id":    "chatcmpl-123",
		"model": "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "Hello!",
				},
			},
		},
	}

	chunkJSON, err = json.Marshal(textChunk)
	require.NoError(t, err)

	events, err = provider.TransformStream(chunkJSON, state)
	require.NoError(t, err)

	eventStr = string(events)
	assert.Contains(t, eventStr, "event: content_block_start")
	assert.Contains(t, eventStr, "event: content_block_delta")
	assert.Contains(t, eventStr, "Hello!")

	// Test finish chunk
	finishChunk := map[string]interface{}{
		"id":    "chatcmpl-123",
		"model": "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"completion_tokens": 5,
		},
	}

	chunkJSON, err = json.Marshal(finishChunk)
	require.NoError(t, err)

	events, err = provider.TransformStream(chunkJSON, state)
	require.NoError(t, err)

	eventStr = string(events)
	assert.Contains(t, eventStr, "event: content_block_stop")
	assert.Contains(t, eventStr, "event: message_delta")
	assert.Contains(t, eventStr, "event: message_stop")
	assert.Contains(t, eventStr, "end_turn")
}

func TestOpenAIProvider_StreamingToolCalls(t *testing.T) {
	provider := NewOpenAIProvider()
	state := &StreamState{}

	// First chunk with tool call start
	toolCallStartChunk := map[string]interface{}{
		"id":    "chatcmpl-123",
		"model": "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{
							"index": 0,
							"id":    "call_abc123",
							"type":  "function",
							"function": map[string]interface{}{
								"name":      "ls",
								"arguments": "",
							},
						},
					},
				},
			},
		},
	}

	chunkJSON, err := json.Marshal(toolCallStartChunk)
	require.NoError(t, err)

	events, err := provider.TransformStream(chunkJSON, state)
	require.NoError(t, err)

	eventStr := string(events)
	assert.Contains(t, eventStr, "event: content_block_start")
	assert.Contains(t, eventStr, "toolu_abc123")
	assert.Contains(t, eventStr, "tool_use")

	// Second chunk with arguments
	toolCallArgsChunk := map[string]interface{}{
		"id":    "chatcmpl-123",
		"model": "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{
							"index": 0,
							"function": map[string]interface{}{
								"arguments": "{\"path\":\"/home\"}",
							},
						},
					},
				},
			},
		},
	}

	chunkJSON, err = json.Marshal(toolCallArgsChunk)
	require.NoError(t, err)

	events, err = provider.TransformStream(chunkJSON, state)
	require.NoError(t, err)

	eventStr = string(events)
	assert.Contains(t, eventStr, "event: content_block_delta")
	assert.Contains(t, eventStr, "input_json_delta")
	assert.Contains(t, eventStr, "/home")
}

func TestOpenAIProvider_ConvertUsage(t *testing.T) {
	provider := NewOpenAIProvider()

	usage := map[string]interface{}{
		"prompt_tokens":     100,
		"completion_tokens": 50,
		"total_tokens":      150,
		"prompt_tokens_details": map[string]interface{}{
			"cached_tokens": 20,
		},
		"cache_creation_input_tokens": 10,
	}

	result := provider.convertUsage(usage)

	assert.Equal(t, 100, result["input_tokens"])
	assert.Equal(t, 50, result["output_tokens"])
	assert.Equal(t, 20, result["cache_read_input_tokens"])
	assert.Equal(t, 10, result["cache_creation_input_tokens"])
}

func TestOpenAIProvider_ConvertToolCallID(t *testing.T) {
	provider := NewOpenAIProvider()

	tests := []struct {
		input    string
		expected string
	}{
		{"call_abc123", "toolu_abc123"},
		{"toolu_abc123", "toolu_abc123"},
		{"xyz789", "toolu_xyz789"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := provider.convertToolCallID(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
