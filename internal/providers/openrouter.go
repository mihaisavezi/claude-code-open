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
	if p.endpoint == "" {
		return "https://api.openrouter.ai/v1/chat/completions"
	}

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
				// Convert tool call to Claude format

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
	function, ok := toolCall["function"].(map[string]interface{})
	if !ok {
		return nil
	}

	toolCallID, _ := toolCall["id"].(string)
	functionName, _ := function["name"].(string)
	arguments, _ := function["arguments"].(string)

	// Parse arguments JSON
	input := p.parseToolArguments(arguments)

	// Convert ID format: call_ -> toolu_
	claudeID := p.convertToolCallID(toolCallID)

	return map[string]interface{}{
		"type":  "tool_use",
		"id":    claudeID,
		"name":  functionName,
		"input": input,
	}
}

// parseToolArguments parses JSON arguments or returns empty map
func (p *OpenRouterProvider) parseToolArguments(arguments string) map[string]interface{} {
	if arguments == "" {
		return map[string]interface{}{}
	}

	var input map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		// If parsing fails, use empty input
		return map[string]interface{}{}
	}

	return input
}

// convertToolCallID converts OpenRouter tool call ID to Claude format
func (p *OpenRouterProvider) convertToolCallID(toolCallID string) string {
	if strings.HasPrefix(toolCallID, "toolu_") {
		return toolCallID
	}
	if strings.HasPrefix(toolCallID, "call_") {
		return "toolu_" + strings.TrimPrefix(toolCallID, "call_")
	}
	return "toolu_" + toolCallID
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

	// Get or create text content block at index 0
	textIndex := p.getOrCreateTextBlock(state)
	contentBlock := state.ContentBlocks[textIndex]

	// Send content_block_start event if needed
	if !contentBlock.StartSent {
		events = append(events, p.createTextBlockStartEvent(textIndex)...)
		contentBlock.StartSent = true
	}

	// Send content_block_delta event
	events = append(events, p.createTextDeltaEvent(textIndex, content)...)

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

	// Parse tool call data using helper
	toolCallData := p.parseToolCallData(toolCall)

	// Find or create content block
	contentBlockIndex := p.findOrCreateContentBlock(toolCallData, state)
	if contentBlockIndex == -1 {
		return events // Skip if couldn't find or create
	}

	contentBlock := state.ContentBlocks[contentBlockIndex]

	// Update content block with new data
	p.updateContentBlock(contentBlock, toolCallData)

	// Send content_block_start event if needed
	if !contentBlock.StartSent && p.shouldSendStartEvent(contentBlock) {
		events = append(events, p.createContentBlockStartEvent(contentBlockIndex, contentBlock)...)
		contentBlock.StartSent = true
	}

	// Handle argument streaming
	if toolCallData.Arguments != "" && toolCallData.Arguments != contentBlock.Arguments {
		newPart := p.calculateArgumentsDelta(toolCallData.Arguments, contentBlock.Arguments)
		contentBlock.Arguments = toolCallData.Arguments

		if newPart != "" {
			events = append(events, p.createInputDeltaEvent(contentBlockIndex, newPart)...)
		}
	}

	return events
}

// ToolCallData holds parsed tool call information
type ToolCallData struct {
	Index        int
	HasIndex     bool
	ID           string
	FunctionName string
	Arguments    string
}

// parseToolCallData extracts tool call information from OpenRouter chunk
func (p *OpenRouterProvider) parseToolCallData(toolCall map[string]interface{}) ToolCallData {
	data := ToolCallData{}

	// Parse tool call index
	toolCallIndex, hasIndex := toolCall["index"].(float64)
	if !hasIndex {
		if idx, ok := toolCall["index"].(int); ok {
			toolCallIndex = float64(idx)
			hasIndex = true
		}
	}
	data.Index = int(toolCallIndex)
	data.HasIndex = hasIndex

	// Parse ID and function details
	data.ID, _ = toolCall["id"].(string)
	if function, ok := toolCall["function"].(map[string]interface{}); ok {
		data.FunctionName, _ = function["name"].(string)
		data.Arguments, _ = function["arguments"].(string)
	}

	// Tool call data parsed successfully

	return data
}

// findOrCreateContentBlock locates existing content block or creates new one
func (p *OpenRouterProvider) findOrCreateContentBlock(data ToolCallData, state *StreamState) int {
	// First try to find by tool call index
	if data.HasIndex {
		for blockIdx, block := range state.ContentBlocks {
			if block.Type == "tool_use" && block.ToolCallIndex == data.Index {
				return blockIdx
			}
		}
	}

	// Then try to find by ID
	if data.ID != "" {
		for blockIdx, block := range state.ContentBlocks {
			if block.Type == "tool_use" && block.ToolCallID == data.ID {
				return blockIdx
			}
		}
	}

	// Create new content block if we have an ID (first chunk)
	if data.ID != "" {
		contentBlockIndex := len(state.ContentBlocks)
		state.ContentBlocks[contentBlockIndex] = &ContentBlockState{
			Type:          "tool_use",
			ToolCallID:    data.ID,
			ToolCallIndex: data.Index,
			ToolName:      data.FunctionName,
			Arguments:     "",
		}
		return contentBlockIndex
	}

	return -1 // Couldn't find or create
}

// updateContentBlock updates content block with new tool call data
func (p *OpenRouterProvider) updateContentBlock(block *ContentBlockState, data ToolCallData) {
	if data.FunctionName != "" {
		block.ToolName = data.FunctionName
	}
}

// shouldSendStartEvent determines if content_block_start event should be sent
func (p *OpenRouterProvider) shouldSendStartEvent(block *ContentBlockState) bool {
	return block.ToolCallID != "" && block.ToolName != ""
}

// createContentBlockStartEvent creates content_block_start SSE event
func (p *OpenRouterProvider) createContentBlockStartEvent(index int, block *ContentBlockState) []byte {
	claudeToolID := p.convertToolCallID(block.ToolCallID)

	contentBlockStartEvent := map[string]interface{}{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]interface{}{
			"type":  "tool_use",
			"id":    claudeToolID,
			"name":  block.ToolName,
			"input": map[string]interface{}{},
		},
	}
	return p.formatSSEEvent("content_block_start", contentBlockStartEvent)
}

// calculateArgumentsDelta calculates the incremental part of arguments
func (p *OpenRouterProvider) calculateArgumentsDelta(newArgs, oldArgs string) string {
	// Check if arguments are incremental (common case)
	if len(newArgs) > len(oldArgs) && strings.HasPrefix(newArgs, oldArgs) {
		return newArgs[len(oldArgs):] // Extract new part
	}
	// Non-incremental case - return entire new arguments
	return newArgs
}

// createInputDeltaEvent creates input_json_delta SSE event
func (p *OpenRouterProvider) createInputDeltaEvent(index int, partialJSON string) []byte {
	inputDeltaEvent := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": partialJSON,
		},
	}
	return p.formatSSEEvent("content_block_delta", inputDeltaEvent)
}

// getOrCreateTextBlock gets or creates text content block at index 0
func (p *OpenRouterProvider) getOrCreateTextBlock(state *StreamState) int {
	textIndex := 0
	if _, exists := state.ContentBlocks[textIndex]; !exists {
		state.ContentBlocks[textIndex] = &ContentBlockState{
			Type: "text",
		}
	}
	return textIndex
}

// createTextBlockStartEvent creates content_block_start event for text
func (p *OpenRouterProvider) createTextBlockStartEvent(index int) []byte {
	contentBlockStartEvent := map[string]interface{}{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]interface{}{
			"type": "text",
			"text": "",
		},
	}
	return p.formatSSEEvent("content_block_start", contentBlockStartEvent)
}

// createTextDeltaEvent creates content_block_delta event for text
func (p *OpenRouterProvider) createTextDeltaEvent(index int, text string) []byte {
	contentDeltaEvent := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": text,
		},
	}
	return p.formatSSEEvent("content_block_delta", contentDeltaEvent)
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
