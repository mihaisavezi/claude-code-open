package providers

import (
	"encoding/json"
	"fmt"
	"strings"
)

type OpenRouterProvider struct {
	name     string
	endpoint string
	apiKey   string
}

func NewOpenRouterProvider() *OpenRouterProvider {
	return &OpenRouterProvider{
		name: "openrouter",
	}
}

func (p *OpenRouterProvider) Name() string {
	return p.name
}

func (p *OpenRouterProvider) SupportsStreaming() bool {
	return true
}

func (p *OpenRouterProvider) GetEndpoint() string {
	return p.endpoint
}

func (p *OpenRouterProvider) SetAPIKey(key string) {
	p.apiKey = key
}

func (p *OpenRouterProvider) IsStreaming(headers map[string][]string) bool {
	if contentType, ok := headers["Content-Type"]; ok {
		for _, ct := range contentType {
			if ct == "text/event-stream" || strings.Contains(ct, "stream") {
				return true
			}
		}
	}
	if transferEncoding, ok := headers["Transfer-Encoding"]; ok {
		for _, te := range transferEncoding {
			if te == "chunked" {
				return true
			}
		}
	}
	return false
}

func (p *OpenRouterProvider) Transform(request []byte) ([]byte, error) {
	// Remove cache_control from request for OpenRouter
	cleaned, err := p.removeCacheControl(request)
	if err != nil {
		return request, nil // Use original if cleaning fails
	}

	// Transform OpenRouter response to Anthropic format
	return p.convertToAnthropic(cleaned)
}

func (p *OpenRouterProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
	var orChunk map[string]interface{}
	if err := json.Unmarshal(chunk, &orChunk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenRouter chunk: %w", err)
	}

	var events []byte

	// Store message ID and model from first chunk
	if id, ok := orChunk["id"].(string); ok && state.MessageID == "" {
		state.MessageID = id
	}
	if model, ok := orChunk["model"].(string); ok && state.Model == "" {
		state.Model = model
	}

	// Handle choices array
	if choices, ok := orChunk["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {

			// Send message_start event if not sent yet
			if !state.MessageStartSent {
				messageStartEvent := p.createMessageStartEvent(state.MessageID, state.Model, orChunk)
				events = append(events, p.formatSSEEvent("message_start", messageStartEvent)...)
				state.MessageStartSent = true
			}

			// Handle delta content
			if delta, ok := firstChoice["delta"].(map[string]interface{}); ok {
				// Send content_block_start if we have content and haven't sent it yet
				if content, ok := delta["content"].(string); ok && content != "" && !state.ContentBlockStartSent {
					contentBlockStartEvent := map[string]interface{}{
						"type":  "content_block_start",
						"index": 0,
						"content_block": map[string]interface{}{
							"type": "text",
							"text": "",
						},
					}
					events = append(events, p.formatSSEEvent("content_block_start", contentBlockStartEvent)...)
					state.ContentBlockStartSent = true
				}

				// Handle text content delta
				if content, ok := delta["content"].(string); ok && content != "" {
					contentDeltaEvent := map[string]interface{}{
						"type":  "content_block_delta",
						"index": 0,
						"delta": map[string]interface{}{
							"type": "text_delta",
							"text": content,
						},
					}
					events = append(events, p.formatSSEEvent("content_block_delta", contentDeltaEvent)...)
				}
			}

			// Handle finish_reason
			if finishReason, ok := firstChoice["finish_reason"]; ok && finishReason != nil {
				if reason, ok := finishReason.(string); ok {
					// Send content_block_stop if we had content
					if state.ContentBlockStartSent {
						contentStopEvent := map[string]interface{}{
							"type":  "content_block_stop",
							"index": 0,
						}
						events = append(events, p.formatSSEEvent("content_block_stop", contentStopEvent)...)
					}

					// Send message_delta with stop reason
					messageDeltaEvent := map[string]interface{}{
						"type": "message_delta",
						"delta": map[string]interface{}{
							"stop_reason":   p.convertStopReason(reason),
							"stop_sequence": nil,
						},
					}

					// Add usage if present
					if usage, ok := orChunk["usage"].(map[string]interface{}); ok {
						if completionTokens, ok := usage["completion_tokens"]; ok {
							messageDeltaEvent["usage"] = map[string]interface{}{
								"output_tokens": completionTokens,
							}
						}
					}

					events = append(events, p.formatSSEEvent("message_delta", messageDeltaEvent)...)

					// Send message_stop
					messageStopEvent := map[string]interface{}{
						"type": "message_stop",
					}
					events = append(events, p.formatSSEEvent("message_stop", messageStopEvent)...)
				}
			}
		}
	}

	return events, nil
}

func (p *OpenRouterProvider) convertToAnthropic(openRouterData []byte) ([]byte, error) {
	var orResponse map[string]interface{}
	if err := json.Unmarshal(openRouterData, &orResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenRouter response: %w", err)
	}

	// Create Anthropic response structure
	anthropicResponse := make(map[string]interface{})

	// Copy ID if present
	if id, ok := orResponse["id"]; ok {
		anthropicResponse["id"] = id
	}

	// Set type
	anthropicResponse["type"] = "message"

	// Extract role and content from choices[0].message
	if choices, ok := orResponse["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := firstChoice["message"].(map[string]interface{}); ok {
				// Extract role
				if role, ok := message["role"]; ok {
					anthropicResponse["role"] = role
				}

				// Convert content from string to Anthropic's array format
				if content, ok := message["content"].(string); ok {
					anthropicResponse["content"] = []map[string]interface{}{
						{
							"type": "text",
							"text": content,
						},
					}
				}
			}

			// Map finish_reason to stop_reason
			if finishReason, ok := firstChoice["finish_reason"]; ok {
				anthropicResponse["stop_reason"] = p.convertStopReason(fmt.Sprintf("%v", finishReason))
			}
		}
	}

	// Copy model if present
	if model, ok := orResponse["model"]; ok {
		anthropicResponse["model"] = model
	}

	// Transform usage object
	if usage, ok := orResponse["usage"].(map[string]interface{}); ok {
		anthropicUsage := make(map[string]interface{})

		// Map token fields
		if promptTokens, ok := usage["prompt_tokens"]; ok {
			anthropicUsage["input_tokens"] = promptTokens
		}
		if completionTokens, ok := usage["completion_tokens"]; ok {
			anthropicUsage["output_tokens"] = completionTokens
		}

		// Handle cached tokens
		if promptDetails, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			if cachedTokens, ok := promptDetails["cached_tokens"]; ok {
				anthropicUsage["cache_read_input_tokens"] = cachedTokens
			}
		}

		anthropicResponse["usage"] = anthropicUsage
	}

	// Default values
	if _, ok := anthropicResponse["stop_reason"]; !ok {
		anthropicResponse["stop_reason"] = nil
	}
	if _, ok := anthropicResponse["stop_sequence"]; !ok {
		anthropicResponse["stop_sequence"] = nil
	}

	return json.Marshal(anthropicResponse)
}

func (p *OpenRouterProvider) removeCacheControl(jsonData []byte) ([]byte, error) {
	var data interface{}
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	cleaned := p.removeCacheControlRecursive(data)

	result, err := json.Marshal(cleaned)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return result, nil
}

func (p *OpenRouterProvider) removeCacheControlRecursive(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for key, value := range v {
			if key != "cache_control" {
				result[key] = p.removeCacheControlRecursive(value)
			}
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = p.removeCacheControlRecursive(item)
		}
		return result
	default:
		return v
	}
}

func (p *OpenRouterProvider) convertStopReason(openaiReason string) *string {
	mapping := map[string]string{
		"stop":           "end_turn",
		"length":         "max_tokens",
		"tool_calls":     "tool_use",
		"function_call":  "tool_use",
		"content_filter": "stop_sequence",
		"null":           "end_turn",
	}

	if anthropicReason, exists := mapping[openaiReason]; exists {
		return &anthropicReason
	}

	defaultReason := "end_turn"
	return &defaultReason
}

func (p *OpenRouterProvider) createMessageStartEvent(messageID, model string, firstChunk map[string]interface{}) map[string]interface{} {
	usage := map[string]interface{}{
		"input_tokens":  0,
		"output_tokens": 1,
	}

	if chunkUsage, ok := firstChunk["usage"].(map[string]interface{}); ok {
		if promptTokens, ok := chunkUsage["prompt_tokens"]; ok {
			usage["input_tokens"] = promptTokens
		}
		if promptDetails, ok := chunkUsage["prompt_tokens_details"].(map[string]interface{}); ok {
			if cachedTokens, ok := promptDetails["cached_tokens"]; ok {
				usage["cache_read_input_tokens"] = cachedTokens
			}
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

func (p *OpenRouterProvider) formatSSEEvent(eventType string, data map[string]interface{}) []byte {
	jsonData, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}