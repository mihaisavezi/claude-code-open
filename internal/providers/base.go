package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Common role and content type constants
	RoleAssistant      = "assistant"
	RoleUser           = "user"
	ContentTypeText    = "text"
	ContentTypeToolUse = "tool_use"

	// Stop reason constants
	StopReasonEndTurn = "end_turn"

	// Content types
	ContentTypeEventStream  = "text/event-stream"
	TransferEncodingChunked = "chunked"

	// Message types
	MessageTypeToolResult = "tool_result"
	MessageTypeAPIError   = "api_error"
)

// BaseProvider provides common functionality for all providers
type BaseProvider struct {
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
func FormatSSEEvent(eventType string, data any) []byte {
	jsonData, err := json.Marshal(data)
	if err != nil {
		// Return a basic error event if marshalling fails
		return []byte("event: error\ndata: {\"error\":\"failed to marshal data\"}\n\n")
	}

	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

// MapTokenUsage maps token usage from source format to Anthropic format
func MapTokenUsage(sourceUsage map[string]any, sourceMapping TokenMapping) map[string]any {
	anthropicUsage := make(map[string]any)

	// Map basic token fields
	if promptTokens, ok := sourceUsage[sourceMapping.InputTokens]; ok {
		anthropicUsage[AnthropicTokenMapping.InputTokens] = promptTokens
	}

	if completionTokens, ok := sourceUsage[sourceMapping.OutputTokens]; ok {
		anthropicUsage[AnthropicTokenMapping.OutputTokens] = completionTokens
	}

	// Handle detailed token information
	if promptDetails, ok := sourceUsage["prompt_tokens_details"].(map[string]any); ok {
		if cachedTokens, ok := promptDetails[sourceMapping.CacheReadInputTokens]; ok {
			anthropicUsage[AnthropicTokenMapping.CacheReadInputTokens] = cachedTokens
		}

		if cacheCreationTokens, ok := promptDetails[sourceMapping.CacheCreateInputTokens]; ok {
			anthropicUsage[AnthropicTokenMapping.CacheCreateInputTokens] = cacheCreationTokens
		}
	}

	if completionDetails, ok := sourceUsage["completion_tokens_details"].(map[string]any); ok {
		for key, value := range completionDetails {
			anthropicUsage["completion_"+key] = value
		}
	}

	return anthropicUsage
}

// ConvertStopReason converts various stop reason formats to Anthropic format
func ConvertStopReason(reason string) *string {
	mapping := map[string]string{
		"stop":           StopReasonEndTurn,
		"length":         "max_tokens",
		"tool_calls":     "tool_use",
		"function_call":  "tool_use",
		"content_filter": "stop_sequence",
		"null":           StopReasonEndTurn,
		"":               StopReasonEndTurn,
	}

	if anthropicReason, exists := mapping[reason]; exists {
		return &anthropicReason
	}

	defaultReason := StopReasonEndTurn

	return &defaultReason
}

// RemoveFieldsRecursively removes specified fields from nested JSON structures
func RemoveFieldsRecursively(data any, fieldsToRemove []string) any {
	switch v := data.(type) {
	case map[string]any:
		result := make(map[string]any)

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
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = RemoveFieldsRecursively(item, fieldsToRemove)
		}

		return result
	default:
		return v
	}
}

// CreateAnthropicContent creates Anthropic content blocks from text
func CreateAnthropicContent(text string) []map[string]any {
	if text == "" {
		text = ""
	}

	return []map[string]any{
		{
			"type": "text",
			"text": text,
		},
	}
}

// CreateMessageStartEvent creates a standard Anthropic message_start event
func CreateMessageStartEvent(messageID, model string, usage map[string]any) map[string]any {
	if usage == nil {
		usage = map[string]any{
			"input_tokens":  0,
			"output_tokens": 1,
		}
	}

	return map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
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

// ProviderInterface defines methods needed for HandleFinishReason
type ProviderInterface interface {
	formatSSEEvent(eventType string, data map[string]any) []byte
	convertStopReason(reason string) *string
}

// HandleFinishReason processes finish reasons and sends appropriate events
func HandleFinishReason(p ProviderInterface, reason string, chunk map[string]any, state *StreamState, getUsage func(map[string]any) map[string]any) []byte {
	var events []byte

	// Send content_block_stop for all active content blocks
	for index, contentBlock := range state.ContentBlocks {
		if contentBlock.StartSent && !contentBlock.StopSent {
			contentStopEvent := map[string]any{
				"type":  "content_block_stop",
				"index": index,
			}
			events = append(events, p.formatSSEEvent("content_block_stop", contentStopEvent)...)
			contentBlock.StopSent = true
		}
	}

	// Send message_delta with stop reason
	messageDeltaEvent := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   p.convertStopReason(reason),
			"stop_sequence": nil,
		},
	}

	// Add usage if present - use the provided function to extract usage
	if getUsage != nil {
		usageData := getUsage(chunk)
		if len(usageData) > 0 {
			messageDeltaEvent["usage"] = usageData
		}
	}

	events = append(events, p.formatSSEEvent("message_delta", messageDeltaEvent)...)

	// Send message_stop
	messageStopEvent := map[string]any{
		"type": "message_stop",
	}
	events = append(events, p.formatSSEEvent("message_stop", messageStopEvent)...)

	return events
}

// StreamProviderInterface extends ProviderInterface for stream processing
type StreamProviderInterface interface {
	formatSSEEvent(eventType string, data map[string]any) []byte
	convertStopReason(reason string) *string
	createMessageStartEvent(messageID, model string, chunk map[string]any) map[string]any
	handleToolCalls(toolCalls []any, state *StreamState) []byte
	handleTextContent(content string, state *StreamState) []byte
	handleFinishReason(reason string, chunk map[string]any, state *StreamState) []byte
}

// ConvertOpenAIStyleToAnthropicStream handles OpenAI-style streaming responses (OpenAI/Nvidia)
func ConvertOpenAIStyleToAnthropicStream(data []byte, state *StreamState, provider StreamProviderInterface, errorPrefix string) ([]byte, error) {
	var rawChunk map[string]any
	if err := json.Unmarshal(data, &rawChunk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s streaming response: %w", errorPrefix, err)
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
	if choices, ok := rawChunk["choices"].([]any); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]any); ok {
			// Send message_start event if not sent yet
			if !state.MessageStartSent {
				messageStartEvent := provider.createMessageStartEvent(state.MessageID, state.Model, rawChunk)
				events = append(events, provider.formatSSEEvent("message_start", messageStartEvent)...)
				state.MessageStartSent = true
			}

			// Handle delta content
			if delta, ok := firstChoice["delta"].(map[string]any); ok {
				// Initialize content blocks map if needed
				if state.ContentBlocks == nil {
					state.ContentBlocks = make(map[int]*ContentBlockState)
				}

				// Check if we have tool calls - if so, prioritize them over text content
				if toolCalls, ok := delta["tool_calls"].([]any); ok {
					toolEvents := provider.handleToolCalls(toolCalls, state)
					events = append(events, toolEvents...)
				} else if content, ok := delta["content"].(string); ok && content != "" {
					// Only handle text content if no tool calls are present
					textEvents := provider.handleTextContent(content, state)
					events = append(events, textEvents...)
				}
			}

			// Handle finish_reason
			if finishReason, ok := firstChoice["finish_reason"]; ok && finishReason != nil {
				if reason, ok := finishReason.(string); ok {
					finishEvents := provider.handleFinishReason(reason, rawChunk, state)
					events = append(events, finishEvents...)
				}
			}
		}
	}

	return events, nil
}

// TransformAssistantMessage converts assistant messages with tool_use to tool_calls format
func TransformAssistantMessage(msgMap map[string]any, content []any) map[string]any {
	transformedMsg := make(map[string]any)
	for k, v := range msgMap {
		transformedMsg[k] = v
	}

	var (
		textContent strings.Builder
		toolCalls   []any
	)

	for _, block := range content {
		if blockMap, ok := block.(map[string]any); ok {
			blockType, ok := blockMap["type"].(string)
			if !ok {
				continue // Skip invalid block types
			}

			switch blockType {
			case "text":
				if text, ok := blockMap["text"].(string); ok {
					textContent.WriteString(text)
				}
			case ContentTypeToolUse:
				if id, ok := blockMap["id"].(string); ok {
					if name, ok := blockMap["name"].(string); ok {
						toolCallID := strings.Replace(id, "toolu_", "call_", 1)

						var arguments string

						if input := blockMap["input"]; input != nil {
							if inputBytes, err := json.Marshal(input); err == nil {
								arguments = string(inputBytes)
							}
						}

						toolCall := map[string]any{
							"id":   toolCallID,
							"type": "function",
							"function": map[string]any{
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

// TransformTools converts tools from Claude format to OpenAI format
func TransformTools(tools []any) ([]any, error) {
	transformedTools := make([]any, 0, len(tools))

	for _, tool := range tools {
		toolMap, ok := tool.(map[string]any)
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
			openAITool := map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name,
				},
			}

			function, ok := openAITool["function"].(map[string]any)
			if !ok {
				continue // Skip invalid function structure
			}

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

// OpenAITransformerInterface defines methods that OpenAI-compatible providers need
type OpenAITransformerInterface interface {
	removeAnthropicSpecificFields(request map[string]any) map[string]any
	transformMessages(messages []any) []any
	transformTools(tools []any) ([]any, error)
}

// TransformAnthropicToOpenAI is a shared transformation function for OpenAI-compatible providers
func TransformAnthropicToOpenAI(anthropicRequest []byte, transformer OpenAITransformerInterface) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(anthropicRequest, &request); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Anthropic request: %w", err)
	}

	// Remove Anthropic-specific fields that OpenAI doesn't support
	cleanedRequest := transformer.removeAnthropicSpecificFields(request)

	// Handle system parameter - convert it to a system message in messages array
	if systemContent, hasSystem := cleanedRequest["system"]; hasSystem {
		if messages, ok := cleanedRequest["messages"].([]any); ok {
			// Create system message
			systemMessage := map[string]any{
				"role":    "system",
				"content": systemContent,
			}

			// Prepend system message to messages array
			newMessages := append([]any{systemMessage}, messages...)
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
	if messages, ok := cleanedRequest["messages"].([]any); ok {
		cleanedRequest["messages"] = transformer.transformMessages(messages)
	}

	// Transform tools from Claude format to OpenAI format if present
	if tools, ok := cleanedRequest["tools"].([]any); ok {
		transformedTools, err := transformer.transformTools(tools)
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

// Common response structures
type CommonResponse struct {
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Error   *CommonError           `json:"error,omitempty"`
	Choices []CommonChoice         `json:"choices,omitempty"`
	Usage   *CommonUsage           `json:"usage,omitempty"`
}

type CommonError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type CommonChoice struct {
	Message      *CommonMessage `json:"message,omitempty"`
	Delta        *CommonMessage `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

type CommonMessage struct {
	Role         string                 `json:"role,omitempty"`
	Content      *string                `json:"content,omitempty"`
	ToolCalls    []CommonToolCall       `json:"tool_calls,omitempty"`
	ToolCallID   *string                `json:"tool_call_id,omitempty"`
	FunctionCall *CommonFunctionCall    `json:"function_call,omitempty"`
}

type CommonToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function CommonFunctionCall  `json:"function"`
}

type CommonFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type CommonUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Anthropic response structures
type AnthropicResponse struct {
	ID         string              `json:"id"`
	Type       string              `json:"type"`
	Role       string              `json:"role,omitempty"`
	Model      string              `json:"model"`
	Content    []AnthropicContent  `json:"content,omitempty"`
	StopReason *string             `json:"stop_reason,omitempty"`
	Usage      *AnthropicUsage     `json:"usage,omitempty"`
	Error      *AnthropicError     `json:"error,omitempty"`
}

type AnthropicContent struct {
	Type      string      `json:"type"`
	Text      *string     `json:"text,omitempty"`
	ID        *string     `json:"id,omitempty"`
	Name      *string     `json:"name,omitempty"`
	Input     any `json:"input,omitempty"`
	ToolUseID *string     `json:"tool_use_id,omitempty"`
	Content   any `json:"content,omitempty"`
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type AnthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ConvertToAnthropic converts OpenAI-style response to Anthropic format
func ConvertToAnthropic(responseData []byte, errorTypeMapper func(string) string, toolCallIDConverter func(string) string) ([]byte, error) {
	var commonResp CommonResponse
	if err := json.Unmarshal(responseData, &commonResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Handle error responses
	if commonResp.Error != nil {
		anthropicResp := AnthropicResponse{
			ID:    commonResp.ID,
			Type:  "error",
			Model: commonResp.Model,
			Error: &AnthropicError{
				Type:    errorTypeMapper(commonResp.Error.Type),
				Message: commonResp.Error.Message,
			},
		}

		return json.Marshal(anthropicResp)
	}

	// Handle streaming vs non-streaming responses
	if len(commonResp.Choices) == 0 {
		return nil, errors.New("no choices in response")
	}

	choice := commonResp.Choices[0]

	message := choice.Message
	if message == nil {
		message = choice.Delta // Handle streaming responses
	}

	if message == nil {
		return nil, errors.New("no message content in choice")
	}

	anthropicResp := AnthropicResponse{
		ID:    commonResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: commonResp.Model,
	}

	// Convert content based on message type
	content, err := convertMessageContent(message, toolCallIDConverter)
	if err != nil {
		return nil, fmt.Errorf("failed to convert message content: %w", err)
	}

	anthropicResp.Content = content

	// Convert stop reason
	if choice.FinishReason != nil {
		stopReason := ConvertStopReason(*choice.FinishReason)
		anthropicResp.StopReason = stopReason
	}

	// Convert usage
	if commonResp.Usage != nil {
		usage := &AnthropicUsage{
			InputTokens:  commonResp.Usage.PromptTokens,
			OutputTokens: commonResp.Usage.CompletionTokens,
		}
		anthropicResp.Usage = usage
	}

	return json.Marshal(anthropicResp)
}

func convertMessageContent(message *CommonMessage, toolCallIDConverter func(string) string) ([]AnthropicContent, error) {
	var content []AnthropicContent

	// Handle regular text content
	if message.Content != nil && *message.Content != "" {
		content = append(content, AnthropicContent{
			Type: "text",
			Text: message.Content,
		})
	}

	// Handle tool calls
	if len(message.ToolCalls) > 0 {
		for _, toolCall := range message.ToolCalls {
			var input map[string]any
			if toolCall.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
					return nil, fmt.Errorf("failed to parse tool call arguments: %w", err)
				}
			}

			claudeID := toolCallIDConverter(toolCall.ID)
			content = append(content, AnthropicContent{
				Type:  "tool_use",
				ID:    &claudeID,
				Name:  &toolCall.Function.Name,
				Input: input,
			})
		}
	}

	// Handle tool results
	if message.Role == "tool" && message.ToolCallID != nil {
		var toolContent any

		if message.Content != nil {
			var jsonContent any
			if err := json.Unmarshal([]byte(*message.Content), &jsonContent); err == nil {
				toolContent = jsonContent
			} else {
				toolContent = *message.Content
			}
		}

		claudeToolID := toolCallIDConverter(*message.ToolCallID)
		content = append(content, AnthropicContent{
			Type:      "tool_result",
			ToolUseID: &claudeToolID,
			Content:   toolContent,
		})
	}

	// Handle legacy function calls
	if message.FunctionCall != nil {
		var input map[string]any
		if message.FunctionCall.Arguments != "" {
			if err := json.Unmarshal([]byte(message.FunctionCall.Arguments), &input); err != nil {
				return nil, fmt.Errorf("failed to parse function call arguments: %w", err)
			}
		}

		id := fmt.Sprintf("func_%d", time.Now().UnixNano())
		content = append(content, AnthropicContent{
			Type:  "tool_use",
			ID:    &id,
			Name:  &message.FunctionCall.Name,
			Input: input,
		})
	}

	// If no content was generated, add empty text block
	if len(content) == 0 {
		emptyText := ""
		content = append(content, AnthropicContent{
			Type: "text",
			Text: &emptyText,
		})
	}

	return content, nil
}
