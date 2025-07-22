package providers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiProvider_BasicMethods(t *testing.T) {
	provider := NewGeminiProvider()

	assert.Equal(t, "gemini", provider.Name())
	assert.True(t, provider.SupportsStreaming())

	provider.SetAPIKey("test-key")
	assert.Equal(t, "test-key", provider.apiKey)
}

func TestGeminiProvider_IsStreaming(t *testing.T) {
	provider := NewGeminiProvider()

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

func TestGeminiProvider_Transform(t *testing.T) {
	provider := NewGeminiProvider()

	geminiResponse := map[string]interface{}{
		"responseId":   "gemini-response-123",
		"modelVersion": "gemini-2.0-flash",
		"candidates": []map[string]interface{}{
			{
				"index": 0,
				"content": map[string]interface{}{
					"role": "model",
					"parts": []map[string]interface{}{
						{
							"text": "Hello! How can I help you today?",
						},
					},
				},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]interface{}{
			"promptTokenCount":     9,
			"candidatesTokenCount": 12,
			"totalTokenCount":      21,
		},
	}

	geminiJSON, err := json.Marshal(geminiResponse)
	require.NoError(t, err)

	result, err := provider.Transform(geminiJSON)
	require.NoError(t, err)

	var anthropicResp map[string]interface{}
	err = json.Unmarshal(result, &anthropicResp)
	require.NoError(t, err)

	// Check basic structure
	assert.Equal(t, "gemini-response-123", anthropicResp["id"])
	assert.Equal(t, "message", anthropicResp["type"])
	assert.Equal(t, "assistant", anthropicResp["role"])
	assert.Equal(t, "gemini-2.0-flash", anthropicResp["model"])

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

func TestGeminiProvider_ConvertStopReason(t *testing.T) {
	provider := NewGeminiProvider()

	tests := []struct {
		geminiReason      string
		expectedAnthropic string
	}{
		{"STOP", "end_turn"},
		{"MAX_TOKENS", "max_tokens"},
		{"SAFETY", "stop_sequence"},
		{"RECITATION", "stop_sequence"},
		{"LANGUAGE", "stop_sequence"},
		{"OTHER", "end_turn"},
		{"BLOCKLIST", "stop_sequence"},
		{"PROHIBITED_CONTENT", "stop_sequence"},
		{"SPII", "stop_sequence"},
		{"MALFORMED_FUNCTION_CALL", "tool_use"},
		{"FINISH_REASON_UNSPECIFIED", "end_turn"},
		{"unknown", "end_turn"},
	}

	for _, tt := range tests {
		t.Run(tt.geminiReason, func(t *testing.T) {
			result := provider.convertStopReason(tt.geminiReason)
			assert.Equal(t, tt.expectedAnthropic, *result)
		})
	}
}

func TestGeminiProvider_FunctionCallsTransform(t *testing.T) {
	provider := NewGeminiProvider()

	geminiResponse := map[string]interface{}{
		"responseId":   "gemini-response-123",
		"modelVersion": "gemini-2.0-flash",
		"candidates": []map[string]interface{}{
			{
				"index": 0,
				"content": map[string]interface{}{
					"role": "model",
					"parts": []map[string]interface{}{
						{
							"functionCall": map[string]interface{}{
								"name": "get_weather",
								"args": map[string]interface{}{
									"location": "San Francisco",
									"unit":     "celsius",
								},
							},
						},
					},
				},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]interface{}{
			"promptTokenCount":     9,
			"candidatesTokenCount": 12,
			"totalTokenCount":      21,
		},
	}

	geminiJSON, err := json.Marshal(geminiResponse)
	require.NoError(t, err)

	result, err := provider.Transform(geminiJSON)
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
		assert.Contains(t, *idPtr, "toolu_")
	} else {
		assert.Contains(t, id.(string), "toolu_")
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
		assert.Equal(t, "end_turn", *stopPtr)
	} else {
		assert.Equal(t, "end_turn", stopReason.(string))
	}
}

func TestGeminiProvider_ErrorHandling(t *testing.T) {
	provider := NewGeminiProvider()

	errorResponse := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    400,
			"message": "Invalid API key",
			"status":  "UNAUTHENTICATED",
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

func TestGeminiProvider_TransformStream(t *testing.T) {
	provider := NewGeminiProvider()
	state := &StreamState{}

	// Test message start chunk
	messageStartChunk := map[string]interface{}{
		"responseId":   "gemini-response-123",
		"modelVersion": "gemini-2.0-flash",
		"candidates": []map[string]interface{}{
			{
				"index": 0,
				"content": map[string]interface{}{
					"role": "model",
					"parts": []map[string]interface{}{
						{
							"text": "Hello!",
						},
					},
				},
			},
		},
	}

	chunkJSON, err := json.Marshal(messageStartChunk)
	require.NoError(t, err)

	events, err := provider.TransformStream(chunkJSON, state)
	require.NoError(t, err)

	// Should generate message_start and content events
	eventStr := string(events)
	assert.Contains(t, eventStr, "event: message_start")
	assert.Contains(t, eventStr, "gemini-response-123")
	assert.Contains(t, eventStr, "event: content_block_start")
	assert.Contains(t, eventStr, "event: content_block_delta")
	assert.Contains(t, eventStr, "Hello!")
	assert.True(t, state.MessageStartSent)

	// Test finish chunk
	finishChunk := map[string]interface{}{
		"responseId":   "gemini-response-123",
		"modelVersion": "gemini-2.0-flash",
		"candidates": []map[string]interface{}{
			{
				"index":        0,
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]interface{}{
			"candidatesTokenCount": 5,
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

func TestGeminiProvider_StreamingFunctionCalls(t *testing.T) {
	provider := NewGeminiProvider()
	state := &StreamState{}

	// Function call chunk
	functionCallChunk := map[string]interface{}{
		"responseId":   "gemini-response-123",
		"modelVersion": "gemini-2.0-flash",
		"candidates": []map[string]interface{}{
			{
				"index": 0,
				"content": map[string]interface{}{
					"role": "model",
					"parts": []map[string]interface{}{
						{
							"functionCall": map[string]interface{}{
								"name": "get_current_time",
								"args": map[string]interface{}{
									"timezone": "UTC",
								},
							},
						},
					},
				},
			},
		},
	}

	chunkJSON, err := json.Marshal(functionCallChunk)
	require.NoError(t, err)

	events, err := provider.TransformStream(chunkJSON, state)
	require.NoError(t, err)

	eventStr := string(events)
	assert.Contains(t, eventStr, "event: message_start")
	assert.Contains(t, eventStr, "event: content_block_start")
	assert.Contains(t, eventStr, "tool_use")
	assert.Contains(t, eventStr, "get_current_time")
	assert.Contains(t, eventStr, "event: content_block_delta")
	assert.Contains(t, eventStr, "input_json_delta")
	assert.Contains(t, eventStr, "UTC")
}

func TestGeminiProvider_ConvertUsage(t *testing.T) {
	provider := NewGeminiProvider()

	usage := map[string]interface{}{
		"promptTokenCount":     100,
		"candidatesTokenCount": 50,
		"totalTokenCount":      150,
	}

	result := provider.convertUsage(usage)

	assert.Equal(t, 100, result["input_tokens"])
	assert.Equal(t, 50, result["output_tokens"])
}

func TestGeminiProvider_MapGeminiErrorType(t *testing.T) {
	provider := NewGeminiProvider()

	tests := []struct {
		geminiType        string
		expectedAnthropic string
	}{
		{"INVALID_ARGUMENT", "invalid_request_error"},
		{"UNAUTHENTICATED", "authentication_error"},
		{"PERMISSION_DENIED", "permission_error"},
		{"NOT_FOUND", "not_found_error"},
		{"RESOURCE_EXHAUSTED", "rate_limit_error"},
		{"INTERNAL", "api_error"},
		{"UNAVAILABLE", "overloaded_error"},
		{"DEADLINE_EXCEEDED", "rate_limit_error"},
		{"unknown_error", "api_error"},
	}

	for _, tt := range tests {
		t.Run(tt.geminiType, func(t *testing.T) {
			result := provider.mapGeminiErrorType(tt.geminiType)
			assert.Equal(t, tt.expectedAnthropic, result)
		})
	}
}

func TestGeminiProvider_EmptyContent(t *testing.T) {
	provider := NewGeminiProvider()

	geminiResponse := map[string]interface{}{
		"responseId":   "gemini-response-123",
		"modelVersion": "gemini-2.0-flash",
		"candidates": []map[string]interface{}{
			{
				"index":        0,
				"finishReason": "STOP",
				// No content field
			},
		},
	}

	geminiJSON, err := json.Marshal(geminiResponse)
	require.NoError(t, err)

	result, err := provider.Transform(geminiJSON)
	require.NoError(t, err)

	var anthropicResp map[string]interface{}
	err = json.Unmarshal(result, &anthropicResp)
	require.NoError(t, err)

	// Should have empty text content
	content, ok := anthropicResp["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, content, 1)

	textBlock := content[0].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	text, ok := textBlock["text"]
	require.True(t, ok)
	if textPtr, isPtr := text.(*string); isPtr {
		assert.Equal(t, "", *textPtr)
	} else {
		assert.Equal(t, "", text.(string))
	}
}
