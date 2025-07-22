package providers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BaseProvider provides common functionality for all providers
type BaseProvider struct {
	name     string
	endpoint string
	apiKey   string
}

// TokenMapping defines how to map token fields between formats
type TokenMapping struct {
	InputTokens            string
	OutputTokens           string
	CacheReadInputTokens   string
	CacheCreateInputTokens string
}

// Common token field mappings
var (
	OpenAITokenMapping = TokenMapping{
		InputTokens:            "prompt_tokens",
		OutputTokens:           "completion_tokens",
		CacheReadInputTokens:   "cached_tokens",
		CacheCreateInputTokens: "cache_creation_tokens",
	}

	AnthropicTokenMapping = TokenMapping{
		InputTokens:            "input_tokens",
		OutputTokens:           "output_tokens",
		CacheReadInputTokens:   "cache_read_input_tokens",
		CacheCreateInputTokens: "cache_create_input_tokens",
	}
)

// Common utility functions

// IsStreamingContentType checks if the content type indicates streaming
func IsStreamingContentType(contentType string) bool {
	return contentType == "text/event-stream" || strings.Contains(contentType, "stream")
}

// FormatSSEEvent formats data as a Server-Sent Event
func FormatSSEEvent(eventType string, data interface{}) []byte {
	jsonData, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

// MapTokenUsage maps token usage from source format to Anthropic format
func MapTokenUsage(sourceUsage map[string]interface{}, sourceMapping TokenMapping) map[string]interface{} {
	anthropicUsage := make(map[string]interface{})

	// Map basic token fields
	if promptTokens, ok := sourceUsage[sourceMapping.InputTokens]; ok {
		anthropicUsage[AnthropicTokenMapping.InputTokens] = promptTokens
	}
	if completionTokens, ok := sourceUsage[sourceMapping.OutputTokens]; ok {
		anthropicUsage[AnthropicTokenMapping.OutputTokens] = completionTokens
	}

	// Handle detailed token information
	if promptDetails, ok := sourceUsage["prompt_tokens_details"].(map[string]interface{}); ok {
		if cachedTokens, ok := promptDetails[sourceMapping.CacheReadInputTokens]; ok {
			anthropicUsage[AnthropicTokenMapping.CacheReadInputTokens] = cachedTokens
		}
		if cacheCreationTokens, ok := promptDetails[sourceMapping.CacheCreateInputTokens]; ok {
			anthropicUsage[AnthropicTokenMapping.CacheCreateInputTokens] = cacheCreationTokens
		}
	}

	if completionDetails, ok := sourceUsage["completion_tokens_details"].(map[string]interface{}); ok {
		for key, value := range completionDetails {
			anthropicUsage["completion_"+key] = value
		}
	}

	return anthropicUsage
}

// ConvertStopReason converts various stop reason formats to Anthropic format
func ConvertStopReason(reason string) *string {
	mapping := map[string]string{
		"stop":           "end_turn",
		"length":         "max_tokens",
		"tool_calls":     "tool_use",
		"function_call":  "tool_use",
		"content_filter": "stop_sequence",
		"null":           "end_turn",
		"":               "end_turn",
	}

	if anthropicReason, exists := mapping[reason]; exists {
		return &anthropicReason
	}

	defaultReason := "end_turn"
	return &defaultReason
}

// RemoveFieldsRecursively removes specified fields from nested JSON structures
func RemoveFieldsRecursively(data interface{}, fieldsToRemove []string) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for key, value := range v {
			shouldRemove := false
			for _, field := range fieldsToRemove {
				if key == field {
					shouldRemove = true
					break
				}
			}
			if !shouldRemove {
				result[key] = RemoveFieldsRecursively(value, fieldsToRemove)
			}
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = RemoveFieldsRecursively(item, fieldsToRemove)
		}
		return result
	default:
		return v
	}
}

// CreateAnthropicContent creates Anthropic content blocks from text
func CreateAnthropicContent(text string) []map[string]interface{} {
	if text == "" {
		text = ""
	}

	return []map[string]interface{}{
		{
			"type": "text",
			"text": text,
		},
	}
}

// CreateMessageStartEvent creates a standard Anthropic message_start event
func CreateMessageStartEvent(messageID, model string, usage map[string]interface{}) map[string]interface{} {
	if usage == nil {
		usage = map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 1,
		}
	}

	return map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         usage,
		},
	}
}

// ExtractModelFromConfig parses provider,model format
func ExtractModelFromConfig(modelConfig string) (provider, model string) {
	parts := strings.SplitN(modelConfig, ",", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", strings.TrimSpace(modelConfig)
}
