package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Davincible/claude-code-router-go/internal/config"
	"github.com/Davincible/claude-code-router-go/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformAnthropicToOpenAI_RemovesCacheControl(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := &ProxyHandler{logger: logger}

	// Test request with cache_control at multiple levels
	anthropicRequest := map[string]interface{}{
		"model": "claude-3-5-sonnet",
		"cache_control": map[string]interface{}{
			"type": "ephemeral",
		},
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "Hello",
				"cache_control": map[string]interface{}{
					"type": "ephemeral",
				},
			},
			map[string]interface{}{
				"role":    "user",
				"content": "World",
				// No cache_control here
			},
		},
		"max_tokens": 100,
	}

	inputData, err := json.Marshal(anthropicRequest)
	require.NoError(t, err)

	result, err := handler.transformAnthropicToOpenAI(inputData)
	require.NoError(t, err, "transformAnthropicToOpenAI should not fail")

	var cleanedRequest map[string]interface{}
	err = json.Unmarshal(result, &cleanedRequest)
	require.NoError(t, err, "should be able to parse cleaned request")

	// Verify cache_control is removed from root level
	assert.NotContains(t, cleanedRequest, "cache_control", "cache_control should be removed from root level")

	// Verify cache_control is removed from messages
	messages, ok := cleanedRequest["messages"].([]interface{})
	require.True(t, ok, "messages should be an array")
	require.Len(t, messages, 2, "should have 2 messages")

	firstMessage, ok := messages[0].(map[string]interface{})
	require.True(t, ok, "first message should be a map")
	assert.NotContains(t, firstMessage, "cache_control", "cache_control should be removed from first message")

	secondMessage, ok := messages[1].(map[string]interface{})
	require.True(t, ok, "second message should be a map")
	assert.NotContains(t, secondMessage, "cache_control", "cache_control should be removed from second message")

	// Verify other fields are preserved
	assert.Equal(t, "claude-3-5-sonnet", cleanedRequest["model"], "model should be preserved")
	assert.Equal(t, float64(100), cleanedRequest["max_tokens"], "max_tokens should be preserved")
	assert.Equal(t, "Hello", firstMessage["content"], "first message content should be preserved")
	assert.Equal(t, "user", firstMessage["role"], "first message role should be preserved")
	assert.Equal(t, "World", secondMessage["content"], "second message content should be preserved")
	assert.Equal(t, "user", secondMessage["role"], "second message role should be preserved")
}

func TestRemoveFieldsRecursively(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := &ProxyHandler{logger: logger}

	testData := map[string]interface{}{
		"keep": "this",
		"cache_control": map[string]interface{}{
			"type": "ephemeral",
		},
		"nested": map[string]interface{}{
			"keep_nested": "value",
			"cache_control": map[string]interface{}{
				"type": "ephemeral",
			},
			"deep": map[string]interface{}{
				"cache_control": "remove_me",
				"keep_deep":     "deep_value",
			},
		},
		"array": []interface{}{
			map[string]interface{}{
				"cache_control": "remove",
				"keep_array":    "array_value",
			},
		},
	}

	result, ok := handler.removeFieldsRecursively(testData, []string{"cache_control"}).(map[string]interface{})
	require.True(t, ok, "result should be a map")

	// Check root level
	assert.NotContains(t, result, "cache_control", "cache_control should be removed from root")
	assert.Equal(t, "this", result["keep"], "other fields should be preserved")

	// Check nested level
	nested, ok := result["nested"].(map[string]interface{})
	require.True(t, ok, "nested should be a map")
	assert.NotContains(t, nested, "cache_control", "cache_control should be removed from nested object")
	assert.Equal(t, "value", nested["keep_nested"], "other nested fields should be preserved")

	// Check deep nested level
	deep, ok := nested["deep"].(map[string]interface{})
	require.True(t, ok, "deep should be a map")
	assert.NotContains(t, deep, "cache_control", "cache_control should be removed from deep nested object")
	assert.Equal(t, "deep_value", deep["keep_deep"], "other deep nested fields should be preserved")

	// Check array level
	array, ok := result["array"].([]interface{})
	require.True(t, ok, "array should be a slice")
	require.Len(t, array, 1, "array should have 1 item")

	arrayItem, ok := array[0].(map[string]interface{})
	require.True(t, ok, "array item should be a map")
	assert.NotContains(t, arrayItem, "cache_control", "cache_control should be removed from array items")
	assert.Equal(t, "array_value", arrayItem["keep_array"], "other array item fields should be preserved")
}

func TestSelectModel_DynamicProviderSelection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := &ProxyHandler{logger: logger}

	routerConfig := &config.RouterConfig{
		Default:     "default,claude-3-5-sonnet",
		LongContext: "longcontext,claude-3-opus",
		Think:       "think,claude-3-5-sonnet",
		WebSearch:   "websearch,claude-3-5-sonnet:online",
		Background:  "background,claude-3-5-haiku",
	}

	testCases := []struct {
		name          string
		inputModel    string
		tokens        int
		expectedModel string
		expectedBody  string
		description   string
	}{
		{
			name:          "explicit provider with comma",
			inputModel:    "openrouter,anthropic/claude-sonnet-4",
			tokens:        1000,
			expectedModel: "openrouter,anthropic/claude-sonnet-4",
			expectedBody:  "anthropic/claude-sonnet-4",
			description:   "should use explicit provider/model when comma format is used",
		},
		{
			name:          "explicit provider overrides long context",
			inputModel:    "openrouter,anthropic/claude-sonnet-4",
			tokens:        70000, // This would normally trigger LongContext
			expectedModel: "openrouter,anthropic/claude-sonnet-4",
			expectedBody:  "anthropic/claude-sonnet-4",
			description:   "should prioritize explicit provider over automatic routing",
		},
		{
			name:          "automatic routing for long context",
			inputModel:    "claude-3-5-sonnet",
			tokens:        70000,
			expectedModel: "longcontext,claude-3-opus",
			expectedBody:  "claude-3-opus",
			description:   "should use long context routing for high token count",
		},
		{
			name:          "automatic routing for haiku background",
			inputModel:    "claude-3-5-haiku",
			tokens:        1000,
			expectedModel: "background,claude-3-5-haiku",
			expectedBody:  "claude-3-5-haiku",
			description:   "should use background routing for haiku model",
		},
		{
			name:          "passthrough for simple model",
			inputModel:    "claude-3-5-sonnet",
			tokens:        1000,
			expectedModel: "think,claude-3-5-sonnet",
			expectedBody:  "claude-3-5-sonnet",
			description:   "should use think routing when no other rules apply",
		},
		{
			name:          "online suffix preservation",
			inputModel:    "openrouter,anthropic/claude-sonnet-4:online",
			tokens:        1000,
			expectedModel: "openrouter,anthropic/claude-sonnet-4:online",
			expectedBody:  "anthropic/claude-sonnet-4:online",
			description:   "should preserve :online suffix for web search",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create test request body
			requestBody := map[string]interface{}{
				"model":      tc.inputModel,
				"messages":   []interface{}{},
				"max_tokens": 100,
			}

			inputBody, err := json.Marshal(requestBody)
			require.NoError(t, err)

			// Call selectModel
			resultBody, selectedModel := handler.selectModel(inputBody, tc.tokens, routerConfig)

			// Verify selected model
			assert.Equal(t, tc.expectedModel, selectedModel, tc.description)

			// Verify request body has correct model
			var parsedResult map[string]interface{}
			err = json.Unmarshal(resultBody, &parsedResult)
			require.NoError(t, err)

			assert.Equal(t, tc.expectedBody, parsedResult["model"], "request body should contain the final model name")
		})
	}
}

func TestSelectModel_NoModelProvided(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := &ProxyHandler{logger: logger}

	routerConfig := &config.RouterConfig{
		Default: "default,claude-3-5-sonnet",
	}

	// Create test request body without model
	requestBody := map[string]interface{}{
		"messages":   []interface{}{},
		"max_tokens": 100,
	}

	inputBody, err := json.Marshal(requestBody)
	require.NoError(t, err)

	// Call selectModel
	resultBody, selectedModel := handler.selectModel(inputBody, 1000, routerConfig)

	// Should use default
	assert.Equal(t, "default,claude-3-5-sonnet", selectedModel)

	// Verify request body has correct model
	var parsedResult map[string]interface{}
	err = json.Unmarshal(resultBody, &parsedResult)
	require.NoError(t, err)

	assert.Equal(t, "claude-3-5-sonnet", parsedResult["model"])
}

func TestHandleResponse_ErrorForwarding(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a mock provider that would normally transform responses
	mockProvider := &MockProvider{
		shouldTransform: true,
	}

	handler := &ProxyHandler{logger: logger}

	testCases := []struct {
		name            string
		statusCode      int
		responseBody    string
		shouldTransform bool
		description     string
	}{
		{
			name:            "error response not transformed",
			statusCode:      400,
			responseBody:    `{"error":{"type":"invalid_request_error","message":"Invalid model specified"}}`,
			shouldTransform: false,
			description:     "error responses should be forwarded without transformation",
		},
		{
			name:            "success response transformed",
			statusCode:      200,
			responseBody:    `{"id":"test","choices":[{"message":{"role":"assistant","content":"Hello"}}]}`,
			shouldTransform: true,
			description:     "success responses should be transformed",
		},
		{
			name:            "server error not transformed",
			statusCode:      500,
			responseBody:    `{"error":{"type":"internal_server_error","message":"Internal server error"}}`,
			shouldTransform: false,
			description:     "server errors should be forwarded without transformation",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock provider
			mockProvider.transformCalled = false

			// Create mock HTTP response
			resp := &http.Response{
				StatusCode: tc.statusCode,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tc.responseBody)),
			}
			resp.Header.Set("Content-Type", "application/json")

			// Create response writer
			w := &MockResponseWriter{
				headers: make(http.Header),
				body:    &bytes.Buffer{},
			}

			// Call handleResponse
			handler.handleResponse(w, resp, mockProvider, 100)

			// Verify transformation was called only for success responses
			if tc.shouldTransform {
				assert.True(t, mockProvider.transformCalled, tc.description)
			} else {
				assert.False(t, mockProvider.transformCalled, tc.description)
			}

			// Verify status code is preserved
			assert.Equal(t, tc.statusCode, w.statusCode, "status code should be preserved")

			// Verify response body
			responseBody := w.body.String()
			if tc.shouldTransform {
				// For successful responses, we expect transformation
				assert.Contains(t, responseBody, "TRANSFORMED", "successful response should be transformed")
			} else {
				// For error responses, we expect original body
				assert.Equal(t, tc.responseBody, responseBody, "error response should be forwarded as-is")
			}
		})
	}
}

// Mock provider for testing
type MockProvider struct {
	transformCalled bool
	shouldTransform bool
}

func (m *MockProvider) Name() string                                 { return "mock" }
func (m *MockProvider) SupportsStreaming() bool                      { return true }
func (m *MockProvider) GetEndpoint() string                          { return "mock" }
func (m *MockProvider) SetAPIKey(key string)                         {}
func (m *MockProvider) IsStreaming(headers map[string][]string) bool { return false }
func (m *MockProvider) TransformStream(chunk []byte, state *providers.StreamState) ([]byte, error) {
	return chunk, nil
}

func (m *MockProvider) Transform(response []byte) ([]byte, error) {
	m.transformCalled = true
	if m.shouldTransform {
		return []byte(`{"transformed": true, "original": "TRANSFORMED"}`), nil
	}
	return response, nil
}

// Mock response writer for testing
type MockResponseWriter struct {
	headers    http.Header
	body       *bytes.Buffer
	statusCode int
}

func (m *MockResponseWriter) Header() http.Header {
	return m.headers
}

func (m *MockResponseWriter) Write(data []byte) (int, error) {
	return m.body.Write(data)
}

func (m *MockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

func TestRemoveAnthropicSpecificFields_ToolChoiceValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := &ProxyHandler{logger: logger}

	testCases := []struct {
		name           string
		request        map[string]interface{}
		expectedResult map[string]interface{}
		description    string
	}{
		{
			name: "tool_choice removed when no tools present",
			request: map[string]interface{}{
				"model":       "claude-3-5-sonnet",
				"messages":    []interface{}{},
				"tool_choice": "auto",
				"max_tokens":  100,
			},
			expectedResult: map[string]interface{}{
				"model":      "claude-3-5-sonnet",
				"messages":   []interface{}{},
				"max_tokens": float64(100), // JSON unmarshal converts to float64
			},
			description: "tool_choice should be removed when no tools array is present",
		},
		{
			name: "tool_choice removed when tools is null",
			request: map[string]interface{}{
				"model":       "claude-3-5-sonnet",
				"messages":    []interface{}{},
				"tools":       nil,
				"tool_choice": "auto",
				"max_tokens":  100,
			},
			expectedResult: map[string]interface{}{
				"model":      "claude-3-5-sonnet",
				"messages":   []interface{}{},
				"tools":      nil, // tools field remains as null
				"max_tokens": float64(100),
			},
			description: "tool_choice should be removed when tools is null",
		},
		{
			name: "tool_choice removed when tools is empty array",
			request: map[string]interface{}{
				"model":       "claude-3-5-sonnet",
				"messages":    []interface{}{},
				"tools":       []interface{}{},
				"tool_choice": "auto",
				"max_tokens":  100,
			},
			expectedResult: map[string]interface{}{
				"model":      "claude-3-5-sonnet",
				"messages":   []interface{}{},
				"tools":      []interface{}{}, // tools field remains as empty array
				"max_tokens": float64(100),
			},
			description: "tool_choice should be removed when tools is an empty array",
		},
		{
			name: "tool_choice preserved when tools present",
			request: map[string]interface{}{
				"model":    "claude-3-5-sonnet",
				"messages": []interface{}{},
				"tools": []interface{}{
					map[string]interface{}{
						"type": "function",
						"function": map[string]interface{}{
							"name": "get_weather",
						},
					},
				},
				"tool_choice": "auto",
				"max_tokens":  100,
			},
			expectedResult: map[string]interface{}{
				"model":    "claude-3-5-sonnet",
				"messages": []interface{}{},
				"tools": []interface{}{
					map[string]interface{}{
						"type": "function",
						"function": map[string]interface{}{
							"name": "get_weather",
						},
					},
				},
				"tool_choice": "auto",
				"max_tokens":  float64(100),
			},
			description: "tool_choice should be preserved when tools array is present and not empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := handler.removeAnthropicSpecificFields(tc.request)

			// Convert to JSON and back to normalize types for comparison
			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)

			var normalizedResult map[string]interface{}
			err = json.Unmarshal(resultJSON, &normalizedResult)
			require.NoError(t, err)

			assert.Equal(t, tc.expectedResult, normalizedResult, tc.description)
		})
	}
}

func TestTransformTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := &ProxyHandler{logger: logger}

	testCases := []struct {
		name     string
		input    []interface{}
		expected []interface{}
	}{
		{
			name: "Claude format to OpenAI format",
			input: []interface{}{
				map[string]interface{}{
					"name":        "get_weather",
					"description": "Get current weather",
					"input_schema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "City name",
							},
						},
						"required": []string{"location"},
					},
				},
			},
			expected: []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "get_weather",
						"description": "Get current weather",
						"parameters": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"location": map[string]interface{}{
									"type":        "string",
									"description": "City name",
								},
							},
							"required": []string{"location"},
						},
					},
				},
			},
		},
		{
			name: "Already OpenAI format - pass through",
			input: []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "search_books",
						"description": "Search for books",
						"parameters": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"query": map[string]interface{}{
									"type": "string",
								},
							},
						},
					},
				},
			},
			expected: []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "search_books",
						"description": "Search for books",
						"parameters": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"query": map[string]interface{}{
									"type": "string",
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := handler.transformTools(tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestTransformAnthropicToOpenAI_WithTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := &ProxyHandler{logger: logger}

	// Test complete request transformation with Claude tools
	anthropicRequest := map[string]interface{}{
		"model": "claude-3-5-sonnet",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "Get the weather for San Francisco",
			},
		},
		"tools": []interface{}{
			map[string]interface{}{
				"name":        "get_weather",
				"description": "Get current weather for a location",
				"input_schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "City name",
						},
					},
					"required": []string{"location"},
				},
			},
		},
		"tool_choice": "auto",
		"max_tokens":  100,
	}

	inputData, err := json.Marshal(anthropicRequest)
	require.NoError(t, err)

	result, err := handler.transformAnthropicToOpenAI(inputData)
	require.NoError(t, err, "transformAnthropicToOpenAI should not fail")

	var transformedRequest map[string]interface{}
	err = json.Unmarshal(result, &transformedRequest)
	require.NoError(t, err, "should be able to parse transformed request")

	// Verify tools are transformed to OpenAI format
	tools, ok := transformedRequest["tools"].([]interface{})
	require.True(t, ok, "tools should be an array")
	require.Len(t, tools, 1, "should have 1 tool")

	tool, ok := tools[0].(map[string]interface{})
	require.True(t, ok, "tool should be a map")

	assert.Equal(t, "function", tool["type"], "tool type should be function")

	function, ok := tool["function"].(map[string]interface{})
	require.True(t, ok, "tool should have function field")

	assert.Equal(t, "get_weather", function["name"], "function name should be preserved")
	assert.Equal(t, "Get current weather for a location", function["description"], "function description should be preserved")

	parameters, ok := function["parameters"].(map[string]interface{})
	require.True(t, ok, "function should have parameters")
	assert.Equal(t, "object", parameters["type"], "parameters type should be object")

	// Verify tool_choice is preserved when valid tools are present
	assert.Equal(t, "auto", transformedRequest["tool_choice"], "tool_choice should be preserved")
}

func TestHandleStreamingResponse_ErrorForwarding(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a mock provider
	mockProvider := &MockProvider{shouldTransform: true}

	handler := &ProxyHandler{logger: logger}

	// Test error response body (simulating SSE error stream)
	errorStreamBody := `data: {"error":{"type":"invalid_request_error","message":"Invalid model specified"}}

`

	// Create mock HTTP response with error status
	resp := &http.Response{
		StatusCode: 400,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(errorStreamBody)),
	}
	resp.Header.Set("Content-Type", "text/event-stream")

	// Create response writer
	w := &MockResponseWriter{
		headers: make(http.Header),
		body:    &bytes.Buffer{},
	}

	// Call handleStreamingResponse
	handler.handleStreamingResponse(w, resp, mockProvider, 100)

	// Verify transformation was NOT called for error response
	assert.False(t, mockProvider.transformCalled, "error streaming responses should not be transformed")

	// Verify status code is preserved
	assert.Equal(t, 400, w.statusCode, "error status code should be preserved")

	// Verify response body contains original error data
	responseBody := w.body.String()
	assert.Contains(t, responseBody, "invalid_request_error", "error response should be forwarded as-is")
	assert.Contains(t, responseBody, "Invalid model specified", "error message should be preserved")
}
