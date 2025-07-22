package handlers

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/pkoukk/tiktoken-go"

	"github.com/Davincible/claude-code-open/internal/config"
	"github.com/Davincible/claude-code-open/internal/providers"
)

type ProxyHandler struct {
	config   *config.Manager
	registry *providers.Registry
	logger   *slog.Logger
}

func NewProxyHandler(config *config.Manager, registry *providers.Registry, logger *slog.Logger) *ProxyHandler {
	return &ProxyHandler{
		config:   config,
		registry: registry,
		logger:   logger,
	}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := h.config.Get()

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.httpError(w, http.StatusBadRequest, "failed to read request body: %v", err)
		return
	}

	// Count input tokens
	inputTokens := h.countInputTokens(string(body))

	// Select model and transform request body
	transformedBody, modelName := h.selectModel(body, inputTokens, &cfg.Router)

	// Find provider for the model
	provider, providerConfig, err := h.findProvider(modelName, cfg)
	if err != nil {
		h.httpError(w, http.StatusBadRequest, "provider not found: %v", err)
		return
	}

	// Transform from Anthropic format to provider format
	finalBody, err := h.transformRequestToProviderFormat(transformedBody, provider.Name())
	if err != nil {
		h.logger.Warn("Request transformation failed, using original", "error", err)
		finalBody = transformedBody
	}

	// Debug: Log request being sent to provider (truncated for readability)
	if len(finalBody) > 500 {
		h.logger.Debug("Sending request to provider", "provider", provider.Name(), "body_preview", string(finalBody[:500])+"...")
	} else {
		h.logger.Debug("Sending request to provider", "provider", provider.Name(), "body", string(finalBody))
	}

	// Build final endpoint URL (handle special cases like Gemini)
	finalURL := h.buildEndpointURL(provider, providerConfig.APIBase, modelName)
	
	// Create upstream request
	req, err := http.NewRequest(r.Method, finalURL, strings.NewReader(string(finalBody)))
	if err != nil {
		h.httpError(w, http.StatusInternalServerError, "failed to create upstream request: %v", err)
		return
	}

	// Copy headers and set auth
	req.Header = r.Header.Clone()
	if providerConfig.APIKey != "" {
		h.setAuthHeader(req, provider, providerConfig.APIKey)
	}

	h.logger.Info("Proxying request",
		"provider", provider.Name(),
		"model", modelName,
		"url", finalURL,
		"input_tokens", inputTokens,
	)

	// Make upstream request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.httpError(w, http.StatusBadGateway, "upstream request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	// Handle response based on streaming
	if provider.IsStreaming(resp.Header) {
		h.handleStreamingResponse(w, resp, provider, inputTokens)
	} else {
		h.handleResponse(w, resp, provider, inputTokens)
	}
}

func (h *ProxyHandler) handleStreamingResponse(w http.ResponseWriter, resp *http.Response, provider providers.Provider, inputTokens int) {
	// Handle decompression
	bodyReader, err := h.decompressReader(resp)
	if err != nil {
		h.httpError(w, http.StatusBadGateway, "decompression error: %v", err)
		return
	}
	if closer, ok := bodyReader.(io.Closer); ok {
		defer closer.Close()
	}

	// Set streaming headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Copy relevant headers
	h.copyHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)

	// For error responses, capture and print the body
	var errorBodyLines []string
	captureError := resp.StatusCode != http.StatusOK

	// Create scanner and state
	scanner := bufio.NewScanner(bodyReader)
	state := &providers.StreamState{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Capture error response body
		if captureError && line != "" {
			errorBodyLines = append(errorBodyLines, line)
		}

		// Skip empty lines and comments
		if line == "" {
			fmt.Fprint(w, "\n")
			h.flushResponse(w)
			continue
		}

		if strings.HasPrefix(line, ": ") {
			continue // Skip SSE comments
		}

		// Handle [DONE] message
		if line == "data: [DONE]" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			h.flushResponse(w)
			break
		}

		// Process data lines
		if strings.HasPrefix(line, "data: ") {
			// For error responses, forward data as-is without transformation
			if captureError {
				fmt.Fprintf(w, "%s\n\n", line)
			} else {
				jsonData := strings.TrimPrefix(line, "data: ")

				// Transform chunk through provider for successful responses
				events, err := provider.TransformStream([]byte(jsonData), state)
				if err != nil {
					h.logger.Error("Stream transformation error", "error", err)
					// Send original chunk on error
					fmt.Fprintf(w, "%s\n\n", line)
				} else {
					if len(events) > 0 {
						w.Write(events)
					}
				}
			}

			h.flushResponse(w)
		} else {
			// Pass through other SSE lines
			fmt.Fprintf(w, "%s\n", line)
			h.flushResponse(w)
		}
	}

	if err := scanner.Err(); err != nil {
		h.logger.Error("Stream scanning error", "error", err)
	}

	// Print captured error response body
	if captureError && len(errorBodyLines) > 0 {
		fmt.Printf("\nUpstream streaming error response body:\n%s\n", strings.Join(errorBodyLines, "\n"))
	}

	h.logger.Info("Completed streaming response",
		"status", resp.StatusCode,
		"input_tokens", inputTokens,
	)
}

func (h *ProxyHandler) handleResponse(w http.ResponseWriter, resp *http.Response, provider providers.Provider, inputTokens int) {
	// Handle decompression
	bodyReader, err := h.decompressReader(resp)
	if err != nil {
		h.httpError(w, http.StatusBadGateway, "decompression error: %v", err)
		return
	}
	if closer, ok := bodyReader.(io.Closer); ok {
		defer closer.Close()
	}

	// Read full response
	respBody, err := io.ReadAll(bodyReader)
	if err != nil {
		h.httpError(w, http.StatusBadGateway, "failed to read upstream response: %v", err)
		return
	}

	var finalBody []byte

	// For error responses, forward original response without transformation
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("\nUpstream error response body:\n%s\n", string(respBody))
		finalBody = respBody
	} else {
		// Transform successful responses
		transformedBody, err := provider.Transform(respBody)
		if err != nil {
			h.logger.Warn("Response transformation failed, using original", "error", err)
			finalBody = respBody
		} else {
			finalBody = transformedBody
		}
	}

	// Copy headers and send response
	h.copyHeaders(w, resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(finalBody)

	h.logResponseTokens(finalBody, resp.StatusCode, inputTokens)
}

func (h *ProxyHandler) findProvider(modelName string, cfg *config.Config) (providers.Provider, *config.Provider, error) {
	// Parse provider name from model (format: "provider,model" or just "model")
	parts := strings.SplitN(modelName, ",", 2)
	var providerName string
	if len(parts) > 1 {
		providerName = parts[0]
	}

	// Find provider config
	var providerConfig *config.Provider
	for i, p := range cfg.Providers {
		if p.Name == providerName {
			providerConfig = &cfg.Providers[i]
			break
		}
	}

	var provider providers.Provider

	if providerConfig != nil {
		_provider, err := h.registry.GetByDomain(providerConfig.APIBase)
		if err != nil {
			return nil, nil, fmt.Errorf("no provider implementation for domain: %w", err)
		}

		provider = _provider
	} else {
		_provider, ok := h.registry.Get(providerName)
		if !ok {
			return nil, nil, fmt.Errorf("provider '%s' not found in registry", providerName)
		}

		providerConfig = &config.Provider{
			Name:    _provider.Name(),
			APIBase: _provider.GetEndpoint(),
		}

		provider = _provider
	}

	// Use provider-specific API key if available, otherwise fallback to CCO_API_KEY
	var apiKey string
	if providerConfig != nil {
		apiKey = providerConfig.APIKey
	}

	if apiKey == "" {
		if ccoAPIKey := os.Getenv("CCO_API_KEY"); ccoAPIKey != "" {
			apiKey = ccoAPIKey
			h.logger.Debug("Using CCO_API_KEY for provider", "provider", provider.Name())
		}

		providerConfig.APIKey = apiKey
	}

	provider.SetAPIKey(apiKey)

	return provider, providerConfig, nil
}

func (h *ProxyHandler) selectModel(inputBody []byte, tokens int, routerConfig *config.RouterConfig) ([]byte, string) {
	var modelBody map[string]any
	if err := json.Unmarshal(inputBody, &modelBody); err != nil {
		h.logger.Error("Failed to unmarshal request body for model selection", "error", err)
		return inputBody, routerConfig.Default
	}

	// Model selection logic
	var selectedModel string

	// Check if user provided explicit model in request
	if model, ok := modelBody["model"].(string); ok && len(model) > 0 {
		// If model contains comma (provider,model format), use it directly
		if strings.Contains(model, ",") {
			selectedModel = model
		} else {
			// Apply automatic routing logic for non-explicit provider requests
			if tokens > 60000 && routerConfig.LongContext != "" {
				selectedModel = routerConfig.LongContext
			} else if strings.HasPrefix(model, "claude-3-5-haiku") && routerConfig.Background != "" {
				selectedModel = routerConfig.Background
			} else if routerConfig.Think != "" {
				selectedModel = routerConfig.Think
			} else if routerConfig.WebSearch != "" {
				selectedModel = routerConfig.WebSearch
			} else {
				selectedModel = model
			}
		}
	} else {
		// No model specified, use default
		selectedModel = routerConfig.Default
	}

	// Update model in request body
	var finalModel string
	if parts := strings.SplitN(selectedModel, ",", 2); len(parts) > 1 {
		finalModel = parts[1]
	} else {
		finalModel = selectedModel
	}

	// Handle :online suffix for web search (preserve it for OpenRouter)
	// OpenRouter expects model:online format, so we keep it as-is
	modelBody["model"] = finalModel

	updatedBody, err := json.Marshal(modelBody)
	if err != nil {
		h.logger.Error("Failed to marshal updated request body", "error", err)
		return inputBody, selectedModel
	}

	return updatedBody, selectedModel
}

func (h *ProxyHandler) countInputTokens(text string) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		h.logger.Error("Failed to get tiktoken encoding", "error", err)
		return 0
	}
	return len(tke.Encode(text, nil, nil))
}

func (h *ProxyHandler) decompressReader(resp *http.Response) (io.Reader, error) {
	var bodyReader io.Reader = resp.Body
	encoding := resp.Header.Get("Content-Encoding")

	switch encoding {
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		bodyReader = gzipReader
	case "br":
		bodyReader = brotli.NewReader(resp.Body)
	}

	return bodyReader, nil
}

func (h *ProxyHandler) copyHeaders(w http.ResponseWriter, resp *http.Response) {
	for key, values := range resp.Header {
		// Skip compression headers since we handle decompression
		if key == "Content-Encoding" || key == "Content-Length" {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
}

func (h *ProxyHandler) flushResponse(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *ProxyHandler) httpError(w http.ResponseWriter, code int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	h.logger.Error("HTTP Error", "code", code, "message", msg)
	http.Error(w, msg, code)
}

func (h *ProxyHandler) transformRequestToProviderFormat(requestBody []byte, providerName string) ([]byte, error) {
	switch providerName {
	case "openrouter", "openai":
		return h.transformAnthropicToOpenAI(requestBody)
	case "gemini":
		return h.transformAnthropicToGemini(requestBody)
	case "anthropic":
		return h.transformOpenAIToAnthropic(requestBody)
	default:
		return requestBody, nil // Default: no transformation
	}
}

func (h *ProxyHandler) transformAnthropicToOpenAI(anthropicRequest []byte) ([]byte, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(anthropicRequest, &request); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Anthropic request: %w", err)
	}

	// Remove Anthropic-specific fields that OpenAI doesn't support
	cleanedRequest := h.removeAnthropicSpecificFields(request)

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
		cleanedRequest["messages"] = h.transformMessages(messages)
	}

	// Transform tools from Claude format to OpenAI/OpenRouter format if present
	if tools, ok := cleanedRequest["tools"].([]interface{}); ok {
		transformedTools, err := h.transformTools(tools)
		if err != nil {
			h.logger.Warn("Failed to transform tools", "error", err)
			// If tools transformation fails, remove tool_choice to prevent validation errors
			if _, hasToolChoice := cleanedRequest["tool_choice"]; hasToolChoice {
				delete(cleanedRequest, "tool_choice")
			}
		} else {
			cleanedRequest["tools"] = transformedTools

			// Re-validate tool_choice after successful transformation
			// If transformed tools array is empty, remove tool_choice
			if len(transformedTools) == 0 {
				if _, hasToolChoice := cleanedRequest["tool_choice"]; hasToolChoice {
					delete(cleanedRequest, "tool_choice")
				}
			}
		}
	}

	return json.Marshal(cleanedRequest)
}

func (h *ProxyHandler) transformOpenAIToAnthropic(openAIRequest []byte) ([]byte, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(openAIRequest, &request); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenAI request: %w", err)
	}

	// Transform messages from OpenAI format to Claude format
	if messages, ok := request["messages"].([]interface{}); ok {
		transformedMessages := h.transformOpenAIMessagesToClaude(messages)
		request["messages"] = transformedMessages
	}

	// Transform tools from OpenAI format to Claude format
	if tools, ok := request["tools"].([]interface{}); ok {
		transformedTools := h.transformOpenAIToolsToClaude(tools)
		request["tools"] = transformedTools
	}

	return json.Marshal(request)
}

func (h *ProxyHandler) transformOpenAIMessagesToClaude(messages []interface{}) []interface{} {
	transformedMessages := make([]interface{}, 0, len(messages))

	i := 0
	for i < len(messages) {
		if msgMap, ok := messages[i].(map[string]interface{}); ok {
			role, _ := msgMap["role"].(string)

			if role == "tool" {
				// Convert OpenAI tool message to Claude tool_result format

				// Collect all consecutive tool messages
				var toolResults []interface{}
				for i < len(messages) {
					if toolMsg, ok := messages[i].(map[string]interface{}); ok {
						if toolRole, _ := toolMsg["role"].(string); toolRole == "tool" {
							toolCallID, _ := toolMsg["tool_call_id"].(string)
							content := toolMsg["content"]

							// Convert call_ to toolu_ format
							claudeToolID := "toolu_" + strings.TrimPrefix(toolCallID, "call_")

							toolResult := map[string]interface{}{
								"type":        "tool_result",
								"tool_use_id": claudeToolID,
								"content":     content,
							}

							toolResults = append(toolResults, toolResult)
							i++
						} else {
							break
						}
					} else {
						break
					}
				}

				if len(toolResults) > 0 {
					// Create user message with tool results
					userMessage := map[string]interface{}{
						"role":    "user",
						"content": toolResults,
					}
					transformedMessages = append(transformedMessages, userMessage)
				}
			} else {
				// Regular message, keep as-is
				transformedMessages = append(transformedMessages, msgMap)
				i++
			}
		} else {
			// Non-map message, keep as-is
			transformedMessages = append(transformedMessages, messages[i])
			i++
		}
	}

	return transformedMessages
}

func (h *ProxyHandler) transformOpenAIToolsToClaude(tools []interface{}) []interface{} {
	claudeTools := make([]interface{}, 0, len(tools))

	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]interface{}); ok {
			// Check if this is OpenAI format: {"type": "function", "function": {...}}
			if toolType, ok := toolMap["type"].(string); ok && toolType == "function" {
				if function, ok := toolMap["function"].(map[string]interface{}); ok {
					claudeTool := map[string]interface{}{
						"name":        function["name"],
						"description": function["description"],
					}

					// Transform parameters to input_schema
					if parameters, ok := function["parameters"]; ok {
						claudeTool["input_schema"] = parameters
					}

					claudeTools = append(claudeTools, claudeTool)
				}
			} else {
				// Already in Claude format or unknown format, keep as-is
				claudeTools = append(claudeTools, tool)
			}
		}
	}

	return claudeTools
}

func (h *ProxyHandler) removeAnthropicSpecificFields(request map[string]interface{}) map[string]interface{} {
	// Remove Claude/Anthropic-specific fields that OpenAI/OpenRouter don't support
	fieldsToRemove := []string{"cache_control"}
	
	// Remove metadata if store is not enabled (OpenAI requirement)
	if store, hasStore := request["store"]; !hasStore || store != true {
		fieldsToRemove = append(fieldsToRemove, "metadata")
	}
	
	cleaned := h.removeFieldsRecursively(request, fieldsToRemove).(map[string]interface{})

	// Handle tool_choice logic: only remove if no tools are present, tools is null, or tools is empty array
	if tools, hasTools := cleaned["tools"]; !hasTools || tools == nil {
		delete(cleaned, "tool_choice")
	} else if toolsArray, ok := tools.([]interface{}); ok && len(toolsArray) == 0 {
		delete(cleaned, "tool_choice")
	}

	return cleaned
}

// buildEndpointURL constructs the final endpoint URL for the provider
func (h *ProxyHandler) buildEndpointURL(provider providers.Provider, baseURL, modelName string) string {
	// Handle Gemini's special URL requirement
	if provider.Name() == "gemini" {
		// Extract actual model name from modelName (remove provider prefix if present)
		actualModel := modelName
		if parts := strings.SplitN(modelName, ",", 2); len(parts) > 1 {
			actualModel = parts[1]
		}
		
		// Gemini requires the model in the URL path
		// Format: https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
		if strings.HasSuffix(baseURL, "/models") {
			return fmt.Sprintf("%s/%s:generateContent", baseURL, actualModel)
		} else if strings.Contains(baseURL, "/models/") {
			// Base URL already has a model specified, replace it
			baseIndex := strings.LastIndex(baseURL, "/models/")
			basePart := baseURL[:baseIndex+8] // Keep "/models/"
			return fmt.Sprintf("%s%s:generateContent", basePart, actualModel)
		}
		// Fallback to appending the model
		return fmt.Sprintf("%s/%s:generateContent", strings.TrimSuffix(baseURL, "/"), actualModel)
	}
	
	// For all other providers, use the base URL as-is
	return baseURL
}

// transformAnthropicToGemini converts Anthropic/Claude format to Gemini format
func (h *ProxyHandler) transformAnthropicToGemini(requestBody []byte) ([]byte, error) {
	var anthropicReq map[string]interface{}
	if err := json.Unmarshal(requestBody, &anthropicReq); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Anthropic request: %w", err)
	}

	geminiReq := make(map[string]interface{})

	// Handle system message and convert messages to contents
	contents, err := h.convertAnthropicMessagesToGeminiContents(anthropicReq)
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
		geminiTools, err := h.convertAnthropicToolsToGemini(tools)
		if err != nil {
			return nil, fmt.Errorf("failed to convert tools: %w", err)
		}
		geminiReq["tools"] = geminiTools
	}

	// Convert safety settings if needed
	safetySettings := []map[string]interface{}{
		{
			"category":  "HARM_CATEGORY_HARASSMENT",
			"threshold": "BLOCK_MEDIUM_AND_ABOVE",
		},
		{
			"category":  "HARM_CATEGORY_HATE_SPEECH", 
			"threshold": "BLOCK_MEDIUM_AND_ABOVE",
		},
		{
			"category":  "HARM_CATEGORY_SEXUALLY_EXPLICIT",
			"threshold": "BLOCK_MEDIUM_AND_ABOVE",
		},
		{
			"category":  "HARM_CATEGORY_DANGEROUS_CONTENT",
			"threshold": "BLOCK_MEDIUM_AND_ABOVE",
		},
	}
	geminiReq["safetySettings"] = safetySettings

	return json.Marshal(geminiReq)
}

// convertAnthropicMessagesToGeminiContents converts Anthropic messages to Gemini contents format
func (h *ProxyHandler) convertAnthropicMessagesToGeminiContents(anthropicReq map[string]interface{}) ([]interface{}, error) {
	var contents []interface{}

	// Handle system message first
	if system, ok := anthropicReq["system"].(string); ok && system != "" {
		systemContent := map[string]interface{}{
			"role": "user",
			"parts": []map[string]interface{}{
				{
					"text": "System: " + system,
				},
			},
		}
		contents = append(contents, systemContent)
	}

	// Convert messages
	if messages, ok := anthropicReq["messages"].([]interface{}); ok {
		for _, msg := range messages {
			if msgMap, ok := msg.(map[string]interface{}); ok {
				geminiContent, err := h.convertAnthropicMessageToGeminiContent(msgMap)
				if err != nil {
					return nil, fmt.Errorf("failed to convert message: %w", err)
				}
				if geminiContent != nil {
					contents = append(contents, geminiContent)
				}
			}
		}
	}

	return contents, nil
}

// convertAnthropicMessageToGeminiContent converts a single Anthropic message to Gemini content
func (h *ProxyHandler) convertAnthropicMessageToGeminiContent(message map[string]interface{}) (map[string]interface{}, error) {
	role, ok := message["role"].(string)
	if !ok {
		return nil, fmt.Errorf("message missing role")
	}

	// Map roles
	var geminiRole string
	switch role {
	case "user":
		geminiRole = "user"
	case "assistant":
		geminiRole = "model"
	default:
		return nil, fmt.Errorf("unsupported role: %s", role)
	}

	content := map[string]interface{}{
		"role": geminiRole,
	}

	// Convert content array
	if contentArray, ok := message["content"].([]interface{}); ok {
		parts, err := h.convertAnthropicContentToGeminiParts(contentArray)
		if err != nil {
			return nil, fmt.Errorf("failed to convert content: %w", err)
		}
		content["parts"] = parts
	} else if contentStr, ok := message["content"].(string); ok {
		// Handle string content
		content["parts"] = []map[string]interface{}{
			{
				"text": contentStr,
			},
		}
	}

	return content, nil
}

// convertAnthropicContentToGeminiParts converts Anthropic content blocks to Gemini parts
func (h *ProxyHandler) convertAnthropicContentToGeminiParts(contentArray []interface{}) ([]interface{}, error) {
	var parts []interface{}

	for _, item := range contentArray {
		if itemMap, ok := item.(map[string]interface{}); ok {
			contentType, ok := itemMap["type"].(string)
			if !ok {
				continue
			}

			switch contentType {
			case "text":
				if text, ok := itemMap["text"].(string); ok {
					parts = append(parts, map[string]interface{}{
						"text": text,
					})
				}
			case "tool_use":
				// Convert tool use to function call
				if name, ok := itemMap["name"].(string); ok {
					functionCall := map[string]interface{}{
						"name": name,
					}
					if input, ok := itemMap["input"].(map[string]interface{}); ok {
						functionCall["args"] = input
					}
					parts = append(parts, map[string]interface{}{
						"functionCall": functionCall,
					})
				}
			case "tool_result":
				// Convert tool result to function response
				if _, ok := itemMap["tool_use_id"].(string); ok {
					// Gemini expects function_response.response to be a structured object
					// Convert string content to structured format
					var responseContent interface{}
					if content, ok := itemMap["content"]; ok {
						if contentStr, isString := content.(string); isString {
							// Wrap string content in an object structure
							responseContent = map[string]interface{}{
								"result": contentStr,
							}
						} else {
							// If it's already structured, use as-is
							responseContent = content
						}
					} else {
						responseContent = map[string]interface{}{
							"result": "",
						}
					}

					functionResponse := map[string]interface{}{
						"name":     "tool_result", // Generic name for tool results
						"response": responseContent,
					}
					parts = append(parts, map[string]interface{}{
						"functionResponse": functionResponse,
					})
				}
			case "image":
				// Handle image content if supported
				if source, ok := itemMap["source"].(map[string]interface{}); ok {
					if mediaType, ok := source["media_type"].(string); ok {
						if data, ok := source["data"].(string); ok {
							parts = append(parts, map[string]interface{}{
								"inlineData": map[string]interface{}{
									"mimeType": mediaType,
									"data":     data,
								},
							})
						}
					}
				}
			}
		}
	}

	return parts, nil
}

// convertAnthropicToolsToGemini converts Anthropic tools to Gemini format
func (h *ProxyHandler) convertAnthropicToolsToGemini(tools []interface{}) ([]interface{}, error) {
	var geminiTools []interface{}

	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]interface{}); ok {
			if toolType, ok := toolMap["type"].(string); ok && toolType == "function" {
				if function, ok := toolMap["function"].(map[string]interface{}); ok {
					geminiFunction := map[string]interface{}{
						"name": function["name"],
					}
					
					if description, ok := function["description"].(string); ok {
						geminiFunction["description"] = description
					}

					// Convert parameters schema
					if parameters, ok := function["parameters"].(map[string]interface{}); ok {
						geminiFunction["parameters"] = h.convertOpenAPISchemaToGemini(parameters)
					}

					geminiTool := map[string]interface{}{
						"functionDeclarations": []interface{}{geminiFunction},
					}
					geminiTools = append(geminiTools, geminiTool)
				}
			}
		}
	}

	return geminiTools, nil
}

// convertOpenAPISchemaToGemini converts OpenAPI schema to Gemini schema format
func (h *ProxyHandler) convertOpenAPISchemaToGemini(schema map[string]interface{}) map[string]interface{} {
	geminiSchema := make(map[string]interface{})

	if schemaType, ok := schema["type"].(string); ok {
		geminiSchema["type"] = strings.ToUpper(schemaType)
	}

	if description, ok := schema["description"].(string); ok {
		geminiSchema["description"] = description
	}

	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		geminiProperties := make(map[string]interface{})
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				geminiProperties[key] = h.convertOpenAPISchemaToGemini(propMap)
			}
		}
		geminiSchema["properties"] = geminiProperties
	}

	if required, ok := schema["required"].([]interface{}); ok {
		geminiSchema["required"] = required
	}

	if items, ok := schema["items"].(map[string]interface{}); ok {
		geminiSchema["items"] = h.convertOpenAPISchemaToGemini(items)
	}

	if enum, ok := schema["enum"].([]interface{}); ok {
		geminiSchema["enum"] = enum
	}

	return geminiSchema
}

// setAuthHeader sets the appropriate authentication header for the provider
func (h *ProxyHandler) setAuthHeader(req *http.Request, provider providers.Provider, apiKey string) {
	switch provider.Name() {
	case "gemini":
		// Gemini uses x-goog-api-key header
		req.Header.Set("x-goog-api-key", apiKey)
	default:
		// All other providers use Bearer token
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func (h *ProxyHandler) removeFieldsRecursively(data interface{}, fieldsToRemove []string) interface{} {
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
				result[key] = h.removeFieldsRecursively(value, fieldsToRemove)
			}
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = h.removeFieldsRecursively(item, fieldsToRemove)
		}
		return result
	default:
		return v
	}
}

func (h *ProxyHandler) transformTools(tools []interface{}) ([]interface{}, error) {
	transformedTools := make([]interface{}, 0, len(tools))

	for i, tool := range tools {

		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			h.logger.Warn("Skipping malformed tool", "index", i, "type", fmt.Sprintf("%T", tool))
			continue // Skip malformed tools
		}

		// Check if this is already in OpenAI format (has "type": "function" and "function" field)
		if toolType, hasType := toolMap["type"].(string); hasType && toolType == "function" {
			if _, hasFunction := toolMap["function"]; hasFunction {
				// Already in OpenAI format, keep as-is
				transformedTools = append(transformedTools, tool)
				continue
			}
		}

		// Transform from Claude format to OpenAI format
		// Claude tools might have: name, description, input_schema
		// OpenAI tools need: type: "function", function: {name, description, parameters}
		if name, hasName := toolMap["name"].(string); hasName {

			openAITool := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": name,
				},
			}

			function := openAITool["function"].(map[string]interface{})

			// Add description if present
			if description, hasDesc := toolMap["description"].(string); hasDesc {
				function["description"] = description
			}

			// Transform input_schema to parameters
			if inputSchema, hasInputSchema := toolMap["input_schema"]; hasInputSchema {
				function["parameters"] = inputSchema
			}
			transformedTools = append(transformedTools, openAITool)
		} else {
			h.logger.Warn("Tool missing name field", "index", i, "tool", toolMap)
		}
	}

	return transformedTools, nil
}

func (h *ProxyHandler) transformMessages(messages []interface{}) []interface{} {
	transformedMessages := make([]interface{}, 0, len(messages))

	for i, message := range messages {
		if msgMap, ok := message.(map[string]interface{}); ok {

			// Check role-specific transformations
			if role, ok := msgMap["role"].(string); ok {
				if role == "user" {
					// Transform user messages with tool_result blocks to OpenAI tool message format
					if content, ok := msgMap["content"].([]interface{}); ok {
						toolResultMessages := h.extractToolResults(content, i)
						if len(toolResultMessages) > 0 {
							transformedMessages = append(transformedMessages, toolResultMessages...)
							continue // Skip the original message as we've replaced it
						}
					}
				} else if role == "assistant" {
					// Transform assistant messages with tool_use blocks to OpenAI tool_calls format
					if content, ok := msgMap["content"].([]interface{}); ok {
						transformedMessage := h.transformAssistantMessage(msgMap, content, i)
						if transformedMessage != nil {
							transformedMessages = append(transformedMessages, transformedMessage)
							continue
						}
					}
				}
			}

			// Regular message transformation - remove cache_control
			cleanedMessage := h.removeFieldsRecursively(msgMap, []string{"cache_control"})
			transformedMessages = append(transformedMessages, cleanedMessage)
		} else {
			transformedMessages = append(transformedMessages, message)
		}
	}
	return transformedMessages
}

// extractToolResults converts Claude tool_result blocks to OpenAI tool message format
func (h *ProxyHandler) extractToolResults(content []interface{}, messageIndex int) []interface{} {
	var toolMessages []interface{}
	var regularContent []interface{}

	for _, contentBlock := range content {
		if blockMap, ok := contentBlock.(map[string]interface{}); ok {
			if blockType, ok := blockMap["type"].(string); ok && blockType == "tool_result" {
				toolUseID, _ := blockMap["tool_use_id"].(string)
				resultContent := blockMap["content"]

				h.logger.Info("Processing tool result",
					"tool_use_id", toolUseID,
					"content_preview", h.truncateContent(resultContent, 100))

				// Convert Claude tool_use_id (toolu_*) to OpenAI tool_call_id (call_*)
				// Handle malformed double-prefix cases
				var openAIToolID string
				if strings.HasPrefix(toolUseID, "toolu_toolu_") {
					// Malformed double prefix - extract the core ID
					coreID := strings.TrimPrefix(toolUseID, "toolu_toolu_")
					openAIToolID = "call_" + coreID
					h.logger.Warn("Fixed malformed double toolu_ prefix",
						"original_id", toolUseID,
						"core_id", coreID,
						"fixed_id", openAIToolID)
				} else if strings.HasPrefix(toolUseID, "toolu_") {
					openAIToolID = "call_" + strings.TrimPrefix(toolUseID, "toolu_")
				} else if strings.HasPrefix(toolUseID, "call_") {
					// Already in OpenAI format, keep as-is
					openAIToolID = toolUseID
				} else {
					// Unknown format, add call_ prefix
					openAIToolID = "call_" + toolUseID
				}

				// Create OpenAI format tool message
				toolMessage := map[string]interface{}{
					"role":         "tool",
					"tool_call_id": openAIToolID,
					"content":      h.formatToolResultContent(resultContent),
				}

				// Validate ID format
				if strings.Contains(openAIToolID, "toolu_") {
					h.logger.Warn("Invalid tool_call_id format detected",
						"original_id", toolUseID,
						"converted_id", openAIToolID,
						"error", "tool_call_id should not contain toolu_ prefix")
				}

				toolMessages = append(toolMessages, toolMessage)
			} else {
				// Regular content block
				regularContent = append(regularContent, contentBlock)
			}
		} else {
			// Non-map content block
			regularContent = append(regularContent, contentBlock)
		}
	}

	// If we found tool results, return them as separate messages
	// If we also have regular content, add it as a user message after tool results
	if len(toolMessages) > 0 {
		if len(regularContent) > 0 {
			regularMessage := map[string]interface{}{
				"role":    "user",
				"content": regularContent,
			}
			toolMessages = append(toolMessages, regularMessage)
		}
		return toolMessages
	}

	// No tool results found
	return nil
}

// formatToolResultContent converts tool result content to string format expected by OpenAI
func (h *ProxyHandler) formatToolResultContent(content interface{}) string {
	if str, ok := content.(string); ok {
		return str
	}

	// Handle array of content blocks (text/image)
	if contentArray, ok := content.([]interface{}); ok {
		var textParts []string
		for _, block := range contentArray {
			if blockMap, ok := block.(map[string]interface{}); ok {
				if blockType, ok := blockMap["type"].(string); ok && blockType == "text" {
					if text, ok := blockMap["text"].(string); ok {
						textParts = append(textParts, text)
					}
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n")
		}
	}

	// Fallback: convert to JSON string
	if jsonBytes, err := json.Marshal(content); err == nil {
		return string(jsonBytes)
	}

	return fmt.Sprintf("%v", content)
}

// truncateContent truncates content for logging while preserving readability
func (h *ProxyHandler) truncateContent(content interface{}, maxLen int) string {
	str := fmt.Sprintf("%v", content)
	if len(str) <= maxLen {
		return str
	}
	return str[:maxLen] + "..."
}

// transformAssistantMessage converts Claude assistant messages with tool_use blocks to OpenAI format with tool_calls
func (h *ProxyHandler) transformAssistantMessage(msgMap map[string]interface{}, content []interface{}, messageIndex int) map[string]interface{} {
	var textContent strings.Builder
	var toolCalls []interface{}

	for _, contentBlock := range content {
		if blockMap, ok := contentBlock.(map[string]interface{}); ok {
			blockType, _ := blockMap["type"].(string)

			switch blockType {
			case "text":
				// Extract text content
				if text, ok := blockMap["text"].(string); ok {
					textContent.WriteString(text)
				}
			case "tool_use":
				// Convert Claude tool_use to OpenAI tool_call format
				toolUseID, _ := blockMap["id"].(string)
				toolName, _ := blockMap["name"].(string)
				toolInput := blockMap["input"]

				h.logger.Info("Processing tool use",
					"tool_id", toolUseID,
					"tool_name", toolName,
					"tool_input", toolInput)

				// Convert Claude tool_use_id (toolu_*) to OpenAI tool_call_id (call_*)
				var openAIToolID string
				if strings.HasPrefix(toolUseID, "toolu_") {
					openAIToolID = "call_" + strings.TrimPrefix(toolUseID, "toolu_")
				} else if strings.HasPrefix(toolUseID, "call_") {
					openAIToolID = toolUseID
				} else {
					openAIToolID = "call_" + toolUseID
				}

				// Convert input to JSON string format expected by OpenAI
				var argumentsJSON string
				if toolInput != nil {
					if inputBytes, err := json.Marshal(toolInput); err == nil {
						argumentsJSON = string(inputBytes)
					} else {
						argumentsJSON = "{}"
					}
				} else {
					argumentsJSON = "{}"
				}

				// Create OpenAI tool_call format
				toolCall := map[string]interface{}{
					"id":   openAIToolID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      toolName,
						"arguments": argumentsJSON,
					},
				}

				toolCalls = append(toolCalls, toolCall)
			}
		}
	}

	// Only return transformed message if we found tool_use blocks
	if len(toolCalls) > 0 {
		result := map[string]interface{}{
			"role":       "assistant",
			"content":    textContent.String(),
			"tool_calls": toolCalls,
		}

		// If content is empty, set to null as expected by OpenAI format
		if textContent.Len() == 0 {
			result["content"] = nil
		}

		return result
	}

	// No tool_use blocks found, return nil to indicate no transformation needed
	return nil
}

func (h *ProxyHandler) logResponseTokens(respBody []byte, statusCode int, inputTokens int) {
	logFields := []any{
		"status", statusCode,
		"input_tokens", inputTokens,
	}

	// Try to extract output tokens from response
	var response map[string]interface{}
	if err := json.Unmarshal(respBody, &response); err == nil {
		if usage, ok := response["usage"].(map[string]interface{}); ok {
			if outputTokens, ok := usage["output_tokens"]; ok {
				logFields = append(logFields, "output_tokens", outputTokens)
			}
		}
	}

	if statusCode != http.StatusOK {
		h.logger.Error("Upstream error response", logFields...)
	} else {
		h.logger.Info("Successful response", logFields...)
	}
}
