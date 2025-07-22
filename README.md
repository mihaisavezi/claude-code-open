# Claude Code Router

A production-ready LLM proxy server that converts requests from various LLM providers to Anthropic's Claude API format. Built with Go for high performance and reliability.

## Features

- **Multi-Provider Support**: Currently supports OpenRouter, OpenAI, and Anthropic providers
- **Dynamic Request Transformation**: Automatically converts requests from any supported provider format to Anthropic's Claude API format
- **Dynamic Model Selection**: Support for explicit provider/model selection using comma notation (e.g., `openrouter,anthropic/claude-sonnet-4`)
- **Streaming Support**: Full support for streaming responses with proper SSE formatting and tool calling
- **Advanced Tool Calling**: Complete streaming tool call support with proper Claude SSE event generation
- **Error Transparency**: Upstream errors (status != 200) are forwarded without transformation to preserve original error details
- **Modular Architecture**: Clean, extensible design that makes adding new providers straightforward
- **Production Ready**: Comprehensive error handling, logging, and process management
- **CLI Interface**: Intuitive command-line interface for easy management
- **Process Management**: Automatic service lifecycle management with reference counting

## Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/your-username/claude-code-router-go
cd claude-code-router-go

# Build the application
go build -o ccr .

# Install globally (optional)
sudo mv ccr /usr/local/bin/
```

### Configuration

Initialize your configuration:

```bash
ccr config init
```

This will prompt you for:
- Provider name (e.g., `openrouter`, `openai`)
- API key for the provider
- API base URL
- Default model
- Optional router authentication key

### Usage

Start the router service:

```bash
ccr start
```

Use Claude Code with the router:

```bash
ccr code [claude-code-arguments]
```

Check service status:

```bash
ccr status
```

Stop the service:

```bash
ccr stop
```

## Dynamic Model Selection

The router supports explicit provider and model selection using comma notation, which overrides all automatic routing logic:

### Explicit Provider Selection

Instead of relying on the configured routing rules, you can specify exactly which provider and model to use:

```json
{
  "model": "openrouter,anthropic/claude-sonnet-4",
  "messages": [
    {"role": "user", "content": "Hello"}
  ]
}
```

This format (`provider,model`) will:
- Use the specified provider (must be configured in your config)
- Use the exact model name with that provider
- Bypass all automatic routing rules (long context, background, etc.)
- Preserve model suffixes like `:online` for web search

### Examples

```json
// Use OpenRouter with a specific Anthropic model
{"model": "openrouter,anthropic/claude-sonnet-4"}

// Use OpenRouter with web search enabled
{"model": "openrouter,anthropic/claude-sonnet-4:online"}

// Use OpenAI directly
{"model": "openai,gpt-4o"}

// Regular model name (uses automatic routing)
{"model": "claude-3-5-sonnet"}
```

### Automatic Routing (Fallback)

When no comma is present in the model name, the router applies these rules in order:

1. **Long Context**: If tokens > 60,000 → use `LongContext` config
2. **Background Tasks**: If model starts with "claude-3-5-haiku" → use `Background` config  
3. **Default Routing**: Use `Think`, `WebSearch`, or model as-is

## Architecture

### Core Components

- **`internal/config/`** - Configuration management
- **`internal/providers/`** - Provider implementations and registry
- **`internal/server/`** - HTTP server and routing
- **`internal/handlers/`** - Request handlers (proxy, health)
- **`internal/middleware/`** - HTTP middleware (auth, logging)
- **`internal/process/`** - Process lifecycle management
- **`cmd/`** - CLI command implementations

### Provider System

The router uses a modular provider system where each provider implements the `Provider` interface:

```go
type Provider interface {
    Name() string
    SupportsStreaming() bool
    Transform(request []byte) ([]byte, error)
    TransformStream(chunk []byte, state *StreamState) ([]byte, error)
    IsStreaming(headers map[string][]string) bool
    GetEndpoint() string
    SetAPIKey(key string)
}
```

### Request Flow

1. Client sends request to router
2. Router authenticates request (if API key configured)
3. Router selects appropriate model based on routing configuration
4. Router identifies provider based on configuration
5. Request is transformed by provider-specific transformer
6. Router proxies request to upstream provider
7. Response is transformed back to Claude format
8. Router streams response to client

## Configuration

### Configuration File Location

- Linux/macOS: `~/.claude-code-router/config.json`
- Windows: `%USERPROFILE%\.claude-code-router\config.json`

### Configuration Format

```json
{
  "HOST": "127.0.0.1",
  "PORT": 6970,
  "APIKEY": "your-router-api-key-optional",
  "Providers": [
    {
      "name": "openrouter",
      "api_base_url": "https://openrouter.ai/api/v1/chat/completions",
      "api_key": "your-provider-api-key",
      "models": ["anthropic/claude-3.5-sonnet"]
    }
  ],
  "Router": {
    "default": "openrouter,anthropic/claude-3.5-sonnet",
    "think": "openrouter,anthropic/claude-3.5-sonnet",
    "longContext": "openrouter,anthropic/claude-3.5-sonnet-20241022",
    "background": "openrouter,anthropic/claude-3.5-haiku",
    "webSearch": "openrouter,perplexity/llama-3.1-sonar-large-128k-online"
  }
}
```

### Router Configuration

- **`default`**: Default model to use when no specific model is requested
- **`think`**: Model for complex reasoning tasks
- **`longContext`**: Model for requests with >60k tokens
- **`background`**: Model for background/batch processing
- **`webSearch`**: Model for web search enabled tasks

Model format: `provider_name,model_name`

## Commands

### Service Management

```bash
# Start the router service
ccr start [--verbose] [--log-file]

# Stop the router service  
ccr stop

# Check service status
ccr status
```

### Configuration Management

```bash
# Initialize configuration interactively
ccr config init

# Show current configuration
ccr config show

# Validate configuration
ccr config validate
```

### Claude Code Integration

```bash
# Run Claude Code through the router
ccr code [args...]

# Examples:
ccr code --help
ccr code "Write a Python script to sort a list"
ccr code --resume session-name
```

## Adding New Providers

To add support for a new LLM provider:

1. **Create Provider Implementation**:
   ```go
   // internal/providers/newprovider.go
   type NewProvider struct {
       name     string
       endpoint string
       apiKey   string
   }
   
   func (p *NewProvider) Transform(request []byte) ([]byte, error) {
       // Implement request transformation logic
   }
   
   func (p *NewProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
       // Implement streaming transformation logic
   }
   ```

2. **Register Provider**:
   ```go
   // internal/providers/registry.go
   func (r *Registry) Initialize() {
       r.Register(NewOpenRouterProvider())
       r.Register(NewOpenAIProvider())
       r.Register(NewAnthropicProvider())
       r.Register(NewYourProvider()) // Add here
   }
   ```

3. **Update Domain Mapping**:
   ```go
   // internal/providers/registry.go
   domainProviderMap := map[string]string{
       "your-provider.com": "yourprovider",
       // ... existing mappings
   }
   ```

## Development

### Prerequisites

- Go 1.24.4 or later
- Access to LLM provider APIs (OpenRouter, OpenAI, etc.)

### Development

```bash
# Development with hot reload (automatically installs Air if needed)
make dev

# This will:
# - Install Air if not present
# - Start the server with `ccr start --verbose`
# - Watch for Go file changes
# - Automatically rebuild and restart on changes
```

### Building

```bash
# Build for current platform
go build -o ccr .

# Or use Makefile
make build

# Build for multiple platforms
make build-all

# Manual cross-compilation
GOOS=linux GOARCH=amd64 go build -o ccr-linux-amd64 .
GOOS=darwin GOARCH=amd64 go build -o ccr-darwin-amd64 .
GOOS=windows GOARCH=amd64 go build -o ccr-windows-amd64.exe .
```

### Testing

```bash
# Run tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run specific provider tests
go test ./internal/providers/...
```

### Dependencies

Key dependencies:
- `github.com/spf13/cobra` - CLI framework
- `github.com/fatih/color` - Terminal colors
- `github.com/pkoukk/tiktoken-go` - Token counting
- `github.com/fsnotify/fsnotify` - Config file watching
- `github.com/andybalholm/brotli` - Brotli compression

## Production Deployment

### Systemd Service (Linux)

Create `/etc/systemd/system/claude-code-router.service`:

```ini
[Unit]
Description=Claude Code Router
After=network.target

[Service]
Type=simple
User=your-user
ExecStart=/usr/local/bin/ccr start
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl enable claude-code-router
sudo systemctl start claude-code-router
```

### Docker

```dockerfile
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY . .
RUN go build -o ccr .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/ccr .
CMD ["./ccr", "start"]
```

### Environment Variables

The router respects these environment variables:

- `CCR_HOST` - Override host binding
- `CCR_PORT` - Override port binding
- `CCR_CONFIG_PATH` - Override config file path
- `CCR_LOG_LEVEL` - Set log level (debug, info, warn, error)

## Monitoring

### Health Check

```bash
curl http://localhost:6970/health
```

### Logs

Logs include structured information about:
- Request routing and provider selection
- Token usage (input/output)
- Response times and status codes
- Error conditions and debugging info

### Metrics

The router provides basic operational metrics through logs:
- Request count and response times
- Token usage statistics
- Provider response status codes
- Error rates by provider

## Troubleshooting

### Common Issues

**Service won't start:**
- Check configuration with `ccr config validate`
- Ensure port is available with `netstat -ln | grep :6970`
- Check logs with `ccr start --verbose`

**Authentication errors:**
- Verify provider API keys in configuration
- Check router API key if authentication is enabled
- Ensure Claude Code environment variables are set correctly

**Transformation errors:**
- Enable verbose logging to see transformation details
- Check provider compatibility
- Verify request format matches expected provider schema

**Performance issues:**
- Monitor token usage in logs
- Consider using faster models for background tasks
- Check network latency to provider APIs

### Debug Mode

```bash
ccr start --verbose
```

This enables detailed logging of:
- Request/response transformations
- Provider selection logic
- Token counting details
- HTTP request/response details

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Code Style

- Use `gofmt` for formatting
- Follow Go naming conventions
- Add tests for new features
- Update documentation

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Changelog

### v0.2.0
- Complete refactor with modular architecture
- Support for multiple providers (OpenRouter, OpenAI, Anthropic)
- Improved CLI interface
- Production-ready error handling and logging
- Configuration management system
- Process lifecycle management

### v0.1.0
- Initial proof-of-concept implementation
- Basic OpenRouter support
- Simple proxy functionality