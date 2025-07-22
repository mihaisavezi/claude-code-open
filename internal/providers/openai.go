package providers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type OpenAIProvider struct {
	name     string
	endpoint string
	apiKey   string
}

func NewOpenAIProvider() *OpenAIProvider {
	return &OpenAIProvider{
		name: "openai",
	}
}

func (p *OpenAIProvider) Name() string {
	return p.name
}

func (p *OpenAIProvider) SupportsStreaming() bool {
	return true
}

func (p *OpenAIProvider) GetEndpoint() string {
	if p.endpoint == "" {
		p.endpoint = "https://api.openai.com/v1/chat/completions"
	}

	return p.endpoint
}

func (p *OpenAIProvider) SetAPIKey(key string) {
	p.apiKey = key
}

func (p *OpenAIProvider) IsStreaming(headers map[string][]string) bool {
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

func (p *OpenAIProvider) Transform(request []byte) ([]byte, error) {
	return p.convertOpenAIToAnthropic(request)
}

func (p *OpenAIProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
	return p.convertOpenAIToAnthropicStream(chunk, state)
}

// OpenAI format structures
type openAIResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []openAIChoice `json:"choices"`
	Usage             *openAIUsage   `json:"usage,omitempty"`
	SystemFingerprint *string        `json:"system_fingerprint,omitempty"`
	Error             *openAIError   `json:"error,omitempty"`
}

type openAIChoice struct {
	Index        int            `json:"index"`
	Message      *openAIMessage `json:"message,omitempty"`
	Delta        *openAIMessage `json:"delta,omitempty"`
	Logprobs     interface{}    `json:"logprobs,omitempty"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

type openAIMessage struct {
	Role         string           `json:"role"`
	Content      *string          `json:"content,omitempty"`
	Name         *string          `json:"name,omitempty"`
	ToolCalls    []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallId   *string          `json:"tool_call_id,omitempty"`
	FunctionCall *openAIFunction  `json:"function_call,omitempty"`
}

type openAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param,omitempty"`
	Code    *string `json:"code,omitempty"`
}

// Anthropic format structures
type anthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []anthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   *string            `json:"stop_reason,omitempty"`
	StopSequence *string            `json:"stop_sequence,omitempty"`
	Usage        *anthropicUsage    `json:"usage,omitempty"`
	Error        *anthropicError    `json:"error,omitempty"`
}

type anthropicContent struct {
	Type      string                 `json:"type"`
	Text      *string                `json:"text,omitempty"`
	ID        *string                `json:"id,omitempty"`
	Name      *string                `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseId *string                `json:"tool_use_id,omitempty"`
	Content   interface{}            `json:"content,omitempty"`
	IsError   *bool                  `json:"is_error,omitempty"`
}

type anthropicUsage struct {
	InputTokens            int  `json:"input_tokens"`
	OutputTokens           int  `json:"output_tokens"`
	CacheReadInputTokens   *int `json:"cache_read_input_tokens,omitempty"`
	CacheCreateInputTokens *int `json:"cache_create_input_tokens,omitempty"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (p *OpenAIProvider) convertOpenAIToAnthropic(openaiData []byte) ([]byte, error) {
	var openaiResp openAIResponse
	if err := json.Unmarshal(openaiData, &openaiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenAI response: %w", err)
	}

	// Handle error responses
	if openaiResp.Error != nil {
		anthropicResp := anthropicResponse{
			ID:    openaiResp.ID,
			Type:  "error",
			Model: openaiResp.Model,
			Error: &anthropicError{
				Type:    p.mapOpenAIErrorType(openaiResp.Error.Type),
				Message: openaiResp.Error.Message,
			},
		}
		return json.Marshal(anthropicResp)
	}

	// Handle streaming vs non-streaming responses
	if len(openaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in OpenAI response")
	}

	choice := openaiResp.Choices[0]
	message := choice.Message
	if message == nil {
		message = choice.Delta // Handle streaming responses
	}

	if message == nil {
		return nil, fmt.Errorf("no message content in choice")
	}

	anthropicResp := anthropicResponse{
		ID:    openaiResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: openaiResp.Model,
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
	if openaiResp.Usage != nil {
		usage := &anthropicUsage{
			InputTokens:  openaiResp.Usage.PromptTokens,
			OutputTokens: openaiResp.Usage.CompletionTokens,
		}
		anthropicResp.Usage = usage
	}

	return json.Marshal(anthropicResp)
}

func (p *OpenAIProvider) convertMessageContent(message *openAIMessage) ([]anthropicContent, error) {
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

func (p *OpenAIProvider) convertStopReason(openaiReason string) *string {
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

func (p *OpenAIProvider) mapOpenAIErrorType(openaiType string) string {
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

	if anthropicType, exists := mapping[openaiType]; exists {
		return anthropicType
	}

	return "api_error"
}

func (p *OpenAIProvider) convertOpenAIToAnthropicStream(openaiData []byte, state *StreamState) ([]byte, error) {
	var rawChunk map[string]interface{}
	if err := json.Unmarshal(openaiData, &rawChunk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenAI streaming response: %w", err)
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

func (p *OpenAIProvider) createMessageStartEvent(messageID, model string, firstChunk map[string]interface{}) map[string]interface{} {
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

func (p *OpenAIProvider) formatSSEEvent(eventType string, data map[string]interface{}) []byte {
	jsonData, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

// handleTextContent processes text content streaming
func (p *OpenAIProvider) handleTextContent(content string, state *StreamState) []byte {
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
func (p *OpenAIProvider) handleToolCalls(toolCalls []interface{}, state *StreamState) []byte {
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
func (p *OpenAIProvider) handleSingleToolCall(toolCall map[string]interface{}, state *StreamState) []byte {
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

// OpenAIToolCallData holds parsed tool call information for OpenAI provider
type OpenAIToolCallData struct {
	Index        int
	HasIndex     bool
	ID           string
	FunctionName string
	Arguments    string
}

// parseToolCallData extracts tool call information from OpenAI chunk
func (p *OpenAIProvider) parseToolCallData(toolCall map[string]interface{}) OpenAIToolCallData {
	data := OpenAIToolCallData{}

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
func (p *OpenAIProvider) findOrCreateContentBlock(data OpenAIToolCallData, state *StreamState) int {
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
func (p *OpenAIProvider) updateContentBlock(block *ContentBlockState, data OpenAIToolCallData) {
	if data.FunctionName != "" {
		block.ToolName = data.FunctionName
	}
}

// shouldSendStartEvent determines if content_block_start event should be sent
func (p *OpenAIProvider) shouldSendStartEvent(block *ContentBlockState) bool {
	return block.ToolCallID != "" && block.ToolName != ""
}

// createContentBlockStartEvent creates content_block_start SSE event
func (p *OpenAIProvider) createContentBlockStartEvent(index int, block *ContentBlockState) []byte {
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

// convertToolCallID converts OpenAI tool call ID to Claude format
func (p *OpenAIProvider) convertToolCallID(toolCallID string) string {
	if strings.HasPrefix(toolCallID, "toolu_") {
		return toolCallID
	}
	if strings.HasPrefix(toolCallID, "call_") {
		return "toolu_" + strings.TrimPrefix(toolCallID, "call_")
	}
	return "toolu_" + toolCallID
}

// calculateArgumentsDelta calculates the incremental part of arguments
func (p *OpenAIProvider) calculateArgumentsDelta(newArgs, oldArgs string) string {
	// Check if arguments are incremental (common case)
	if len(newArgs) > len(oldArgs) && strings.HasPrefix(newArgs, oldArgs) {
		return newArgs[len(oldArgs):] // Extract new part
	}
	// Non-incremental case - return entire new arguments
	return newArgs
}

// createInputDeltaEvent creates input_json_delta SSE event
func (p *OpenAIProvider) createInputDeltaEvent(index int, partialJSON string) []byte {
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
func (p *OpenAIProvider) getOrCreateTextBlock(state *StreamState) int {
	textIndex := 0
	if _, exists := state.ContentBlocks[textIndex]; !exists {
		state.ContentBlocks[textIndex] = &ContentBlockState{
			Type: "text",
		}
	}
	return textIndex
}

// createTextBlockStartEvent creates content_block_start event for text
func (p *OpenAIProvider) createTextBlockStartEvent(index int) []byte {
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
func (p *OpenAIProvider) createTextDeltaEvent(index int, text string) []byte {
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
func (p *OpenAIProvider) handleFinishReason(reason string, chunk map[string]interface{}, state *StreamState) []byte {
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
func (p *OpenAIProvider) convertUsage(usage map[string]interface{}) map[string]interface{} {
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
