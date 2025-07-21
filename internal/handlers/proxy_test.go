package handlers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformAnthropicToOpenAI_RemovesCacheControl(t *testing.T) {
	handler := &ProxyHandler{}

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
	handler := &ProxyHandler{}

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