/*
Package providers implements the provider abstraction layer for the Claude Code Router.

This package defines the Provider interface and provides implementations for various LLM providers
(OpenRouter, OpenAI, Anthropic) that convert between their native formats and Claude's API format.

# Provider Implementation Guide

This document provides detailed guidance for implementing new providers in the Claude Code Router.
Each provider is responsible for transforming requests and responses between Claude's format and
the target provider's format, supporting both streaming and non-streaming modes.

## Provider Interface

All providers must implement the Provider interface:

	type Provider interface {
		Name() string
		SupportsStreaming() bool
		TransformRequest(request []byte) ([]byte, error)
		TransformResponse(response []byte) ([]byte, error)
		TransformStream(chunk []byte, state *StreamState) ([]byte, error)
		IsStreaming(headers map[string][]string) bool
		GetEndpoint() string
		SetAPIKey(key string)
	}

## Core Concepts

### Request Flow
1. Client sends Claude-format request
2. Router selects provider based on model name
3. **Provider transforms request**: Claude format → Provider format using `TransformRequest()`
4. HTTP request sent to provider
5. **Provider transforms response**: Provider format → Claude format using `TransformResponse()`
6. Response sent back to client

### Content Formats

#### Claude Format (Target)
- **Messages**: Array of content blocks (text, tool_use, tool_result)
- **Tool Definitions**: Objects with name, description, input_schema fields
- **Tool Use**: Objects with id, name, input fields
- **Streaming**: SSE events with content_block_start/delta/stop structure

#### Provider Formats (Source)
- **OpenAI/OpenRouter**: Messages with tool_calls arrays
- **OpenAI/OpenRouter Tool Schema**: Objects with type: "function", function: {name, description, parameters}
- **Different field names**: content vs message, input vs arguments, input_schema vs parameters, etc.
- **Web Search**: OpenRouter annotations for search results
- **Enhanced Usage**: Server tool use metrics, cache information

### Request Transformation (Provider Interface)

Each provider implements bidirectional transformations between Claude and provider formats:

#### Tool Schema Transformation
**Claude → OpenAI/OpenRouter:**
```json
// Claude format

	{
	  "name": "get_weather",
	  "description": "Get current weather",
	  "input_schema": {
	    "type": "object",
	    "properties": {"location": {"type": "string"}},
	    "required": ["location"]
	  }
	}

// Transformed to OpenAI/OpenRouter format

	{
	  "type": "function",
	  "function": {
	    "name": "get_weather",
	    "description": "Get current weather",
	    "parameters": {
	      "type": "object",
	      "properties": {"location": {"type": "string"}},
	      "required": ["location"]
	    }
	  }
	}

```

#### Tool Choice Validation
- **Removes** `tool_choice` when no tools are provided
- **Removes** `tool_choice` when tools array is empty or null
- **Preserves** `tool_choice` when valid tools are present
- **Prevents** "tool_choice may only be specified while providing tools" errors

## Implementation Steps

### Important Note: Request vs Response Transformation

**Request Transformation** (Claude → Provider format) is handled by **individual providers** using the `TransformRequest()` method. This includes:
- Tool schema transformation (input_schema → parameters)
- Field removal (cache_control, tool_choice validation)
- Message format standardization
- System message handling (Claude → Provider specific format)

**Response Transformation** (Provider → Claude format) is handled by **individual providers** using the `TransformResponse()` method. This includes:
- Content structure conversion
- Tool call format transformation
- Usage metrics conversion
- Streaming event generation

### 1. Basic Provider Structure

Create a new provider struct and implement basic methods:

	type NewProvider struct {
		name     string
		endpoint string
		apiKey   string
	}

	func NewNewProvider() *NewProvider {
		return &NewProvider{
			name: "new-provider",
		}
	}

	func (p *NewProvider) Name() string {
		return p.name
	}

	func (p *NewProvider) SupportsStreaming() bool {
		return true // or false
	}

	func (p *NewProvider) GetEndpoint() string {
		return p.endpoint
	}

	func (p *NewProvider) SetAPIKey(key string) {
		p.apiKey = key
	}

### 2. Streaming Detection

Implement IsStreaming to detect if a response is streamed:

	func (p *NewProvider) IsStreaming(headers map[string][]string) bool {
		// Check for streaming indicators in response headers
		if contentType, ok := headers["Content-Type"]; ok {
			for _, ct := range contentType {
				if ct == "text/event-stream" || strings.Contains(ct, "stream") {
					return true
				}
			}
		}
		return false
	}

### 3. Request Transformation

Implement TransformRequest for converting Claude requests to provider format:

	func (p *NewProvider) TransformRequest(request []byte) ([]byte, error) {
		var claudeRequest map[string]interface{}
		if err := json.Unmarshal(request, &claudeRequest); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Claude request: %w", err)
		}

		// Convert Claude format to provider format
		providerRequest := p.convertFromClaude(claudeRequest)

		return json.Marshal(providerRequest)
	}

Note: This method transforms Claude requests TO provider format.

### 4. Response Transformation

Implement TransformResponse for complete responses (Provider → Claude format):

	func (p *NewProvider) TransformResponse(response []byte) ([]byte, error) {
		var providerResponse map[string]interface{}
		if err := json.Unmarshal(response, &providerResponse); err != nil {
			return nil, fmt.Errorf("failed to unmarshal provider response: %w", err)
		}

		// Convert to Claude format
		claudeResponse := p.convertToClaude(providerResponse)

		return json.Marshal(claudeResponse)
	}

Note: This method transforms provider responses TO Claude format.

### 5. Streaming Response Transformation

Implement TransformStream for real-time chunk processing (Provider → Claude format):

	func (p *NewProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
		var providerChunk map[string]interface{}
		if err := json.Unmarshal(chunk, &providerChunk); err != nil {
			return nil, fmt.Errorf("failed to unmarshal chunk: %w", err)
		}

		// Initialize state if needed
		if state.ContentBlocks == nil {
			state.ContentBlocks = make(map[int]*ContentBlockState)
		}

		var events []byte

		// Handle different chunk types...
		return events, nil
	}

Note: This method transforms provider streaming responses TO Claude format. Request
transformation (including tool schema) is handled by the TransformRequest method.

## Streaming Implementation Details

### StreamState Management

The StreamState tracks streaming conversion across multiple chunks:

	type StreamState struct {
		MessageStartSent  bool
		MessageID         string
		Model             string
		InitialUsage      map[string]interface{}
		ContentBlocks     map[int]*ContentBlockState
		CurrentIndex      int
	}

	type ContentBlockState struct {
		Type          string // "text" or "tool_use"
		StartSent     bool
		StopSent      bool
		ToolCallID    string // For tool_use blocks
		ToolCallIndex int    // OpenRouter tool call index for tracking across chunks
		ToolName      string // For tool_use blocks
		Arguments     string // Accumulated arguments for tool_use blocks
	}

Key principles:
- **Initialize** ContentBlocks map on first use
- **Track** multiple content blocks by index
- **Manage** start/stop events per content block
- **Accumulate** partial data (like tool arguments)
- **Handle** multiple tool calls in single response
- **Generate** proper input_json_delta events for tool arguments

### Content Block Types

#### Text Content Blocks
For streaming text responses:

	// Check for text content
	if content, ok := delta["content"].(string); ok && content != "" {
		// Get or create text content block
		if _, exists := state.ContentBlocks[state.CurrentIndex]; !exists {
			state.ContentBlocks[state.CurrentIndex] = &ContentBlockState{
				Type: "text",
			}
		}

		contentBlock := state.ContentBlocks[state.CurrentIndex]

		// Send content_block_start if needed
		if !contentBlock.StartSent {
			startEvent := map[string]interface{}{
				"type":  "content_block_start",
				"index": state.CurrentIndex,
				"content_block": map[string]interface{}{
					"type": "text",
					"text": "",
				},
			}
			events = append(events, formatSSEEvent("content_block_start", startEvent)...)
			contentBlock.StartSent = true
		}

		// Send content_block_delta
		deltaEvent := map[string]interface{}{
			"type":  "content_block_delta",
			"index": state.CurrentIndex,
			"delta": map[string]interface{}{
				"type": "text_delta",
				"text": content,
			},
		}
		events = append(events, formatSSEEvent("content_block_delta", deltaEvent)...)
	}

#### Tool Use Content Blocks
For streaming tool calls:

	// Check for tool calls
	if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
		for _, toolCall := range toolCalls {
			if tcMap, ok := toolCall.(map[string]interface{}); ok {
				// Process individual tool call
				toolCallID, _ := tcMap["id"].(string)

				// Find or create content block for this tool call
				var contentBlockIndex int = -1
				for idx, block := range state.ContentBlocks {
					if block.Type == "tool_use" && block.ToolCallID == toolCallID {
						contentBlockIndex = idx
						break
					}
				}

				if contentBlockIndex == -1 {
					state.CurrentIndex++
					contentBlockIndex = state.CurrentIndex
					state.ContentBlocks[contentBlockIndex] = &ContentBlockState{
						Type:       "tool_use",
						ToolCallID: toolCallID,
						// ... other fields
					}
				}

				// Send content_block_start for tool_use if not sent
				if !contentBlock.StartSent {
					claudeToolID := "toolu_" + strings.TrimPrefix(toolCallID, "call_")
					startEvent := map[string]interface{}{
						"type":  "content_block_start",
						"index": contentBlockIndex,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    claudeToolID,
							"name":  functionName,
							"input": map[string]interface{}{},
						},
					}
					events = append(events, formatSSEEvent("content_block_start", startEvent)...)
					contentBlock.StartSent = true
				}

				// Send input_json_delta for streaming arguments
				// Handle both incremental and non-incremental argument updates
				if arguments != "" && arguments != contentBlock.Arguments {
					var newPart string

					// Check if arguments are incremental (common case)
					if len(arguments) > len(contentBlock.Arguments) &&
					   strings.HasPrefix(arguments, contentBlock.Arguments) {
						newPart = arguments[len(contentBlock.Arguments):]
					} else {
						// Non-incremental update - send entire new part
						newPart = arguments
					}

					contentBlock.Arguments = arguments

					deltaEvent := map[string]interface{}{
						"type":  "content_block_delta",
						"index": contentBlockIndex,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": newPart,
						},
					}
					events = append(events, formatSSEEvent("content_block_delta", deltaEvent)...)
				}
			}
		}
	}

### Claude Streaming Event Format

Generate events in Claude's expected format:

	// Message start (once per response)
	event: message_start
	data: {"type": "message_start", "message": {...}}

	// Content block start (once per content block)
	event: content_block_start
	data: {"type": "content_block_start", "index": 0, "content_block": {...}}

	// Content deltas (multiple per content block)
	event: content_block_delta
	data: {"type": "content_block_delta", "index": 0, "delta": {...}}

	// Content block stop (once per content block)
	event: content_block_stop
	data: {"type": "content_block_stop", "index": 0}

	// Message delta (once per response)
	event: message_delta
	data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}}

	// Message stop (once per response)
	event: message_stop
	data: {"type": "message_stop"}

## Field Mapping Reference

### Common Transformations

#### OpenAI/OpenRouter → Claude (Response Transformation)

**Message Structure:**
- `choices[0].message.content` → `content[0].text`
- `choices[0].message.tool_calls` → `content[].type: "tool_use"`
- `choices[0].finish_reason` → `stop_reason`

**Tool Calls:**
- `tool_calls[].id` → `id` (with prefix conversion: "call_" → "toolu_")
- `tool_calls[].function.name` → `name`
- `tool_calls[].function.arguments` → `input` (JSON parsed)

#### Claude → OpenAI/OpenRouter (Request Transformation - Handled by Proxy)

**Tool Schema:**
- `name` → `function.name` (preserved)
- `description` → `function.description` (preserved)
- `input_schema` → `function.parameters` (schema structure preserved)
- Wrapped in: `{"type": "function", "function": {...}}`

**Tool Choice Validation:**
- `tool_choice` removed if `tools` is missing, null, or empty array
- `tool_choice` preserved if valid `tools` array is provided

**Usage/Tokens:**
- `usage.prompt_tokens` → `usage.input_tokens`
- `usage.completion_tokens` → `usage.output_tokens`
- `usage.prompt_tokens_details.cached_tokens` → `usage.cache_read_input_tokens`
- `usage.cache_creation_input_tokens` → `usage.cache_creation_input_tokens` (preserved)
- `usage.server_tool_use.web_search_requests` → `usage.server_tool_use.web_search_requests` (preserved)

**Stop Reasons:**
- `"stop"` → `"end_turn"`
- `"length"` → `"max_tokens"`
- `"tool_calls"` → `"tool_use"`
- `"function_call"` → `"tool_use"`

### Content Block Structure

#### Text Content Block

	{
		"type": "text",
		"text": "response content"
	}

#### Tool Use Content Block

	{
		"type": "tool_use",
		"id": "toolu_01ABC123",
		"name": "function_name",
		"input": {
			"param1": "value1",
			"param2": "value2"
		}
	}

## Error Handling

### Transformation Errors
- **Log** but don't fail - return original response if transformation fails
- **Validate** required fields before transformation
- **Handle** missing or malformed data gracefully

### Streaming Errors
- **Skip** malformed chunks rather than stopping stream
- **Log** errors for debugging but continue processing
- **Reset** state if corruption detected

## Testing

### Unit Tests Required

#### Provider Tests (Both Request and Response Transformation)
1. **Basic Methods**: Name, SupportsStreaming, etc.
2. **Request Transform**: Claude format → Provider format conversion
3. **Response Transform**: Provider format → Claude format conversion
4. **Tool Calls Transform**: Tool call conversion and ID mapping
5. **Web Search Annotations**: Annotation and server tool use preservation
6. **Streaming Transform**: Chunk-by-chunk conversion with tool calls
7. **Streaming State**: Multiple content blocks, tool calls
8. **Stop Reason Mapping**: All provider-specific stop reasons
9. **Error Cases**: Malformed input, missing fields

### Test Data
- Use real examples from provider documentation
- Test edge cases: empty responses, mixed content types
- Test tool calling with streaming and non-streaming modes
- Test web search responses with annotations
- Verify Claude format compliance and SSE event structure

## Registration

Add provider to registry in `registry.go`:

	func (r *Registry) Initialize() {
		r.Register(NewOpenRouterProvider())
		r.Register(NewOpenAIProvider())
		r.Register(NewAnthropicProvider())
		r.Register(NewNewProvider()) // Add your provider
	}

Add domain mapping for automatic provider selection:

	domainProviderMap := map[string]string{
		"openrouter.ai":     "openrouter",
		"api.openai.com":    "openai",
		"api.anthropic.com": "anthropic",
		"your-provider.com": "new-provider", // Add your domain
	}

## Best Practices

### Code Organization
- **Separate** transformation logic from streaming logic
- **Use** helper methods for complex transformations
- **Document** field mappings in comments
- **Handle** provider-specific quirks explicitly

### Performance
- **Minimize** JSON marshaling/unmarshaling in hot paths
- **Reuse** objects where possible
- **Stream** efficiently without buffering entire responses

### Compatibility
- **Follow** Claude's API format exactly
- **Test** with real Claude API responses
- **Handle** version differences gracefully
- **Document** provider limitations

## Reference Implementations

See existing implementations for detailed examples:

### Provider Examples (Request and Response Transformation)
- **OpenRouter** (`openrouter.go`): Full implementation with bidirectional transformation and tool calling
- **OpenAI** (`openai.go`): Similar to OpenRouter with minor differences
- **Gemini** (`gemini.go`): Different API format requiring custom transformation
- **Nvidia** (`nvidia.go`): OpenAI-compatible format with minor variations
- **Anthropic** (`anthropic.go`): Pass-through implementation for requests, identity transformation

The OpenRouter provider is the most complete reference implementation,
including comprehensive streaming tool call support, web search annotations,
enhanced usage information handling with server tool use metrics, and full
bidirectional transformation between Claude and OpenAI formats.

## Common Issues and Solutions

### "tool_choice may only be specified while providing tools"
**Cause**: Claude Code sends `tool_choice` with empty/missing tools array
**Solution**: Provider `TransformRequest()` method automatically removes `tool_choice` when tools are invalid
**Prevention**: Always validate tool_choice/tools relationship in request transformation

### Tool Schema Mismatch
**Cause**: Claude uses `input_schema`, OpenAI/OpenRouter use `parameters`
**Solution**: Provider `TransformRequest()` method transforms `input_schema` → `parameters` automatically
**Prevention**: Ensure tool definitions follow Claude format in client requests

### Empty Tool Parameters in Streaming (OpenRouter)
**Cause**: OpenRouter sends tool calls in multiple chunks - first chunk has ID/name, subsequent chunks have only index and arguments
**Solution**: Track tool calls by both ID and index, handle non-incremental argument updates
**Prevention**: Use `ToolCallIndex` field to identify tool calls across streaming chunks

**OpenRouter Streaming Pattern**:
- First chunk: `{"id":"toolu_123","index":0,"function":{"name":"LS","arguments":""}}`
- Later chunks: `{"index":0,"function":{"arguments":"{\"path\""}}`
- Final chunks: `{"index":0,"function":{"arguments":":\"/home\"}"}`

**Implementation Notes**:
- Arguments may not always be incremental - handle bounds checking
- Empty tool names/IDs in subsequent chunks are normal
- Use `ToolCallIndex` to track tool calls when ID is missing
*/
package providers
