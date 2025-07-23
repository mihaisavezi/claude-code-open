package providers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type GeminiProvider struct {
	name     string
	endpoint string
	apiKey   string
}

func NewGeminiProvider() *GeminiProvider {
	return &GeminiProvider{
		name: "gemini",
	}
}

func (p *GeminiProvider) Name() string {
	return p.name
}

func (p *GeminiProvider) SupportsStreaming() bool {
	return true
}

func (p *GeminiProvider) GetEndpoint() string {
	if p.endpoint == "" {
		return "https://generativelanguage.googleapis.com/v1beta/models"
	}

	return p.endpoint
}

func (p *GeminiProvider) SetAPIKey(key string) {
	p.apiKey = key
}

func (p *GeminiProvider) IsStreaming(headers map[string][]string) bool {
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

func (p *GeminiProvider) TransformRequest(request []byte) ([]byte, error) {
	// Gemini uses its own format, so we need to transform Anthropic to Gemini
	return p.transformAnthropicToGemini(request)
}

func (p *GeminiProvider) TransformResponse(response []byte) ([]byte, error) {
	// Transform Gemini response to Anthropic format
	return p.convertGeminiToAnthropic(response)
}

func (p *GeminiProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
	return p.convertGeminiToAnthropicStream(chunk, state)
}

// Gemini format structures
type geminiResponse struct {
	Candidates     []geminiCandidate     `json:"candidates,omitempty"`
	PromptFeedback *geminiPromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  *geminiUsageMetadata  `json:"usageMetadata,omitempty"`
	ModelVersion   string                `json:"modelVersion,omitempty"`
	ResponseID     string                `json:"responseId,omitempty"`
	Error          *geminiError          `json:"error,omitempty"`
}

type geminiCandidate struct {
	Content       *geminiContent       `json:"content,omitempty"`
	FinishReason  string               `json:"finishReason,omitempty"`
	SafetyRatings []geminiSafetyRating `json:"safetyRatings,omitempty"`
	TokenCount    int                  `json:"tokenCount,omitempty"`
	Index         int                  `json:"index,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts,omitempty"`
	Role  string       `json:"role,omitempty"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string      `json:"name"`
	Response interface{} `json:"response"`
}

type geminiPromptFeedback struct {
	BlockReason   string               `json:"blockReason,omitempty"`
	SafetyRatings []geminiSafetyRating `json:"safetyRatings,omitempty"`
}

type geminiSafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
	Blocked     bool   `json:"blocked,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount      int `json:"totalTokenCount,omitempty"`
}

type geminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (p *GeminiProvider) convertGeminiToAnthropic(geminiData []byte) ([]byte, error) {
	var geminiResp geminiResponse
	if err := json.Unmarshal(geminiData, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Gemini response: %w", err)
	}

	// Handle error responses
	if geminiResp.Error != nil {
		anthropicResp := anthropicResponse{
			ID:    geminiResp.ResponseID,
			Type:  "error",
			Model: geminiResp.ModelVersion,
			Error: &anthropicError{
				Type:    p.mapGeminiErrorType(geminiResp.Error.Status),
				Message: geminiResp.Error.Message,
			},
		}
		return json.Marshal(anthropicResp)
	}

	// Handle streaming vs non-streaming responses
	if len(geminiResp.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates in Gemini response")
	}

	candidate := geminiResp.Candidates[0]

	anthropicResp := anthropicResponse{
		ID:    geminiResp.ResponseID,
		Type:  "message",
		Role:  "assistant",
		Model: geminiResp.ModelVersion,
	}

	// Convert content
	content, err := p.convertGeminiContent(candidate.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to convert content: %w", err)
	}
	anthropicResp.Content = content

	// Convert stop reason
	if candidate.FinishReason != "" {
		anthropicResp.StopReason = p.convertStopReason(candidate.FinishReason)
	}

	// Convert usage
	if geminiResp.UsageMetadata != nil {
		usage := &anthropicUsage{
			InputTokens:  geminiResp.UsageMetadata.PromptTokenCount,
			OutputTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
		}
		anthropicResp.Usage = usage
	}

	return json.Marshal(anthropicResp)
}

func (p *GeminiProvider) convertGeminiContent(content *geminiContent) ([]anthropicContent, error) {
	if content == nil {
		// Return empty text block if no content
		emptyText := ""
		return []anthropicContent{{
			Type: "text",
			Text: &emptyText,
		}}, nil
	}

	var result []anthropicContent

	for _, part := range content.Parts {
		// Handle text content
		if part.Text != "" {
			result = append(result, anthropicContent{
				Type: "text",
				Text: &part.Text,
			})
		}

		// Handle function calls (tool use)
		if part.FunctionCall != nil {
			id := fmt.Sprintf("toolu_%d", time.Now().UnixNano())
			result = append(result, anthropicContent{
				Type:  "tool_use",
				ID:    &id,
				Name:  &part.FunctionCall.Name,
				Input: part.FunctionCall.Args,
			})
		}

		// Handle function responses (tool results)
		if part.FunctionResponse != nil {
			id := fmt.Sprintf("toolu_%s_%d", part.FunctionResponse.Name, time.Now().UnixNano())
			result = append(result, anthropicContent{
				Type:      "tool_result",
				ToolUseId: &id,
				Content:   part.FunctionResponse.Response,
			})
		}
	}

	// If no content was generated, add empty text block
	if len(result) == 0 {
		emptyText := ""
		result = append(result, anthropicContent{
			Type: "text",
			Text: &emptyText,
		})
	}

	return result, nil
}

func (p *GeminiProvider) convertStopReason(geminiReason string) *string {
	mapping := map[string]string{
		"STOP":                      "end_turn",
		"MAX_TOKENS":                "max_tokens",
		"SAFETY":                    "stop_sequence",
		"RECITATION":                "stop_sequence",
		"LANGUAGE":                  "stop_sequence",
		"OTHER":                     "end_turn",
		"BLOCKLIST":                 "stop_sequence",
		"PROHIBITED_CONTENT":        "stop_sequence",
		"SPII":                      "stop_sequence",
		"MALFORMED_FUNCTION_CALL":   "tool_use",
		"FINISH_REASON_UNSPECIFIED": "end_turn",
	}

	if anthropicReason, exists := mapping[geminiReason]; exists {
		return &anthropicReason
	}

	defaultReason := "end_turn"
	return &defaultReason
}

func (p *GeminiProvider) mapGeminiErrorType(geminiStatus string) string {
	mapping := map[string]string{
		"INVALID_ARGUMENT":   "invalid_request_error",
		"UNAUTHENTICATED":    "authentication_error",
		"PERMISSION_DENIED":  "permission_error",
		"NOT_FOUND":          "not_found_error",
		"RESOURCE_EXHAUSTED": "rate_limit_error",
		"INTERNAL":           "api_error",
		"UNAVAILABLE":        "overloaded_error",
		"DEADLINE_EXCEEDED":  "rate_limit_error",
	}

	if anthropicType, exists := mapping[geminiStatus]; exists {
		return anthropicType
	}

	return "api_error"
}

func (p *GeminiProvider) convertGeminiToAnthropicStream(geminiData []byte, state *StreamState) ([]byte, error) {
	var rawChunk map[string]interface{}
	if err := json.Unmarshal(geminiData, &rawChunk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Gemini streaming response: %w", err)
	}

	var events []byte

	// Store response ID and model from first chunk
	if responseID, ok := rawChunk["responseId"].(string); ok && state.MessageID == "" {
		state.MessageID = responseID
	}
	if modelVersion, ok := rawChunk["modelVersion"].(string); ok && state.Model == "" {
		state.Model = modelVersion
	}

	// Handle candidates array
	if candidates, ok := rawChunk["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if firstCandidate, ok := candidates[0].(map[string]interface{}); ok {

			// Send message_start event if not sent yet
			if !state.MessageStartSent {
				messageStartEvent := p.createMessageStartEvent(state.MessageID, state.Model, rawChunk)
				events = append(events, p.formatSSEEvent("message_start", messageStartEvent)...)
				state.MessageStartSent = true
			}

			// Handle content
			if content, ok := firstCandidate["content"].(map[string]interface{}); ok {
				// Initialize content blocks map if needed
				if state.ContentBlocks == nil {
					state.ContentBlocks = make(map[int]*ContentBlockState)
				}

				// Handle parts array
				if parts, ok := content["parts"].([]interface{}); ok {
					contentEvents := p.handleGeminiParts(parts, state)
					events = append(events, contentEvents...)
				}
			}

			// Handle finish_reason
			if finishReason, ok := firstCandidate["finishReason"]; ok && finishReason != nil {
				if reason, ok := finishReason.(string); ok {
					finishEvents := p.handleFinishReason(reason, rawChunk, state)
					events = append(events, finishEvents...)
				}
			}
		}
	}

	return events, nil
}

func (p *GeminiProvider) createMessageStartEvent(messageID, model string, firstChunk map[string]interface{}) map[string]interface{} {
	usage := map[string]interface{}{
		"input_tokens":  0,
		"output_tokens": 1,
	}

	if usageMetadata, ok := firstChunk["usageMetadata"].(map[string]interface{}); ok {
		if promptTokens, ok := usageMetadata["promptTokenCount"]; ok {
			usage["input_tokens"] = promptTokens
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

func (p *GeminiProvider) formatSSEEvent(eventType string, data map[string]interface{}) []byte {
	jsonData, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

// handleGeminiParts processes Gemini content parts for streaming
func (p *GeminiProvider) handleGeminiParts(parts []interface{}, state *StreamState) []byte {
	var events []byte

	for _, part := range parts {
		if partMap, ok := part.(map[string]interface{}); ok {
			// Handle text content
			if text, ok := partMap["text"].(string); ok && text != "" {
				textEvents := p.handleTextContent(text, state)
				events = append(events, textEvents...)
			}

			// Handle function calls
			if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
				functionEvents := p.handleFunctionCall(functionCall, state)
				events = append(events, functionEvents...)
			}
		}
	}

	return events
}

// handleTextContent processes text content streaming
func (p *GeminiProvider) handleTextContent(content string, state *StreamState) []byte {
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

// handleFunctionCall processes function call streaming
func (p *GeminiProvider) handleFunctionCall(functionCall map[string]interface{}, state *StreamState) []byte {
	var events []byte

	name, _ := functionCall["name"].(string)
	args, _ := functionCall["args"].(map[string]interface{})

	// Create new content block for tool use
	contentBlockIndex := len(state.ContentBlocks)
	toolCallID := fmt.Sprintf("toolu_gemini_%d", time.Now().UnixNano())

	state.ContentBlocks[contentBlockIndex] = &ContentBlockState{
		Type:       "tool_use",
		ToolCallID: toolCallID,
		ToolName:   name,
		Arguments:  "",
	}

	contentBlock := state.ContentBlocks[contentBlockIndex]

	// Send content_block_start event
	events = append(events, p.createToolBlockStartEvent(contentBlockIndex, contentBlock)...)
	contentBlock.StartSent = true

	// Send function arguments as input_json_delta if we have args
	if args != nil {
		argsJSON, err := json.Marshal(args)
		if err == nil {
			events = append(events, p.createInputDeltaEvent(contentBlockIndex, string(argsJSON))...)
		}
	}

	return events
}

// getOrCreateTextBlock gets or creates text content block at index 0
func (p *GeminiProvider) getOrCreateTextBlock(state *StreamState) int {
	textIndex := 0
	if _, exists := state.ContentBlocks[textIndex]; !exists {
		state.ContentBlocks[textIndex] = &ContentBlockState{
			Type: "text",
		}
	}
	return textIndex
}

// createTextBlockStartEvent creates content_block_start event for text
func (p *GeminiProvider) createTextBlockStartEvent(index int) []byte {
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
func (p *GeminiProvider) createTextDeltaEvent(index int, text string) []byte {
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

// createToolBlockStartEvent creates content_block_start event for tool use
func (p *GeminiProvider) createToolBlockStartEvent(index int, block *ContentBlockState) []byte {
	contentBlockStartEvent := map[string]interface{}{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]interface{}{
			"type":  "tool_use",
			"id":    block.ToolCallID,
			"name":  block.ToolName,
			"input": map[string]interface{}{},
		},
	}
	return p.formatSSEEvent("content_block_start", contentBlockStartEvent)
}

// createInputDeltaEvent creates input_json_delta SSE event
func (p *GeminiProvider) createInputDeltaEvent(index int, partialJSON string) []byte {
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

// handleFinishReason processes finish reasons and sends appropriate events
func (p *GeminiProvider) handleFinishReason(reason string, chunk map[string]interface{}, state *StreamState) []byte {
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
	if usageMetadata, ok := chunk["usageMetadata"].(map[string]interface{}); ok {
		usageData := p.convertUsage(usageMetadata)
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
func (p *GeminiProvider) convertUsage(usage map[string]interface{}) map[string]interface{} {
	anthropicUsage := make(map[string]interface{})

	// Map token fields
	if promptTokens, ok := usage["promptTokenCount"]; ok {
		anthropicUsage["input_tokens"] = promptTokens
	}
	if candidatesTokens, ok := usage["candidatesTokenCount"]; ok {
		anthropicUsage["output_tokens"] = candidatesTokens
	}

	return anthropicUsage
}

// transformAnthropicToGemini converts Anthropic/Claude format to Gemini format
func (p *GeminiProvider) transformAnthropicToGemini(requestBody []byte) ([]byte, error) {
	var anthropicReq map[string]interface{}
	if err := json.Unmarshal(requestBody, &anthropicReq); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Anthropic request: %w", err)
	}

	geminiReq := make(map[string]interface{})

	// Handle system message and convert messages to contents
	contents, err := p.convertAnthropicMessagesToGeminiContents(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}
	geminiReq["contents"] = contents

	// Convert generation config
	generationConfig := make(map[string]interface{})
	
	if maxTokens, ok := anthropicReq["max_tokens"].(float64); ok {
		generationConfig["maxOutputTokens"] = int(maxTokens)
	}
	
	if temperature, ok := anthropicReq["temperature"].(float64); ok {
		generationConfig["temperature"] = temperature
	}
	
	if topP, ok := anthropicReq["top_p"].(float64); ok {
		generationConfig["topP"] = topP
	}
	
	if topK, ok := anthropicReq["top_k"].(float64); ok {
		generationConfig["topK"] = int(topK)
	}

	if len(generationConfig) > 0 {
		geminiReq["generationConfig"] = generationConfig
	}

	// Convert tools
	if tools, ok := anthropicReq["tools"].([]interface{}); ok && len(tools) > 0 {
		geminiTools, err := p.convertAnthropicToolsToGemini(tools)
		if err != nil {
			return nil, fmt.Errorf("failed to convert tools: %w", err)
		}
		geminiReq["tools"] = geminiTools
	}

	// Convert safety settings if needed
	safetySettings := []map[string]interface{}{
		{
			"category":  "HARM_CATEGORY_HARASSMENT",
			"threshold": "BLOCK_NONE",
		},
		{
			"category":  "HARM_CATEGORY_HATE_SPEECH",
			"threshold": "BLOCK_NONE",
		},
		{
			"category":  "HARM_CATEGORY_SEXUALLY_EXPLICIT",
			"threshold": "BLOCK_NONE",
		},
		{
			"category":  "HARM_CATEGORY_DANGEROUS_CONTENT",
			"threshold": "BLOCK_NONE",
		},
	}
	geminiReq["safetySettings"] = safetySettings

	return json.Marshal(geminiReq)
}

// Helper methods for transformAnthropicToGemini
func (p *GeminiProvider) convertAnthropicMessagesToGeminiContents(anthropicReq map[string]interface{}) ([]interface{}, error) {
	var contents []interface{}

	// Handle system message first
	if systemContent, hasSystem := anthropicReq["system"]; hasSystem {
		if systemStr, ok := systemContent.(string); ok {
			systemContent := map[string]interface{}{
				"parts": []interface{}{
					map[string]interface{}{
						"text": systemStr,
					},
				},
				"role": "user",
			}
			contents = append(contents, systemContent)
		}
	}

	// Convert messages
	if messages, ok := anthropicReq["messages"].([]interface{}); ok {
		for _, message := range messages {
			if msgMap, ok := message.(map[string]interface{}); ok {
				geminiContent, err := p.convertAnthropicMessageToGemini(msgMap)
				if err != nil {
					return nil, err
				}
				if geminiContent != nil {
					contents = append(contents, geminiContent)
				}
			}
		}
	}

	return contents, nil
}

func (p *GeminiProvider) convertAnthropicMessageToGemini(message map[string]interface{}) (map[string]interface{}, error) {
	role, _ := message["role"].(string)
	content := message["content"]

	var parts []interface{}

	switch contentType := content.(type) {
	case string:
		// Simple text content
		parts = append(parts, map[string]interface{}{
			"text": contentType,
		})
	case []interface{}:
		// Array of content blocks
		for _, block := range contentType {
			if blockMap, ok := block.(map[string]interface{}); ok {
				part, err := p.convertContentBlockToGeminiPart(blockMap)
				if err != nil {
					return nil, err
				}
				if part != nil {
					parts = append(parts, part)
				}
			}
		}
	default:
		return nil, fmt.Errorf("unsupported content type: %T", content)
	}

	// Convert role
	geminiRole := "user"
	if role == "assistant" {
		geminiRole = "model"
	}

	return map[string]interface{}{
		"parts": parts,
		"role":  geminiRole,
	}, nil
}

func (p *GeminiProvider) convertContentBlockToGeminiPart(block map[string]interface{}) (map[string]interface{}, error) {
	blockType, _ := block["type"].(string)

	switch blockType {
	case "text":
		if text, ok := block["text"].(string); ok {
			return map[string]interface{}{
				"text": text,
			}, nil
		}
	case "tool_use":
		// Convert tool_use to function_call for Gemini
		if name, ok := block["name"].(string); ok {
			functionCall := map[string]interface{}{
				"name": name,
			}

			if input := block["input"]; input != nil {
				functionCall["args"] = input
			}

			return map[string]interface{}{
				"functionCall": functionCall,
			}, nil
		}
	case "tool_result":
		// Convert tool_result to function_response for Gemini
		if toolUseId, ok := block["tool_use_id"].(string); ok {
			// Extract content and ensure it's a structured object for protobuf compatibility
			var response interface{}
			if content := block["content"]; content != nil {
				if contentStr, ok := content.(string); ok {
					// Wrap string content in structured object format for protobuf compatibility
					response = map[string]interface{}{
						"content": contentStr,
					}
				} else {
					response = content
				}
			} else {
				response = map[string]interface{}{}
			}

			return map[string]interface{}{
				"functionResponse": map[string]interface{}{
					"name":     toolUseId, // Use tool_use_id as function name reference
					"response": response,   // Structured object instead of plain string
				},
			}, nil
		}
	}

	return nil, nil
}

func (p *GeminiProvider) convertAnthropicToolsToGemini(tools []interface{}) ([]interface{}, error) {
	var geminiTools []interface{}

	functionDeclarations := make([]interface{}, 0)

	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]interface{}); ok {
			functionDecl := map[string]interface{}{
				"name": toolMap["name"],
			}

			if description, ok := toolMap["description"]; ok {
				functionDecl["description"] = description
			}

			if inputSchema, ok := toolMap["input_schema"]; ok {
				functionDecl["parameters"] = inputSchema
			}

			functionDeclarations = append(functionDeclarations, functionDecl)
		}
	}

	if len(functionDeclarations) > 0 {
		geminiTool := map[string]interface{}{
			"functionDeclarations": functionDeclarations,
		}
		geminiTools = append(geminiTools, geminiTool)
	}

	return geminiTools, nil
}
