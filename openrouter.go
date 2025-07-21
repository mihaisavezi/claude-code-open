package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/3JoB/brotli"
)

func isOpenRouter(url string) bool {
	// Check if the URL contains "openrouter" to identify OpenRouter responses
	return strings.Contains(url, "openrouter")
}

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

		// Handle [DONE] message
		if line == "data: [DONE]" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			break
		}

		// Process data lines
		if strings.HasPrefix(line, "data: ") {
			jsonData := strings.TrimPrefix(line, "data: ")

			// Transform OpenRouter chunk to Claude format
			// claudeChunk, err := convertOpenRouterChunkToClaude([]byte(jsonData))
			claudeChunk, err := ConvertOpenAIToAnthropicStream([]byte(jsonData))
			if err != nil {
				logger.Error("Failed to convert OpenRouter chunk", "error", err)
				// Send original chunk if conversion fails
				fmt.Fprintf(w, "%s\n", line)
			} else {
				fmt.Fprintf(w, "data: %s\n", string(claudeChunk))
			}

			// Send an extra newline for SSE format
			fmt.Fprint(w, "\n")

			// Flush the response to ensure real-time streaming
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		} else {
			// Pass through other SSE lines (like event: or id:)
			fmt.Fprintf(w, "%s\n", line)
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
	// transformedBody, err := convertOpenRouterToClaude(respBody)
	transformedBody, err := ConvertOpenAIToAnthropic(respBody)
	if err != nil {
		fmt.Println("Failed to transform OpenRouter body to Claude format", err)
		fmt.Println("Body:\n", string(respBody))
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

// Helper function to detect streaming responses more accurately
func isStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")
	return contentType == "text/event-stream" ||
		strings.Contains(contentType, "stream") ||
		resp.Header.Get("Transfer-Encoding") == "chunked"
}

// convertOpenRouterToClaude converts OpenRouter response format to Claude format
func convertOpenRouterToClaude(openRouterData []byte) ([]byte, error) {
	var orResponse map[string]interface{}

	// Unmarshal OpenRouter response
	if err := json.Unmarshal(openRouterData, &orResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenRouter response: %w", err)
	}

	// Create Claude response structure
	claudeResponse := make(map[string]interface{})

	// Copy ID if present
	if id, ok := orResponse["id"]; ok {
		claudeResponse["id"] = id
	}

	// Set type
	claudeResponse["type"] = "message"

	// Extract role and content from choices[0].message
	if choices, ok := orResponse["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := firstChoice["message"].(map[string]interface{}); ok {
				// Extract role
				if role, ok := message["role"]; ok {
					claudeResponse["role"] = role
				}

				// Convert content from string to Claude's array format
				if content, ok := message["content"].(string); ok {
					claudeResponse["content"] = []map[string]interface{}{
						{
							"type": "text",
							"text": content,
						},
					}
				}

				// Handle other message fields dynamically
				for key, value := range message {
					if key != "role" && key != "content" {
						// Add other message fields with "message_" prefix to avoid conflicts
						claudeResponse["message_"+key] = value
					}
				}
			}

			// Map finish_reason to stop_reason
			if finishReason, ok := firstChoice["finish_reason"]; ok {
				claudeResponse["stop_reason"] = finishReason
			}

			// Map native_finish_reason to stop_sequence if needed
			if nativeFinishReason, ok := firstChoice["native_finish_reason"]; ok {
				// Only set stop_sequence if it's different from finish_reason
				if finishReason, hasFinish := firstChoice["finish_reason"]; !hasFinish || nativeFinishReason != finishReason {
					claudeResponse["stop_sequence"] = nativeFinishReason
				}
			}

			// Handle other choice fields dynamically
			for key, value := range firstChoice {
				if key != "message" && key != "finish_reason" && key != "native_finish_reason" {
					claudeResponse["choice_"+key] = value
				}
			}
		}
	}

	// Copy model if present
	if model, ok := orResponse["model"]; ok {
		claudeResponse["model"] = model
	}

	// Transform usage object
	if usage, ok := orResponse["usage"].(map[string]interface{}); ok {
		claudeUsage := make(map[string]interface{})

		// Map token fields
		if promptTokens, ok := usage["prompt_tokens"]; ok {
			claudeUsage["input_tokens"] = promptTokens
		}
		if completionTokens, ok := usage["completion_tokens"]; ok {
			claudeUsage["output_tokens"] = completionTokens
		}

		// Handle prompt_tokens_details
		if promptDetails, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			if cachedTokens, ok := promptDetails["cached_tokens"]; ok {
				claudeUsage["cache_read_input_tokens"] = cachedTokens
			}
			// Add other prompt token details dynamically
			for key, value := range promptDetails {
				if key != "cached_tokens" {
					claudeUsage["prompt_"+key] = value
				}
			}
		}

		// Handle completion_tokens_details
		if completionDetails, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
			// Add completion token details dynamically
			for key, value := range completionDetails {
				claudeUsage["completion_"+key] = value
			}
		}

		// Add other usage fields dynamically
		for key, value := range usage {
			if key != "prompt_tokens" && key != "completion_tokens" &&
				key != "prompt_tokens_details" && key != "completion_tokens_details" {
				claudeUsage[key] = value
			}
		}

		claudeResponse["usage"] = claudeUsage
	}

	// Handle all other top-level fields dynamically
	for key, value := range orResponse {
		if key != "id" && key != "choices" && key != "model" && key != "usage" {
			// Add with "or_" prefix to distinguish OpenRouter-specific fields
			claudeResponse["or_"+key] = value
		}
	}

	// Default values for Claude-specific fields if not set
	if _, ok := claudeResponse["stop_reason"]; !ok {
		claudeResponse["stop_reason"] = nil
	}
	if _, ok := claudeResponse["stop_sequence"]; !ok {
		claudeResponse["stop_sequence"] = nil
	}

	// Marshal back to JSON
	result, err := json.Marshal(claudeResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Claude response: %w", err)
	}

	return result, nil
}
