package main

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
)

func handleStreamingOpenRouter(w http.ResponseWriter, resp *http.Response, inputTokens int) {
	// Handle decompression
	var bodyReader io.Reader = resp.Body
	encoding := resp.Header.Get("Content-Encoding")
	switch encoding {
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			httpError(w, http.StatusBadGateway, "create gzip reader: %v", err)
			logger.Error("Failed to create gzip reader", "error", err)
			return
		}
		defer gzipReader.Close()
		bodyReader = gzipReader
	case "br":
		brotliReader := brotli.NewReader(resp.Body)
		bodyReader = brotliReader
	}

	// Set streaming headers for Claude format
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)

	// Create scanner to read SSE lines
	scanner := bufio.NewScanner(bodyReader)

	// Track if we've sent the initial message_start event
	messageStartSent := false
	var messageId string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines
		if line == "" {
			fmt.Fprint(w, "\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			continue
		}

		// Handle OpenRouter processing comments - ignore them
		if strings.HasPrefix(line, ": OPENROUTER PROCESSING") {
			continue
		}

		// Handle other SSE comments - pass through
		if strings.HasPrefix(line, ":") {
			fmt.Fprintf(w, "%s\n", line)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			continue
		}

		// Handle [DONE] message
		if line == "data: [DONE]" {
			// Send final message_stop event
			if messageId != "" {
				stopEvent := map[string]interface{}{
					"type": "message_stop",
				}
				stopEventJson, _ := json.Marshal(stopEvent)
				fmt.Fprintf(w, "data: %s\n\n", string(stopEventJson))
			}

			fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			break
		}

		// Process data lines
		if strings.HasPrefix(line, "data: ") {
			jsonData := strings.TrimPrefix(line, "data: ")

			// Parse OpenRouter chunk
			var orChunk map[string]interface{}
			if err := json.Unmarshal([]byte(jsonData), &orChunk); err != nil {
				logger.Error("Failed to parse OpenRouter chunk", "error", err)
				// Send original chunk if parsing fails
				fmt.Fprintf(w, "%s\n\n", line)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				continue
			}

			// Extract message ID for consistency
			if id, ok := orChunk["id"].(string); ok && messageId == "" {
				messageId = id
			}

			// Send message_start event for first chunk
			if !messageStartSent {
				startEvent := map[string]interface{}{
					"type": "message_start",
					"message": map[string]interface{}{
						"id":      messageId,
						"type":    "message",
						"role":    "assistant",
						"content": []interface{}{},
						"model":   orChunk["model"],
					},
				}
				startEventJson, _ := json.Marshal(startEvent)
				fmt.Fprintf(w, "data: %s\n\n", string(startEventJson))
				messageStartSent = true
			}

			// Transform and send content delta
			if choices, ok := orChunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if firstChoice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := firstChoice["delta"].(map[string]interface{}); ok {
						// Handle content delta
						if content, ok := delta["content"].(string); ok && content != "" {
							deltaEvent := map[string]interface{}{
								"type":  "content_block_delta",
								"index": 0,
								"delta": map[string]interface{}{
									"type": "text_delta",
									"text": content,
								},
							}
							deltaEventJson, _ := json.Marshal(deltaEvent)
							fmt.Fprintf(w, "data: %s\n\n", string(deltaEventJson))
						}

						// Handle finish_reason
						if finishReason, ok := firstChoice["finish_reason"]; ok && finishReason != nil {
							stopEvent := map[string]interface{}{
								"type":  "content_block_stop",
								"index": 0,
							}
							stopEventJson, _ := json.Marshal(stopEvent)
							fmt.Fprintf(w, "data: %s\n\n", string(stopEventJson))
						}
					}
				}
			}

			// Handle usage information (typically in final chunks)
			if usage, ok := orChunk["usage"].(map[string]interface{}); ok {
				claudeUsage := make(map[string]interface{})

				// Map token fields
				if promptTokens, ok := usage["prompt_tokens"]; ok {
					claudeUsage["input_tokens"] = promptTokens
				}
				if completionTokens, ok := usage["completion_tokens"]; ok {
					claudeUsage["output_tokens"] = completionTokens
				}

				// Handle token details
				if promptDetails, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
					if cachedTokens, ok := promptDetails["cached_tokens"]; ok {
						claudeUsage["cache_read_input_tokens"] = cachedTokens
					}
				}

				usageEvent := map[string]interface{}{
					"type": "message_delta",
					"delta": map[string]interface{}{
						"stop_reason": "end_turn",
						"usage":       claudeUsage,
					},
				}
				usageEventJson, _ := json.Marshal(usageEvent)
				fmt.Fprintf(w, "data: %s\n\n", string(usageEventJson))
			}

			// Flush after each event
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Error("Error reading stream", "error", err)
	}

	logger.Info("Completed streaming response",
		"status", resp.StatusCode,
		"input_tokens", inputTokens)
}

// convertOpenRouterChunkToClaude converts a single OpenRouter streaming chunk to Claude format
func convertOpenRouterChunkToClaude(openRouterChunk []byte) ([]byte, error) {
	var orChunk map[string]interface{}

	if err := json.Unmarshal(openRouterChunk, &orChunk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenRouter chunk: %w", err)
	}

	// Create Claude chunk structure
	claudeChunk := make(map[string]interface{})

	// Copy ID if present
	if id, ok := orChunk["id"]; ok {
		claudeChunk["id"] = id
	}

	// Set type for streaming
	claudeChunk["type"] = "message_delta"

	// Handle choices array
	if choices, ok := orChunk["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {
			// Extract delta from the choice
			if delta, ok := firstChoice["delta"].(map[string]interface{}); ok {
				// Create Claude delta structure
				claudeDelta := make(map[string]interface{})

				// Handle content
				if content, ok := delta["content"].(string); ok && content != "" {
					claudeDelta["text"] = content
					claudeChunk["delta"] = map[string]interface{}{
						"type": "text_delta",
						"text": content,
					}
				}

				// Handle role (usually only in first chunk)
				if role, ok := delta["role"]; ok {
					claudeChunk["role"] = role
				}

				// Handle other delta fields dynamically
				for key, value := range delta {
					if key != "content" && key != "role" {
						claudeDelta["delta_"+key] = value
					}
				}
			}

			// Handle finish_reason
			if finishReason, ok := firstChoice["finish_reason"]; ok && finishReason != nil {
				claudeChunk["stop_reason"] = finishReason
			}

			// Handle other choice fields
			for key, value := range firstChoice {
				if key != "delta" && key != "finish_reason" {
					claudeChunk["choice_"+key] = value
				}
			}
		}
	}

	// Copy model if present
	if model, ok := orChunk["model"]; ok {
		claudeChunk["model"] = model
	}

	// Handle usage (typically in the last chunk)
	if usage, ok := orChunk["usage"].(map[string]interface{}); ok {
		claudeUsage := make(map[string]interface{})

		// Map token fields
		if promptTokens, ok := usage["prompt_tokens"]; ok {
			claudeUsage["input_tokens"] = promptTokens
		}
		if completionTokens, ok := usage["completion_tokens"]; ok {
			claudeUsage["output_tokens"] = completionTokens
		}

		// Handle token details
		if promptDetails, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			if cachedTokens, ok := promptDetails["cached_tokens"]; ok {
				claudeUsage["cache_read_input_tokens"] = cachedTokens
			}
			// Add other prompt token details
			for key, value := range promptDetails {
				if key != "cached_tokens" {
					claudeUsage["prompt_"+key] = value
				}
			}
		}

		if completionDetails, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
			for key, value := range completionDetails {
				claudeUsage["completion_"+key] = value
			}
		}

		// Add other usage fields
		for key, value := range usage {
			if key != "prompt_tokens" && key != "completion_tokens" &&
				key != "prompt_tokens_details" && key != "completion_tokens_details" {
				claudeUsage[key] = value
			}
		}

		claudeChunk["usage"] = claudeUsage
	}

	// Handle other top-level fields dynamically
	for key, value := range orChunk {
		if key != "id" && key != "choices" && key != "model" && key != "usage" {
			claudeChunk["or_"+key] = value
		}
	}

	// Marshal back to JSON
	result, err := json.Marshal(claudeChunk)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Claude chunk: %w", err)
	}

	return result, nil
}

func handleDirectStream(w http.ResponseWriter, resp *http.Response, inputTokens int) {
	// Copy headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Handle decompression if needed
	var bodyReader io.Reader = resp.Body
	encoding := resp.Header.Get("Content-Encoding")
	switch encoding {
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			logger.Error("Failed to create gzip reader", "error", err)
			return
		}
		defer gzipReader.Close()
		bodyReader = gzipReader
	case "br":
		brotliReader := brotli.NewReader(resp.Body)
		bodyReader = brotliReader
	}

	// Stream the response directly
	bytesWritten, err := io.Copy(w, bodyReader)
	if err != nil {
		logger.Error("Failed to stream response", "error", err)
		return
	}

	logger.Info("Streamed response",
		"status", resp.StatusCode,
		"input_tokens", inputTokens,
		"bytes_written", bytesWritten)
}

func handleNonStreamingOpenRouter(w http.ResponseWriter, resp *http.Response, inputTokens int) {
	// Handle decompression
	var bodyReader io.Reader = resp.Body
	encoding := resp.Header.Get("Content-Encoding")
	switch encoding {
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			httpError(w, http.StatusBadGateway, "create gzip reader: %v", err)
			logger.Error("Failed to create gzip reader", "error", err)
			return
		}
		defer gzipReader.Close()
		bodyReader = gzipReader
	case "br":
		brotliReader := brotli.NewReader(resp.Body)
		bodyReader = brotliReader
	}

	// Read full response for transformation
	respBody, err := io.ReadAll(bodyReader)
	if err != nil {
		httpError(w, http.StatusBadGateway, "read upstream response: %v", err)
		logger.Error("Failed to read upstream response body", "error", err)
		return
	}

	// Transform OpenRouter response to Claude format
	transformedBody, err := convertOpenRouterToClaude(respBody)
	if err != nil {
		fmt.Println("Failed to transform OpenRouter body to Claude format", err)
		// Continue with original response if transformation fails
		transformedBody = respBody
	}

	// Set appropriate headers (remove compression headers since we're sending raw)
	for key, values := range resp.Header {
		if key != "Content-Encoding" && key != "Content-Length" {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(transformedBody)

	// Log response details with token usage
	logResponseWithTokens(transformedBody, resp.StatusCode, inputTokens)
}

func logResponseWithTokens(respBody []byte, statusCode int, inputTokens int) {
	logFields := []any{
		"status", statusCode,
		"input_tokens", inputTokens,
	}

	// Attempt to parse token usage from Claude format
	var claudeResponse map[string]interface{}
	if err := json.Unmarshal(respBody, &claudeResponse); err == nil {
		if usage, ok := claudeResponse["usage"].(map[string]interface{}); ok {
			if outputTokens, ok := usage["output_tokens"]; ok {
				logFields = append(logFields, "output_tokens", outputTokens)
			}
		}
	}

	if statusCode != http.StatusOK {
		logger.Error("Upstream non-200 response", logFields...)
	} else {
		logger.Info("Upstream 200 OK", logFields...)
	}
}

// Updated main proxy handler with proper streaming support
func streamingProxyHandler(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	if err := authenticate(cfg, r); err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		logger.Error("Unauthorized request", "remote_addr", r.RemoteAddr, "error", err)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, http.StatusBadRequest, "read body: %v", err)
		logger.Error("Failed to read request body", "error", err)
		return
	}

	// Clean cache control from request if needed
	if isOpenRouter(provider.APIBase) {
		body, err = removeCacheControl(body)
		if err != nil {
			fmt.Println("Failed to remove cache control from OpenRouter request", err)
		}
	}

	input := string(body)
	inputTokens := countInputTokensCl100k(input)
	input, model := selectModel(body, inputTokens, &cfg.Router)

	// Find the provider based on the model name
	var provider Provider
	for _, p := range cfg.Providers {
		if p.Name == providerName[0] {
			provider = p
			break
		}
	}

	if provider.Name == "" {
		slog.Error("Provider not set. You need to set <provider>,<model> in the config, can't forward request", "model", model)
		return
	}

	// Create upstream request
	req, err := http.NewRequest(r.Method, provider.APIBase, strings.NewReader(input))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "create request: %v", err)
		logger.Error("Failed to create upstream request", "error", err)
		return
	}
	req.Header = r.Header.Clone()
	if provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	logger.Info("Proxy request", "url", provider.APIBase, "model", model, "input_tokens", inputTokens)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream error: %v", err)
		logger.Error("Upstream request failed", "error", err)
		return
	}
	defer resp.Body.Close()

	// Check if this is a streaming response
	isStreaming := isStreamingResponse(resp)

	if isOpenRouter(provider.APIBase) && isStreaming {
		// Handle OpenRouter streaming with transformation
		handleStreamingOpenRouter(w, resp, inputTokens)
	} else if isOpenRouter(provider.APIBase) {
		// Handle non-streaming OpenRouter with transformation
		handleNonStreamingOpenRouter(w, resp, inputTokens)
	} else {
		// Direct proxy for non-OpenRouter providers
		handleDirectStream(w, resp, inputTokens)
	}
}

// Helper function to detect streaming responses more accurately
func isStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")
	return contentType == "text/event-stream" ||
		strings.Contains(contentType, "stream") ||
		resp.Header.Get("Transfer-Encoding") == "chunked"
}
