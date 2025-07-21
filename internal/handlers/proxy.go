package handlers

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/pkoukk/tiktoken-go"

	"github.com/Davincible/claude-code-router-go/internal/config"
	"github.com/Davincible/claude-code-router-go/internal/providers"
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

	// Create upstream request
	req, err := http.NewRequest(r.Method, providerConfig.APIBase, strings.NewReader(string(finalBody)))
	if err != nil {
		h.httpError(w, http.StatusInternalServerError, "failed to create upstream request: %v", err)
		return
	}

	// Copy headers and set auth
	req.Header = r.Header.Clone()
	if providerConfig.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+providerConfig.APIKey)
	}

	h.logger.Info("Proxying request",
		"provider", provider.Name(),
		"model", modelName,
		"url", providerConfig.APIBase,
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
			jsonData := strings.TrimPrefix(line, "data: ")

			// Transform chunk through provider
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

	// Transform response
	transformedBody, err := provider.Transform(respBody)
	if err != nil {
		h.logger.Warn("Response transformation failed, using original", "error", err)
		transformedBody = respBody
	}

	// Print response body on upstream errors
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("\nUpstream error response body:\n%s\n", string(respBody))
	}

	// Copy headers and send response
	h.copyHeaders(w, resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(transformedBody)

	h.logResponseTokens(transformedBody, resp.StatusCode, inputTokens)
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

	if providerConfig == nil {
		return nil, nil, fmt.Errorf("provider '%s' not found in configuration", providerName)
	}

	// Get provider implementation by domain
	provider, err := h.registry.GetByDomain(providerConfig.APIBase)
	if err != nil {
		return nil, nil, fmt.Errorf("no provider implementation for domain: %w", err)
	}

	provider.SetAPIKey(providerConfig.APIKey)

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
	if tokens > 60000 && routerConfig.LongContext != "" {
		selectedModel = routerConfig.LongContext
	} else if model, ok := modelBody["model"].(string); ok && strings.HasPrefix(model, "claude-3-5-haiku") && routerConfig.Background != "" {
		selectedModel = routerConfig.Background
	} else if routerConfig.Think != "" {
		selectedModel = routerConfig.Think
	} else if routerConfig.WebSearch != "" {
		selectedModel = routerConfig.WebSearch
	} else if model, ok := modelBody["model"].(string); ok && len(model) > 0 {
		selectedModel = model
	} else {
		selectedModel = routerConfig.Default
	}

	// Update model in request body
	if parts := strings.SplitN(selectedModel, ",", 2); len(parts) > 1 {
		modelBody["model"] = parts[1]
	} else {
		modelBody["model"] = selectedModel
	}

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
	case "anthropic":
		return requestBody, nil // No transformation needed
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

	// Transform any Anthropic-specific message formats if needed
	if messages, ok := cleanedRequest["messages"].([]interface{}); ok {
		cleanedRequest["messages"] = h.transformMessages(messages)
	}

	return json.Marshal(cleanedRequest)
}

func (h *ProxyHandler) removeAnthropicSpecificFields(request map[string]interface{}) map[string]interface{} {
	fieldsToRemove := []string{"cache_control"}
	return h.removeFieldsRecursively(request, fieldsToRemove).(map[string]interface{})
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

func (h *ProxyHandler) transformMessages(messages []interface{}) []interface{} {
	// Remove cache_control from individual messages as well
	transformedMessages := make([]interface{}, len(messages))
	for i, message := range messages {
		if msgMap, ok := message.(map[string]interface{}); ok {
			// Remove cache_control from this message
			cleanedMessage := h.removeFieldsRecursively(msgMap, []string{"cache_control"})
			transformedMessages[i] = cleanedMessage
		} else {
			transformedMessages[i] = message
		}
	}
	return transformedMessages
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