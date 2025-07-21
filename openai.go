package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// OpenAI format structures
type OpenAIResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []OpenAIChoice `json:"choices"`
	Usage             *OpenAIUsage   `json:"usage,omitempty"`
	SystemFingerprint *string        `json:"system_fingerprint,omitempty"`
	Error             *OpenAIError   `json:"error,omitempty"`
}

type OpenAIChoice struct {
	Index        int             `json:"index"`
	Message      *OpenAIMessage  `json:"message,omitempty"`
	Delta        *OpenAIMessage  `json:"delta,omitempty"`
	Logprobs     *OpenAILogprobs `json:"logprobs,omitempty"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

type OpenAIMessage struct {
	Role         string           `json:"role"`
	Content      *string          `json:"content,omitempty"`
	Name         *string          `json:"name,omitempty"`
	ToolCalls    []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallId   *string          `json:"tool_call_id,omitempty"`
	FunctionCall *OpenAIFunction  `json:"function_call,omitempty"`
}

type OpenAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

type OpenAIFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAILogprobs struct {
	Content []OpenAILogprobContent `json:"content,omitempty"`
}

type OpenAILogprobContent struct {
	Token       string             `json:"token"`
	Logprob     float64            `json:"logprob"`
	Bytes       []int              `json:"bytes,omitempty"`
	TopLogprobs []OpenAITopLogprob `json:"top_logprobs,omitempty"`
}

type OpenAITopLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes,omitempty"`
}

type OpenAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param,omitempty"`
	Code    *string `json:"code,omitempty"`
}

// Anthropic format structures
type AnthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []AnthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   *string            `json:"stop_reason,omitempty"`
	StopSequence *string            `json:"stop_sequence,omitempty"`
	Usage        *AnthropicUsage    `json:"usage,omitempty"`
	Error        *AnthropicError    `json:"error,omitempty"`
}

type AnthropicContent struct {
	Type      string                 `json:"type"`
	Text      *string                `json:"text,omitempty"`
	ID        *string                `json:"id,omitempty"`
	Name      *string                `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseId *string                `json:"tool_use_id,omitempty"`
	Content   interface{}            `json:"content,omitempty"`
	IsError   *bool                  `json:"is_error,omitempty"`
}

type AnthropicUsage struct {
	InputTokens            int  `json:"input_tokens"`
	OutputTokens           int  `json:"output_tokens"`
	CacheReadInputTokens   *int `json:"cache_read_input_tokens,omitempty"`
	CacheCreateInputTokens *int `json:"cache_create_input_tokens,omitempty"`
}

type AnthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ConvertOpenAIToAnthropic converts OpenAI format response to Anthropic format
func ConvertOpenAIToAnthropic(openaiData []byte) ([]byte, error) {
	var openaiResp OpenAIResponse
	if err := json.Unmarshal(openaiData, &openaiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenAI response: %w", err)
	}

	// Handle error responses
	if openaiResp.Error != nil {
		anthropicResp := AnthropicResponse{
			ID:    openaiResp.ID,
			Type:  "error",
			Model: openaiResp.Model,
			Error: &AnthropicError{
				Type:    mapOpenAIErrorType(openaiResp.Error.Type),
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

	anthropicResp := AnthropicResponse{
		ID:    openaiResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: openaiResp.Model,
	}

	// Convert content based on message type
	content, err := convertMessageContent(message)
	if err != nil {
		return nil, fmt.Errorf("failed to convert message content: %w", err)
	}
	anthropicResp.Content = content

	// Convert stop reason
	if choice.FinishReason != nil {
		anthropicResp.StopReason = convertStopReason(*choice.FinishReason)
	}

	// Convert usage
	if openaiResp.Usage != nil {
		usage := &AnthropicUsage{
			InputTokens:  openaiResp.Usage.PromptTokens,
			OutputTokens: openaiResp.Usage.CompletionTokens,
		}

		// Handle detailed token usage if available (requires extending OpenAIUsage struct)
		// This would need the OpenAIUsage struct to include prompt_tokens_details etc.
		anthropicResp.Usage = usage
	}

	return json.Marshal(anthropicResp)
}

// convertMessageContent converts OpenAI message content to Anthropic content blocks
func convertMessageContent(message *OpenAIMessage) ([]AnthropicContent, error) {
	var content []AnthropicContent

	// Handle regular text content
	if message.Content != nil && *message.Content != "" {
		content = append(content, AnthropicContent{
			Type: "text",
			Text: message.Content,
		})
	}

	// Handle tool calls (assistant making tool calls)
	if len(message.ToolCalls) > 0 {
		for _, toolCall := range message.ToolCalls {
			// Parse the arguments JSON string into a map
			var input map[string]interface{}
			if toolCall.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
					return nil, fmt.Errorf("failed to parse tool call arguments: %w", err)
				}
			}

			content = append(content, AnthropicContent{
				Type:  "tool_use",
				ID:    &toolCall.ID,
				Name:  &toolCall.Function.Name,
				Input: input,
			})
		}
	}

	// Handle tool results (tool responding back)
	if message.Role == "tool" && message.ToolCallId != nil {
		var toolContent interface{}

		// Try to parse content as JSON, fall back to string
		if message.Content != nil {
			var jsonContent interface{}
			if err := json.Unmarshal([]byte(*message.Content), &jsonContent); err == nil {
				toolContent = jsonContent
			} else {
				toolContent = *message.Content
			}
		}

		content = append(content, AnthropicContent{
			Type:      "tool_result",
			ToolUseId: message.ToolCallId,
			Content:   toolContent,
		})
	}

	// Handle legacy function calls (deprecated OpenAI format)
	if message.FunctionCall != nil {
		var input map[string]interface{}
		if message.FunctionCall.Arguments != "" {
			if err := json.Unmarshal([]byte(message.FunctionCall.Arguments), &input); err != nil {
				return nil, fmt.Errorf("failed to parse function call arguments: %w", err)
			}
		}

		// Generate a unique ID for legacy function calls
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

// convertStopReason maps OpenAI finish reasons to Anthropic stop reasons
func convertStopReason(openaiReason string) *string {
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

	// Default fallback
	defaultReason := "end_turn"
	return &defaultReason
}

// mapOpenAIErrorType maps OpenAI error types to Anthropic error types
func mapOpenAIErrorType(openaiType string) string {
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

	return "api_error" // Default fallback
}

// StreamState tracks the state of streaming conversion
type StreamState struct {
	MessageStartSent      bool
	ContentBlockStartSent bool
	MessageID             string
	Model                 string
	InitialUsage          map[string]interface{}
}

// ConvertOpenAIToAnthropicStream converts OpenAI streaming chunks to proper Anthropic SSE format
// Returns multiple SSE events as a byte slice in the format "event: type\ndata: {...}\n\n"
func ConvertOpenAIToAnthropicStream(openaiData []byte, state *StreamState) ([]byte, error) {
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
				messageStartEvent := createMessageStartEvent(state.MessageID, state.Model, rawChunk)
				events = append(events, formatSSEEvent("message_start", messageStartEvent)...)
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
					events = append(events, formatSSEEvent("content_block_start", contentBlockStartEvent)...)
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
					events = append(events, formatSSEEvent("content_block_delta", contentDeltaEvent)...)
				}

				// Handle tool calls (create tool_use content blocks)
				if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
					for _, toolCallInterface := range toolCalls {
						if toolCall, ok := toolCallInterface.(map[string]interface{}); ok {
							if function, ok := toolCall["function"].(map[string]interface{}); ok {
								// Parse arguments
								var input map[string]interface{}
								if args, ok := function["arguments"].(string); ok && args != "" {
									json.Unmarshal([]byte(args), &input)
								}

								toolUseEvent := map[string]interface{}{
									"type":  "content_block_start",
									"index": 0,
									"content_block": map[string]interface{}{
										"type":  "tool_use",
										"id":    toolCall["id"],
										"name":  function["name"],
										"input": input,
									},
								}
								events = append(events, formatSSEEvent("content_block_start", toolUseEvent)...)
							}
						}
					}
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
						events = append(events, formatSSEEvent("content_block_stop", contentStopEvent)...)
					}

					// Send message_delta with stop reason and final usage
					messageDeltaEvent := map[string]interface{}{
						"type": "message_delta",
						"delta": map[string]interface{}{
							"stop_reason":   convertStopReason(reason),
							"stop_sequence": nil,
						},
					}

					// Add final usage if present
					if usage, ok := rawChunk["usage"].(map[string]interface{}); ok {
						if completionTokens, ok := usage["completion_tokens"]; ok {
							messageDeltaEvent["usage"] = map[string]interface{}{
								"output_tokens": completionTokens,
							}
						}
					}

					events = append(events, formatSSEEvent("message_delta", messageDeltaEvent)...)

					// Send message_stop
					messageStopEvent := map[string]interface{}{
						"type": "message_stop",
					}
					events = append(events, formatSSEEvent("message_stop", messageStopEvent)...)
				}
			}
		}
	}

	return events, nil
}

// createMessageStartEvent creates the initial message_start event
func createMessageStartEvent(messageID, model string, firstChunk map[string]interface{}) map[string]interface{} {
	// Build usage object
	usage := map[string]interface{}{
		"input_tokens":  0,
		"output_tokens": 1,
	}

	// Extract usage from first chunk if available
	if chunkUsage, ok := firstChunk["usage"].(map[string]interface{}); ok {
		if promptTokens, ok := chunkUsage["prompt_tokens"]; ok {
			usage["input_tokens"] = promptTokens
		}

		// Handle detailed token information
		if promptDetails, ok := chunkUsage["prompt_tokens_details"].(map[string]interface{}); ok {
			if cachedTokens, ok := promptDetails["cached_tokens"]; ok {
				usage["cache_read_input_tokens"] = cachedTokens
			}
			if cacheCreationTokens, ok := promptDetails["cache_creation_tokens"]; ok {
				usage["cache_creation_input_tokens"] = cacheCreationTokens
			}
		}

		// Add service tier if present
		usage["service_tier"] = "standard" // Default, could be extracted from response
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

// formatSSEEvent formats an event into SSE format
func formatSSEEvent(eventType string, data map[string]interface{}) []byte {
	jsonData, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

// ConvertOpenAIStreamToAnthropicSSE is a higher-level function that manages state
// and converts a sequence of OpenAI streaming chunks to Anthropic SSE format
func ConvertOpenAIStreamToAnthropicSSE(openaiChunks [][]byte) ([]byte, error) {
	state := &StreamState{}
	var allEvents []byte

	for _, chunk := range openaiChunks {
		events, err := ConvertOpenAIToAnthropicStream(chunk, state)
		if err != nil {
			return nil, err
		}
		allEvents = append(allEvents, events...)
	}

	return allEvents, nil
}

// ConvertOpenAIToAnthropicWithFieldPreservation converts with enhanced field preservation
// This combines proper Anthropic format compliance with comprehensive field preservation
func ConvertOpenAIToAnthropicWithFieldPreservation(openaiData []byte, preserveUnknownFields bool) ([]byte, error) {
	var rawResp map[string]interface{}
	if err := json.Unmarshal(openaiData, &rawResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenAI response: %w", err)
	}

	// First do standard conversion
	standardResult, err := ConvertOpenAIToAnthropic(openaiData)
	if err != nil {
		return nil, err
	}

	if !preserveUnknownFields {
		return standardResult, nil
	}

	// Parse the standard result to enhance it
	var anthropicResp map[string]interface{}
	if err := json.Unmarshal(standardResult, &anthropicResp); err != nil {
		return nil, err
	}

	// Add preserved fields with prefixes
	for key, value := range rawResp {
		if !isStandardOpenAIField(key) {
			anthropicResp["openai_"+key] = value
		}
	}

	// Enhance usage with detailed token information
	if usage, ok := rawResp["usage"].(map[string]interface{}); ok {
		if anthropicUsage, ok := anthropicResp["usage"].(map[string]interface{}); ok {
			enhancedUsage := make(map[string]interface{})

			// Copy existing anthropic usage
			for k, v := range anthropicUsage {
				enhancedUsage[k] = v
			}

			// Add detailed token information
			if promptDetails, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
				if cachedTokens, ok := promptDetails["cached_tokens"]; ok {
					enhancedUsage["cache_read_input_tokens"] = cachedTokens
				}
				for key, value := range promptDetails {
					if key != "cached_tokens" {
						enhancedUsage["prompt_"+key] = value
					}
				}
			}

			if completionDetails, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
				for key, value := range completionDetails {
					enhancedUsage["completion_"+key] = value
				}
			}

			anthropicResp["usage"] = enhancedUsage
		}
	}

	return json.Marshal(anthropicResp)
}

// isStandardOpenAIField checks if a field is part of the standard OpenAI response format
func isStandardOpenAIField(field string) bool {
	standardFields := map[string]bool{
		"id": true, "object": true, "created": true, "model": true,
		"choices": true, "usage": true, "system_fingerprint": true, "error": true,
	}
	return standardFields[field]
}

// Example usage demonstrating all conversion functions
func ExampleUsage() {
	// Example OpenAI response with tool calls
	openaiResponse := []byte(`{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"created": 1677652288,
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_abc123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"location\": \"San Francisco\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 82,
			"completion_tokens": 17,
			"total_tokens": 99,
			"prompt_tokens_details": {
				"cached_tokens": 40
			}
		}
	}`)

	// Standard conversion
	anthropicResp, err := ConvertOpenAIToAnthropic(openaiResponse)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Enhanced conversion with field preservation
	enhancedResp, err := ConvertOpenAIToAnthropicWithFieldPreservation(openaiResponse, true)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("Standard Anthropic format:")
	prettyStandard, _ := PrettyPrintJSON(anthropicResp)
	fmt.Println(prettyStandard)

	fmt.Println("\nEnhanced format with field preservation:")
	prettyEnhanced, _ := PrettyPrintJSON(enhancedResp)
	fmt.Println(prettyEnhanced)
}

// Helper function to pretty print JSON for debugging
func PrettyPrintJSON(data []byte) (string, error) {
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", err
	}
	pretty, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return "", err
	}
	return string(pretty), nil
}
