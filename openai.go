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
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
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
		anthropicResp.Usage = &AnthropicUsage{
			InputTokens:  openaiResp.Usage.PromptTokens,
			OutputTokens: openaiResp.Usage.CompletionTokens,
		}
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

// ConvertOpenAIToAnthropicStream handles streaming responses
func ConvertOpenAIToAnthropicStream(openaiData []byte) ([]byte, error) {
	var openaiResp OpenAIResponse
	if err := json.Unmarshal(openaiData, &openaiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenAI streaming response: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in OpenAI streaming response")
	}

	choice := openaiResp.Choices[0]
	delta := choice.Delta

	if delta == nil {
		return nil, fmt.Errorf("no delta in streaming choice")
	}

	// Create streaming event format for Anthropic
	event := map[string]interface{}{
		"type": "content_block_delta",
	}

	if delta.Content != nil && *delta.Content != "" {
		event["delta"] = map[string]interface{}{
			"type": "text_delta",
			"text": *delta.Content,
		}
	}

	if len(delta.ToolCalls) > 0 {
		// Handle tool call deltas
		for _, toolCall := range delta.ToolCalls {
			event["delta"] = map[string]interface{}{
				"type":        "tool_use_delta",
				"tool_use_id": toolCall.ID,
				"name":        toolCall.Function.Name,
				"input":       toolCall.Function.Arguments,
			}
		}
	}

	if choice.FinishReason != nil {
		event["type"] = "message_stop"
		event["stop_reason"] = convertStopReason(*choice.FinishReason)
	}

	return json.Marshal(event)
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
