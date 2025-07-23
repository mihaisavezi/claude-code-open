package providers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type NvidiaProvider struct {
	name     string
	endpoint string
	apiKey   string
}

func NewNvidiaProvider() *NvidiaProvider {
	return &NvidiaProvider{
		name: "nvidia",
	}
}

func (p *NvidiaProvider) Name() string {
	return p.name
}

func (p *NvidiaProvider) SupportsStreaming() bool {
	return true
}

func (p *NvidiaProvider) GetEndpoint() string {
	if p.endpoint == "" {
		return "https://integrate.api.nvidia.com/v1/chat/completions"
	}

	return p.endpoint
}

func (p *NvidiaProvider) SetAPIKey(key string) {
	p.apiKey = key
}

func (p *NvidiaProvider) IsStreaming(headers map[string][]string) bool {
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

func (p *NvidiaProvider) TransformRequest(request []byte) ([]byte, error) {
	// Nvidia uses OpenAI format, so we need to transform Anthropic to OpenAI
	return p.transformAnthropicToOpenAI(request)
}

func (p *NvidiaProvider) TransformResponse(response []byte) ([]byte, error) {
	// Transform Nvidia response to Anthropic format
	return p.convertNvidiaToAnthropic(response)
}

func (p *NvidiaProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
	return p.convertNvidiaToAnthropicStream(chunk, state)
}

// Nvidia format structures (same as OpenAI since they use OpenAI API spec)
type nvidiaResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []nvidiaChoice `json:"choices"`
	Usage             *nvidiaUsage   `json:"usage,omitempty"`
	SystemFingerprint *string        `json:"system_fingerprint,omitempty"`
	Error             *nvidiaError   `json:"error,omitempty"`
}

type nvidiaChoice struct {
	Index        int            `json:"index"`
	Message      *nvidiaMessage `json:"message,omitempty"`
	Delta        *nvidiaMessage `json:"delta,omitempty"`
	Logprobs     interface{}    `json:"logprobs,omitempty"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

type nvidiaMessage struct {
	Role         string           `json:"role"`
	Content      *string          `json:"content,omitempty"`
	Name         *string          `json:"name,omitempty"`
	ToolCalls    []nvidiaToolCall `json:"tool_calls,omitempty"`
	ToolCallId   *string          `json:"tool_call_id,omitempty"`
	FunctionCall *nvidiaFunction  `json:"function_call,omitempty"`
}

type nvidiaToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function nvidiaFunction `json:"function"`
}

type nvidiaFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type nvidiaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type nvidiaError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param,omitempty"`
	Code    *string `json:"code,omitempty"`
}

func (p *NvidiaProvider) convertNvidiaToAnthropic(nvidiaData []byte) ([]byte, error) {
	var nvidiaResp nvidiaResponse
	if err := json.Unmarshal(nvidiaData, &nvidiaResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Nvidia response: %w", err)
	}

	// Handle error responses
	if nvidiaResp.Error != nil {
		anthropicResp := anthropicResponse{
			ID:    nvidiaResp.ID,
			Type:  "error",
			Model: nvidiaResp.Model,
			Error: &anthropicError{
				Type:    p.mapNvidiaErrorType(nvidiaResp.Error.Type),
				Message: nvidiaResp.Error.Message,
			},
		}

		return json.Marshal(anthropicResp)
	}

	// Handle streaming vs non-streaming responses
	if len(nvidiaResp.Choices) == 0 {
		return nil, errors.New("no choices in Nvidia response")
	}

	choice := nvidiaResp.Choices[0]

	message := choice.Message
	if message == nil {
		message = choice.Delta // Handle streaming responses
	}

	if message == nil {
		return nil, errors.New("no message content in choice")
	}

	anthropicResp := anthropicResponse{
		ID:    nvidiaResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: nvidiaResp.Model,
	}

	// Convert content based on message type
	content, err := p.convertMessageContent(message)
	if err != nil {
		return nil, fmt.Errorf("failed to convert message content: %w", err)
	}

	anthropicResp.Content = content

	// Convert stop reason
	if choice.FinishReason != nil {
		anthropicResp.StopReason = p.convertStopReason(*choice.FinishReason)
	}

	// Convert usage
	if nvidiaResp.Usage != nil {
		usage := &anthropicUsage{
			InputTokens:  nvidiaResp.Usage.PromptTokens,
			OutputTokens: nvidiaResp.Usage.CompletionTokens,
		}
		anthropicResp.Usage = usage
	}

	return json.Marshal(anthropicResp)
}

func (p *NvidiaProvider) convertMessageContent(message *nvidiaMessage) ([]anthropicContent, error) {
	var content []anthropicContent

	// Handle regular text content
	if message.Content != nil && *message.Content != "" {
		content = append(content, anthropicContent{
			Type: "text",
			Text: message.Content,
		})
	}

	// Handle tool calls
	if len(message.ToolCalls) > 0 {
		for _, toolCall := range message.ToolCalls {
			var input map[string]interface{}
			if toolCall.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
					return nil, fmt.Errorf("failed to parse tool call arguments: %w", err)
				}
			}

			claudeID := p.convertToolCallID(toolCall.ID)
			content = append(content, anthropicContent{
				Type:  "tool_use",
				ID:    &claudeID,
				Name:  &toolCall.Function.Name,
				Input: input,
			})
		}
	}

	// Handle tool results
	if message.Role == "tool" && message.ToolCallId != nil {
		var toolContent interface{}

		if message.Content != nil {
			var jsonContent interface{}
			if err := json.Unmarshal([]byte(*message.Content), &jsonContent); err == nil {
				toolContent = jsonContent
			} else {
				toolContent = *message.Content
			}
		}

		claudeToolID := p.convertToolCallID(*message.ToolCallId)
		content = append(content, anthropicContent{
			Type:      "tool_result",
			ToolUseId: &claudeToolID,
			Content:   toolContent,
		})
	}

	// Handle legacy function calls
	if message.FunctionCall != nil {
		var input map[string]interface{}
		if message.FunctionCall.Arguments != "" {
			if err := json.Unmarshal([]byte(message.FunctionCall.Arguments), &input); err != nil {
				return nil, fmt.Errorf("failed to parse function call arguments: %w", err)
			}
		}

		id := fmt.Sprintf("func_%d", time.Now().UnixNano())
		content = append(content, anthropicContent{
			Type:  "tool_use",
			ID:    &id,
			Name:  &message.FunctionCall.Name,
			Input: input,
		})
	}

	// If no content was generated, add empty text block
	if len(content) == 0 {
		emptyText := ""
		content = append(content, anthropicContent{
			Type: "text",
			Text: &emptyText,
		})
	}

	return content, nil
}

func (p *NvidiaProvider) convertStopReason(nvidiaReason string) *string {
	mapping := map[string]string{
		"stop":           "end_turn",
		"length":         "max_tokens",
		"tool_calls":     "tool_use",
		"function_call":  "tool_use",
		"content_filter": "stop_sequence",
		"null":           "end_turn",
	}

	if anthropicReason, exists := mapping[nvidiaReason]; exists {
		return &anthropicReason
	}

	defaultReason := "end_turn"

	return &defaultReason
}

func (p *NvidiaProvider) mapNvidiaErrorType(nvidiaType string) string {
	mapping := map[string]string{
		"invalid_request_error":    "invalid_request_error",
		"authentication_error":     "authentication_error",
		"permission_error":         "permission_error",
		"not_found_error":          "not_found_error",
		"rate_limit_error":         "rate_limit_error",
		"api_error":                "api_error",
		"overloaded_error":         "overloaded_error",
		"insufficient_quota_error": "billing_error",
	}

	if anthropicType, exists := mapping[nvidiaType]; exists {
		return anthropicType
	}

	return "api_error"
}

func (p *NvidiaProvider) convertNvidiaToAnthropicStream(nvidiaData []byte, state *StreamState) ([]byte, error) {
	var rawChunk map[string]interface{}
	if err := json.Unmarshal(nvidiaData, &rawChunk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Nvidia streaming response: %w", err)
	}

	var events []byte

	// Store message ID and model from first chunk
	if id, ok := rawChunk["id"].(string); ok && state.MessageID == "" {
		state.MessageID = id
	}

	if model, ok := rawChunk["model"].(string); ok && state.Model == "" {
		state.Model = model
	}

	// Handle choices array
	if choices, ok := rawChunk["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {
			// Send message_start event if not sent yet
			if !state.MessageStartSent {
				messageStartEvent := p.createMessageStartEvent(state.MessageID, state.Model, rawChunk)
				events = append(events, p.formatSSEEvent("message_start", messageStartEvent)...)
				state.MessageStartSent = true
			}

			// Handle delta content
			if delta, ok := firstChoice["delta"].(map[string]interface{}); ok {
				// Initialize content blocks map if needed
				if state.ContentBlocks == nil {
					state.ContentBlocks = make(map[int]*ContentBlockState)
				}

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
					finishEvents := p.handleFinishReason(reason, rawChunk, state)
					events = append(events, finishEvents...)
				}
			}
		}
	}

	return events, nil
}

func (p *NvidiaProvider) createMessageStartEvent(messageID, model string, firstChunk map[string]interface{}) map[string]interface{} {
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

func (p *NvidiaProvider) formatSSEEvent(eventType string, data map[string]interface{}) []byte {
	jsonData, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

// handleTextContent processes text content streaming
func (p *NvidiaProvider) handleTextContent(content string, state *StreamState) []byte {
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
func (p *NvidiaProvider) handleToolCalls(toolCalls []interface{}, state *StreamState) []byte {
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
func (p *NvidiaProvider) handleSingleToolCall(toolCall map[string]interface{}, state *StreamState) []byte {
	var events []byte

	// Parse tool call data
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

// NvidiaToolCallData holds parsed tool call information for Nvidia provider
type NvidiaToolCallData struct {
	Index        int
	HasIndex     bool
	ID           string
	FunctionName string
	Arguments    string
}

// parseToolCallData extracts tool call information from Nvidia chunk
func (p *NvidiaProvider) parseToolCallData(toolCall map[string]interface{}) NvidiaToolCallData {
	data := NvidiaToolCallData{}

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

	return data
}

// findOrCreateContentBlock locates existing content block or creates new one
func (p *NvidiaProvider) findOrCreateContentBlock(data NvidiaToolCallData, state *StreamState) int {
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
func (p *NvidiaProvider) updateContentBlock(block *ContentBlockState, data NvidiaToolCallData) {
	if data.FunctionName != "" {
		block.ToolName = data.FunctionName
	}
}

// shouldSendStartEvent determines if content_block_start event should be sent
func (p *NvidiaProvider) shouldSendStartEvent(block *ContentBlockState) bool {
	return block.ToolCallID != "" && block.ToolName != ""
}

// createContentBlockStartEvent creates content_block_start SSE event
func (p *NvidiaProvider) createContentBlockStartEvent(index int, block *ContentBlockState) []byte {
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

// convertToolCallID converts Nvidia tool call ID to Claude format
func (p *NvidiaProvider) convertToolCallID(toolCallID string) string {
	if strings.HasPrefix(toolCallID, "toolu_") {
		return toolCallID
	}

	if strings.HasPrefix(toolCallID, "call_") {
		return "toolu_" + strings.TrimPrefix(toolCallID, "call_")
	}

	return "toolu_" + toolCallID
}

// calculateArgumentsDelta calculates the incremental part of arguments
func (p *NvidiaProvider) calculateArgumentsDelta(newArgs, oldArgs string) string {
	// Check if arguments are incremental (common case)
	if len(newArgs) > len(oldArgs) && strings.HasPrefix(newArgs, oldArgs) {
		return newArgs[len(oldArgs):] // Extract new part
	}
	// Non-incremental case - return entire new arguments
	return newArgs
}

// createInputDeltaEvent creates input_json_delta SSE event
func (p *NvidiaProvider) createInputDeltaEvent(index int, partialJSON string) []byte {
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
func (p *NvidiaProvider) getOrCreateTextBlock(state *StreamState) int {
	textIndex := 0
	if _, exists := state.ContentBlocks[textIndex]; !exists {
		state.ContentBlocks[textIndex] = &ContentBlockState{
			Type: "text",
		}
	}

	return textIndex
}

// createTextBlockStartEvent creates content_block_start event for text
func (p *NvidiaProvider) createTextBlockStartEvent(index int) []byte {
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
func (p *NvidiaProvider) createTextDeltaEvent(index int, text string) []byte {
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
func (p *NvidiaProvider) handleFinishReason(reason string, chunk map[string]interface{}, state *StreamState) []byte {
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

	// Add usage if present
	if usage, ok := chunk["usage"].(map[string]interface{}); ok {
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

// convertUsage handles usage information conversion
func (p *NvidiaProvider) convertUsage(usage map[string]interface{}) map[string]interface{} {
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

	return anthropicUsage
}

// transformAnthropicToOpenAI converts Anthropic/Claude format to OpenAI format for Nvidia
func (p *NvidiaProvider) transformAnthropicToOpenAI(anthropicRequest []byte) ([]byte, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(anthropicRequest, &request); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Anthropic request: %w", err)
	}

	// Remove Anthropic-specific fields that OpenAI doesn't support
	cleanedRequest := p.removeAnthropicSpecificFields(request)

	// Handle system parameter - convert it to a system message in messages array
	if systemContent, hasSystem := cleanedRequest["system"]; hasSystem {
		if messages, ok := cleanedRequest["messages"].([]interface{}); ok {
			// Create system message
			systemMessage := map[string]interface{}{
				"role":    "system",
				"content": systemContent,
			}

			// Prepend system message to messages array
			newMessages := append([]interface{}{systemMessage}, messages...)
			cleanedRequest["messages"] = newMessages
		}
		// Remove the system parameter as OpenAI doesn't support it at root level
		delete(cleanedRequest, "system")
	}

	// Handle max_tokens parameter - convert to max_completion_tokens for OpenAI compatibility
	if maxTokens, hasMaxTokens := cleanedRequest["max_tokens"]; hasMaxTokens {
		cleanedRequest["max_completion_tokens"] = maxTokens
		delete(cleanedRequest, "max_tokens")
	}

	// Transform any Anthropic-specific message formats if needed
	if messages, ok := cleanedRequest["messages"].([]interface{}); ok {
		cleanedRequest["messages"] = p.transformMessages(messages)
	}

	// Transform tools from Claude format to OpenAI format if present
	if tools, ok := cleanedRequest["tools"].([]interface{}); ok {
		transformedTools, err := p.transformTools(tools)
		if err != nil {
			// If tools transformation fails, remove tool_choice to prevent validation errors
			delete(cleanedRequest, "tool_choice")
		} else {
			cleanedRequest["tools"] = transformedTools

			// Re-validate tool_choice after successful transformation
			// If transformed tools array is empty, remove tool_choice
			if len(transformedTools) == 0 {
				delete(cleanedRequest, "tool_choice")
			}
		}
	}

	return json.Marshal(cleanedRequest)
}

// Helper methods for transformAnthropicToOpenAI (reused from OpenAI provider logic)
func (p *NvidiaProvider) removeAnthropicSpecificFields(request map[string]interface{}) map[string]interface{} {
	fieldsToRemove := []string{"cache_control"}

	if store, hasStore := request["store"]; !hasStore || store != true {
		fieldsToRemove = append(fieldsToRemove, "metadata")
	}

	cleaned := p.removeFieldsRecursively(request, fieldsToRemove).(map[string]interface{})

	if tools, hasTools := cleaned["tools"]; !hasTools || tools == nil {
		delete(cleaned, "tool_choice")
	} else if toolsArray, ok := tools.([]interface{}); ok && len(toolsArray) == 0 {
		delete(cleaned, "tool_choice")
	}

	return cleaned
}

func (p *NvidiaProvider) removeFieldsRecursively(data interface{}, fieldsToRemove []string) interface{} {
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
				result[key] = p.removeFieldsRecursively(value, fieldsToRemove)
			}
		}

		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = p.removeFieldsRecursively(item, fieldsToRemove)
		}

		return result
	default:
		return v
	}
}

func (p *NvidiaProvider) transformTools(tools []interface{}) ([]interface{}, error) {
	transformedTools := make([]interface{}, 0, len(tools))

	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}

		if toolType, hasType := toolMap["type"].(string); hasType && toolType == "function" {
			if _, hasFunction := toolMap["function"]; hasFunction {
				transformedTools = append(transformedTools, tool)
				continue
			}
		}

		if name, hasName := toolMap["name"].(string); hasName {
			openAITool := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": name,
				},
			}

			function := openAITool["function"].(map[string]interface{})

			if description, hasDesc := toolMap["description"].(string); hasDesc {
				function["description"] = description
			}

			if inputSchema, hasInputSchema := toolMap["input_schema"]; hasInputSchema {
				function["parameters"] = inputSchema
			}

			transformedTools = append(transformedTools, openAITool)
		}
	}

	return transformedTools, nil
}

func (p *NvidiaProvider) transformMessages(messages []interface{}) []interface{} {
	transformedMessages := make([]interface{}, 0, len(messages))

	for _, message := range messages {
		if msgMap, ok := message.(map[string]interface{}); ok {
			if role, ok := msgMap["role"].(string); ok {
				if role == "user" {
					if content, ok := msgMap["content"].([]interface{}); ok {
						toolResultMessages := p.extractToolResults(content)
						if len(toolResultMessages) > 0 {
							transformedMessages = append(transformedMessages, toolResultMessages...)
							continue
						}
					}
				} else if role == "assistant" {
					if content, ok := msgMap["content"].([]interface{}); ok {
						transformedMsg := p.transformAssistantMessage(msgMap, content)
						transformedMessages = append(transformedMessages, transformedMsg)

						continue
					}
				}
			}
		}

		transformedMessages = append(transformedMessages, message)
	}

	return transformedMessages
}

func (p *NvidiaProvider) extractToolResults(content []interface{}) []interface{} {
	var toolMessages []interface{}

	for _, block := range content {
		if blockMap, ok := block.(map[string]interface{}); ok {
			if blockType, ok := blockMap["type"].(string); ok && blockType == "tool_result" {
				if toolUseId, ok := blockMap["tool_use_id"].(string); ok {
					toolCallId := strings.Replace(toolUseId, "toolu_", "call_", 1)

					toolMessage := map[string]interface{}{
						"role":         "tool",
						"tool_call_id": toolCallId,
						"content":      blockMap["content"],
					}
					toolMessages = append(toolMessages, toolMessage)
				}
			}
		}
	}

	if len(toolMessages) > 0 {
		return toolMessages
	}

	return nil
}

func (p *NvidiaProvider) transformAssistantMessage(msgMap map[string]interface{}, content []interface{}) map[string]interface{} {
	transformedMsg := make(map[string]interface{})
	for k, v := range msgMap {
		transformedMsg[k] = v
	}

	var (
		textContent strings.Builder
		toolCalls   []interface{}
	)

	for _, block := range content {
		if blockMap, ok := block.(map[string]interface{}); ok {
			blockType, _ := blockMap["type"].(string)

			switch blockType {
			case "text":
				if text, ok := blockMap["text"].(string); ok {
					textContent.WriteString(text)
				}
			case "tool_use":
				if id, ok := blockMap["id"].(string); ok {
					if name, ok := blockMap["name"].(string); ok {
						toolCallId := strings.Replace(id, "toolu_", "call_", 1)

						var arguments string

						if input := blockMap["input"]; input != nil {
							if inputBytes, err := json.Marshal(input); err == nil {
								arguments = string(inputBytes)
							}
						}

						toolCall := map[string]interface{}{
							"id":   toolCallId,
							"type": "function",
							"function": map[string]interface{}{
								"name":      name,
								"arguments": arguments,
							},
						}
						toolCalls = append(toolCalls, toolCall)
					}
				}
			}
		}
	}

	if textContent.Len() > 0 {
		transformedMsg["content"] = textContent.String()
	} else {
		transformedMsg["content"] = ""
	}

	if len(toolCalls) > 0 {
		transformedMsg["tool_calls"] = toolCalls
	}

	return transformedMsg
}
