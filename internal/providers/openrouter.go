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

func (p *OpenRouterProvider) Transform(response []byte) ([]byte, error) {
	// This method transforms OpenRouter RESPONSES to Anthropic format
	// Request transformation is handled in the proxy handler
	return p.convertToAnthropic(response)
}

func (p *OpenRouterProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
	var orChunk map[string]interface{}
	if err := json.Unmarshal(chunk, &orChunk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenRouter chunk: %w", err)
	}

	// Initialize content blocks map if needed
	if state.ContentBlocks == nil {
		state.ContentBlocks = make(map[int]*ContentBlockState)
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

				// Check if we have tool calls - if so, prioritize them over text content
				if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
					toolEvents := p.handleToolCalls(toolCalls, state)
					events = append(events, toolEvents...)
				} else if content, ok := delta["content"].(string); ok && content != "" {
					// Only handle text content if no tool calls are present
					textEvents := p.handleTextContent(content, state)
					events = append(events, textEvents...)
				}
			}

			// Handle finish_reason
			if finishReason, ok := firstChoice["finish_reason"]; ok && finishReason != nil {
				if reason, ok := finishReason.(string); ok {
					finishEvents := p.handleFinishReason(reason, orChunk, state)
					events = append(events, finishEvents...)
				}
			}
		}
	}

	return events, nil
}

// convertContent handles both text content and tool calls conversion
func (p *OpenRouterProvider) convertContent(message map[string]interface{}) []map[string]interface{} {
	var content []map[string]interface{}

	// Handle text content
	if textContent, ok := message["content"].(string); ok && textContent != "" {
		content = append(content, map[string]interface{}{
			"type": "text",
			"text": textContent,
		})
	}

	// Handle tool calls
	if toolCalls, ok := message["tool_calls"].([]interface{}); ok {
		for _, toolCall := range toolCalls {
			if tcMap, ok := toolCall.(map[string]interface{}); ok {
				toolContent := p.convertToolCall(tcMap)
				if toolContent != nil {
					content = append(content, toolContent)
				}
			}
		}
	}

	// Return at least empty text content if nothing else
	if len(content) == 0 {
		content = append(content, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	return content
}

// convertToolCall converts OpenRouter tool call to Anthropic tool_use format
func (p *OpenRouterProvider) convertToolCall(toolCall map[string]interface{}) map[string]interface{} {
	if function, ok := toolCall["function"].(map[string]interface{}); ok {
		toolCallID, _ := toolCall["id"].(string)
		functionName, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)

		// Parse arguments JSON
		var input map[string]interface{}
		if arguments != "" {
			if err := json.Unmarshal([]byte(arguments), &input); err != nil {
				// If parsing fails, use empty input
				input = map[string]interface{}{}
			}
		} else {
			input = map[string]interface{}{}
		}

		// Convert ID format: call_ -> toolu_
		claudeID := "toolu_" + strings.TrimPrefix(toolCallID, "call_")

		return map[string]interface{}{
			"type":  "tool_use",
			"id":    claudeID,
			"name":  functionName,
			"input": input,
		}
	}
	return nil
}

// convertAnnotations handles OpenRouter web search annotations
func (p *OpenRouterProvider) convertAnnotations(annotations interface{}) interface{} {
	// OpenRouter and Claude use the same annotation format according to docs
	// Just pass through, but we could add validation or transformation here if needed
	return annotations
}

// convertUsage handles enhanced usage information conversion
func (p *OpenRouterProvider) convertUsage(usage map[string]interface{}) map[string]interface{} {
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

	// Handle cache creation tokens (if available)
	if cacheCreationTokens, ok := usage["cache_creation_input_tokens"]; ok {
		anthropicUsage["cache_creation_input_tokens"] = cacheCreationTokens
	}

	// Handle server tool use (web search) usage
	if serverToolUse, ok := usage["server_tool_use"].(map[string]interface{}); ok {
		if webSearchRequests, ok := serverToolUse["web_search_requests"]; ok {
			// Add as additional usage info
			anthropicUsage["server_tool_use"] = map[string]interface{}{
				"web_search_requests": webSearchRequests,
			}
		}
	}

	return anthropicUsage
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

				// Handle content and tool_calls
				content := p.convertContent(message)
				anthropicResponse["content"] = content

				// Handle annotations (web search results)
				if annotations, ok := message["annotations"]; ok {
					anthropicResponse["annotations"] = p.convertAnnotations(annotations)
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

	// Transform usage object with enhanced handling
	if usage, ok := orResponse["usage"].(map[string]interface{}); ok {
		anthropicUsage := p.convertUsage(usage)
		anthropicResponse["usage"] = anthropicUsage
	}

	// Default values
	if _, ok := anthropicResponse["stop_reason"]; !ok {
		anthropicResponse["stop_reason"] = nil
	}
	if _, ok := anthropicResponse["stop_sequence"]; !ok {
		anthropicResponse["stop_sequence"] = nil
	}

	// Remove any tool_choice field that might be present in OpenRouter responses
	// Claude response format doesn't include tool_choice (only in requests)
	delete(anthropicResponse, "tool_choice")

	return json.Marshal(anthropicResponse)
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

// handleTextContent processes text content streaming
func (p *OpenRouterProvider) handleTextContent(content string, state *StreamState) []byte {
	var events []byte

	// Get or create text content block at index 0 (or next available)
	textIndex := 0
	if _, exists := state.ContentBlocks[textIndex]; !exists {
		state.ContentBlocks[textIndex] = &ContentBlockState{
			Type: "text",
		}
	}

	contentBlock := state.ContentBlocks[textIndex]

	// Send content_block_start for text if not sent yet
	if !contentBlock.StartSent {
		contentBlockStartEvent := map[string]interface{}{
			"type":  "content_block_start",
			"index": textIndex,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		}
		events = append(events, p.formatSSEEvent("content_block_start", contentBlockStartEvent)...)
		contentBlock.StartSent = true
	}

	// Send content_block_delta for text
	contentDeltaEvent := map[string]interface{}{
		"type":  "content_block_delta",
		"index": textIndex,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": content,
		},
	}
	events = append(events, p.formatSSEEvent("content_block_delta", contentDeltaEvent)...)

	return events
}

// handleToolCalls processes tool call streaming
func (p *OpenRouterProvider) handleToolCalls(toolCalls []interface{}, state *StreamState) []byte {
	var events []byte

	for _, toolCall := range toolCalls {
		if tcMap, ok := toolCall.(map[string]interface{}); ok {
			toolCallEvents := p.handleSingleToolCall(tcMap, state)
			events = append(events, toolCallEvents...)
		}
	}

	return events
}

// handleSingleToolCall processes a single tool call
func (p *OpenRouterProvider) handleSingleToolCall(toolCall map[string]interface{}, state *StreamState) []byte {
	var events []byte

	toolCallID, _ := toolCall["id"].(string)

	// Skip tool calls with empty IDs
	if toolCallID == "" {
		return events
	}

	// Get function details
	if function, ok := toolCall["function"].(map[string]interface{}); ok {
		functionName, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)

		// Find existing content block for this tool call or create new one
		var contentBlockIndex int = -1
		for idx, block := range state.ContentBlocks {
			if block.Type == "tool_use" && block.ToolCallID == toolCallID {
				contentBlockIndex = idx
				break
			}
		}

		// Create new content block if not found
		if contentBlockIndex == -1 {
			// Find next available index, starting from 0
			contentBlockIndex = len(state.ContentBlocks)
			state.ContentBlocks[contentBlockIndex] = &ContentBlockState{
				Type:       "tool_use",
				ToolCallID: toolCallID,
				ToolName:   functionName,
				Arguments:  "",
			}
		}

		contentBlock := state.ContentBlocks[contentBlockIndex]

		// Send content_block_start for tool_use if not sent yet
		if !contentBlock.StartSent {
			// Convert toolCallID to Claude format (toolu_ prefix)
			claudeToolID := "toolu_" + strings.TrimPrefix(toolCallID, "call_")

			contentBlockStartEvent := map[string]interface{}{
				"type":  "content_block_start",
				"index": contentBlockIndex,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    claudeToolID,
					"name":  functionName,
					"input": map[string]interface{}{},
				},
			}
			events = append(events, p.formatSSEEvent("content_block_start", contentBlockStartEvent)...)
			contentBlock.StartSent = true
		}

		// Handle arguments delta if we have new content
		if arguments != "" && arguments != contentBlock.Arguments {
			// Find the new part to send as delta
			newPart := arguments[len(contentBlock.Arguments):]
			contentBlock.Arguments = arguments

			// Send input_json_delta
			inputDeltaEvent := map[string]interface{}{
				"type":  "content_block_delta",
				"index": contentBlockIndex,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": newPart,
				},
			}
			events = append(events, p.formatSSEEvent("content_block_delta", inputDeltaEvent)...)
		}
	}

	return events
}

// handleFinishReason processes finish reasons and sends appropriate events
func (p *OpenRouterProvider) handleFinishReason(reason string, orChunk map[string]interface{}, state *StreamState) []byte {
	var events []byte

	// Send content_block_stop for all active content blocks
	for index, contentBlock := range state.ContentBlocks {
		if contentBlock.StartSent && !contentBlock.StopSent {
			contentStopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": index,
			}
			events = append(events, p.formatSSEEvent("content_block_stop", contentStopEvent)...)
			contentBlock.StopSent = true
		}
	}

	// Send message_delta with stop reason
	messageDeltaEvent := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   p.convertStopReason(reason),
			"stop_sequence": nil,
		},
	}

	// Add usage if present with enhanced handling
	if usage, ok := orChunk["usage"].(map[string]interface{}); ok {
		usageData := p.convertUsage(usage)
		if len(usageData) > 0 {
			messageDeltaEvent["usage"] = usageData
		}
	}

	events = append(events, p.formatSSEEvent("message_delta", messageDeltaEvent)...)

	// Send message_stop
	messageStopEvent := map[string]interface{}{
		"type": "message_stop",
	}
	events = append(events, p.formatSSEEvent("message_stop", messageStopEvent)...)

	return events
}
