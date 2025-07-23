# Claude Code Open

A wrapper around Claude Code that actually allows you to connect it to any other LLM provider. Contribute to add providers

Production-ready LLM proxy server that converts requests from various LLM providers to Anthropic's Claude API format. Built with Go for high performance and reliability.

This project is inspired by [Clude Code Router](https://github.com/musistudio/claude-code-router), the project was buggy and didn't work for me. This fixes that.

## Features

- **Multi-Provider Support**: Supports 5 major LLM providers:
  - **OpenRouter**: Access to multiple models from different providers
  - **OpenAI**: Direct access to GPT models (GPT-4, GPT-4-turbo, GPT-3.5, etc.)
  - **Anthropic**: Direct access to Claude models (Claude-3.5-Sonnet, Claude-3-Opus, Claude-3-Haiku)
  - **Nvidia**: Access to Nemotron models via Nvidia's LLM API (bit buggy still)
  - **Google Gemini**: Access to Gemini models (Gemini-2.0-Flash, Gemini-1.5-Pro, etc.)
- **Zero-Config Setup**: Run immediately with just `CCO_API_KEY` environment variable - no config file required
- **YAML Configuration**: Modern YAML configuration with automatic defaults and model whitelists
- **Dynamic Request Transformation**: Automatically converts requests from any supported provider format to Anthropic's Claude API format
- **Dynamic Model Selection**: Support for explicit provider/model selection using comma notation (e.g., `openrouter,anthropic/claude-sonnet-4`)
- **Model Whitelisting**: Filter available models per provider using pattern matching
- **Default Model Management**: Automatically populated model lists with smart defaults for each provider
- **API Key Protection**: Optional proxy-level authentication for added security

## Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/your-username/claude-code-open
cd claude-code-open

# Build the application
go build -o cco .

# Install globally (optional)
sudo mv cco /usr/local/bin/
```

### Quick Start with CCO_API_KEY

For the fastest setup, you can run without any configuration file using just the `CCO_API_KEY` environment variable:

```bash
# Set your API key (works with any provider)
# This is the API key of the provider you want to use, can be any one of the supported providers
# Then in Claude Code you set the model with <provider>,<model name> e.g. openrouter,moonshotai/kimi-k2
export CCO_API_KEY="your-api-key-here"

# Start the router immediately - no config file needed!
# Although you can create a config if you want to store your API keys for all providers. See cco config
cco start

# The API key will be used for whichever provider your model requests
# e.g., if you use "openrouter,anthropic/claude-3.5-sonnet" -> key goes to OpenRouter
# e.g., if you use "openai,gpt-4o" -> key goes to OpenAI
```

**How CCO_API_KEY works:**
- **Single API Key**: Use one API key environment variable for all providers
- **Provider Detection**: The key is automatically sent to the correct provider based on your model selection
- **No Config Required**: Run immediately without creating any configuration files, although config file is an option
- **Fallback Priority**: Provider-specific keys in config files take precedence over CCO_API_KEY

### Full Configuration (Optional)

For advanced setups with multiple API keys, generate a complete YAML configuration:

```bash
cco config generate
```

This creates `config.yaml` with all 5 supported providers and sensible defaults. Then edit the file to add your API keys:

```yaml
# config.yaml
host: 127.0.0.1
port: 6970
api_key: your-proxy-key  # Optional: protect the proxy

providers:
  - name: openrouter
    api_key: your-openrouter-api-key
    model_whitelist: ["claude", "gpt-4"]  # Optional: filter models
  - name: openai
    api_key: your-openai-api-key
  # ... etc
```

Alternatively, use the interactive setup:

```bash
cco config init
```

### Usage

Start the router service:

```bash
cco start  # For if you want to start the proxy server separately. 
```

Use Claude Code with the router:

```bash
cco code [claude-code-arguments]  # You can also run this directly without starting the server first, it will auto start
```

Check service status:

```bash
cco status
```

Stop the service:

```bash
cco stop
```

## Dynamic Model Selection

The router supports explicit provider and model selection using comma notation, which overrides all automatic routing logic:

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
    TransformRequest(request []byte) ([]byte, error)
    TransformResponse(response []byte) ([]byte, error)
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
5. Provider transforms request from Claude format to provider format using `TransformRequest()`
6. Router proxies request to upstream provider
7. Provider transforms response from provider format back to Claude format using `TransformResponse()`
8. Router streams response to client

## Configuration

### Configuration File Location

- Linux/macOS: `~/.claude-code-open/config.yaml` (preferred) or `config.json`
- Windows: `%USERPROFILE%\.claude-code-open\config.yaml` (preferred) or `config.json`

**Backward Compatibility**: The router will also check `~/.claude-code-router/` for existing configurations and use them automatically, with a migration notice.

### YAML Configuration Format (Recommended)

The router now supports modern YAML configuration with automatic defaults:

```yaml
# Server settings
host: 127.0.0.1
port: 6970
api_key: your-proxy-key-here  # Optional: protect proxy with authentication

# Provider configurations  
providers:
  # OpenRouter - Access to multiple models
  - name: openrouter
    api_key: your-openrouter-api-key
    # url: auto-populated from defaults
    # default_models: auto-populated with curated list
    model_whitelist: ["claude", "gpt-4"]  # Optional: filter models by pattern

  # OpenAI - Direct GPT access
  - name: openai
    api_key: your-openai-api-key
    # Automatically configured with GPT-4, GPT-4-turbo, GPT-3.5-turbo

  # Anthropic - Direct Claude access
  - name: anthropic
    api_key: your-anthropic-api-key
    # Automatically configured with Claude models

  # Nvidia - Nemotron models
  - name: nvidia 
    api_key: your-nvidia-api-key

  # Google Gemini
  - name: gemini
    api_key: your-gemini-api-key

# Router configuration for different use cases
router:
  default: openrouter/anthropic/claude-3.5-sonnet
  think: openai/o1-preview
  long_context: anthropic/claude-3-5-sonnet-20241022
  background: anthropic/claude-3-haiku-20240307
  web_search: openrouter/perplexity/llama-3.1-sonar-huge-128k-online
```

### Legacy JSON Format

The router still supports JSON configuration for backward compatibility:

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
      "models": ["anthropic/claude-3.5-sonnet"],
      "model_whitelist": ["claude", "gpt-4"],
      "default_models": ["anthropic/claude-3.5-sonnet"]
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

### Configuration Features

- **Auto-Defaults**: URLs and model lists are automatically populated for all providers
- **YAML Priority**: YAML configuration takes precedence over JSON if both exist
- **Model Whitelisting**: Use `model_whitelist` to filter models by pattern (e.g., `["claude", "gpt-4"]`)
- **Smart Model Management**: Default models are automatically filtered by whitelists
- **Proxy Protection**: Optional `api_key` field protects the entire proxy with authentication

### Router Configuration

- **`default`**: Default model to use when no specific model is requested
- **`think`**: Model for complex reasoning tasks (e.g., o1-preview)
- **`long_context`**: Model for requests with >60k tokens
- **`background`**: Model for background/batch processing  
- **`web_search`**: Model for web search enabled tasks

Model format: `provider_name/model_name` (e.g., `openai/gpt-4o`, `anthropic/claude-3-5-sonnet`)

## Commands

### Service Management

```bash
# Start the router service
cco start [--verbose] [--log-file]

# Stop the router service  
cco stop

# Check service status
cco status
```

### Configuration Management

```bash
# Generate example YAML configuration with all providers
cco config generate

# Generate and overwrite existing configuration
cco config generate --force

# Initialize configuration interactively
cco config init

# Show current configuration (displays format: YAML/JSON)
cco config show

# Validate configuration
cco config validate
```

### Claude Code Integration

```bash
# Run Claude Code through the router
cco code [args...]

# Examples:
cco code --help
cco code "Write a Python script to sort a list"
cco code --resume session-name
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
   
   func (p *NewProvider) TransformRequest(request []byte) ([]byte, error) {
       // Implement Claude → Provider format transformation
   }
   
   func (p *NewProvider) TransformResponse(response []byte) ([]byte, error) {
       // Implement Provider → Claude format transformation
   }
   
   func (p *NewProvider) TransformStream(chunk []byte, state *StreamState) ([]byte, error) {
       // Implement streaming response transformation (Provider → Claude format)
   }
   ```

2. **Register Provider**:
   ```go
   // internal/providers/registry.go
   func (r *Registry) Initialize() {
       r.Register(NewOpenRouterProvider())
       r.Register(NewOpenAIProvider())
       r.Register(NewAnthropicProvider())
       r.Register(NewNvidiaProvider())
       r.Register(NewGeminiProvider())
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
# - Start the server with `cco start --verbose`
# - Watch for Go file changes
# - Automatically rebuild and restart on changes
```

### Building

```bash
# Build for current platform
go build -o cco .

# Or use Makefile
make build

# Or use Taskfile (modern alternative)
task build

# Build for multiple platforms
make build-all      # or
task build-all

# Manual cross-compilation
GOOS=linux GOARCH=amd64 go build -o cco-linux-amd64 .
GOOS=darwin GOARCH=amd64 go build -o cco-darwin-amd64 .
GOOS=windows GOARCH=amd64 go build -o cco-windows-amd64.exe .
```

### Testing

```bash
# Run tests
go test ./...

# Or use task runners
make test           # or
task test

# Run tests with coverage
go test -cover ./...
make coverage       # or  
task test-coverage

# Run specific provider tests
go test ./internal/providers/...

# Additional task commands
task benchmark      # Run benchmarks
task security       # Security audit with gosec
task check          # Comprehensive checks (fmt, lint, test, security)
```

### Task Runner

The project includes both a traditional `Makefile` and a modern `Taskfile.yml` for task automation. [Task](https://taskfile.dev/) provides more powerful features and better cross-platform support.

**Available tasks:**
```bash
# Core development tasks
task build              # Build the binary
task test               # Run tests 
task fmt                # Format code
task lint               # Run linter
task clean              # Clean build artifacts

# Advanced tasks
task dev                # Development mode with hot reload
task build-all          # Cross-platform builds
task test-coverage      # Tests with coverage report
task benchmark          # Run benchmarks
task security           # Security audit
task check              # All checks (fmt, lint, test, security)

# Service management
task start              # Start the service (builds first)
task stop               # Stop the service
task status             # Check service status

# Configuration
task config-generate    # Generate example config
task config-validate    # Validate current config

# Utilities
task deps               # Download dependencies
task mod-update         # Update all dependencies
task docs               # Start documentation server
task install            # Install to system
task release            # Create release build
```

## Production Deployment

### Systemd Service (Linux)

Create `/etc/systemd/system/claude-code-open.service`:

```ini
[Unit]
Description=Claude Code Open
After=network.target

[Service]
Type=simple
User=your-user
ExecStart=/usr/local/bin/cco start
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl enable claude-code-open
sudo systemctl start claude-code-open
```

### Environment Variables

The router respects these environment variables:

- `CCO_API_KEY` - **Universal API key for all providers** (see Quick Start section)
- `CCO_HOST` - Override host binding
- `CCO_PORT` - Override port binding
- `CCO_CONFIG_PATH` - Override config file path
- `CCO_LOG_LEVEL` - Set log level (debug, info, warn, error)

#### CCO_API_KEY Behavior

The `CCO_API_KEY` environment variable provides a simple way to use a single API key across all providers:

1. **No Config File**: If no configuration file exists and `CCO_API_KEY` is set, the router creates a minimal configuration with all providers
2. **Config File Exists**: If a config file exists, `CCO_API_KEY` serves as a fallback for providers without specific API keys
3. **Provider Selection**: The API key is sent to whichever provider you request:
   - `openrouter,anthropic/claude-3.5-sonnet` → API key sent to OpenRouter
   - `openai,gpt-4o` → API key sent to OpenAI  
   - `anthropic,claude-3-haiku-20240307` → API key sent to Anthropic
4. **Priority**: Provider-specific API keys in configuration files take precedence over `CCO_API_KEY`

```bash
# Use your OpenAI API key directly with OpenAI
export CCO_API_KEY="sk-your-openai-key"
cco start

# This request will use your OpenAI key:
# - "openai,gpt-4o"
```

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
- Check configuration with `cco config validate`
- Ensure port is available with `netstat -ln | grep :6970`
- Check logs with `cco start --verbose`

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
cco start --verbose
```

This enables detailed logging of:
- Request/response transformations
- Provider selection logic
- Token counting details
- HTTP request/response details

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Changelog

### v0.3.0
- **New Providers**: Added Nvidia and Google Gemini support (5 total providers)
- **YAML Configuration**: Modern YAML config with automatic defaults and smart model management
- **Model Whitelisting**: Filter available models per provider using pattern matching
- **API Key Protection**: Optional proxy-level authentication for enhanced security
- **Enhanced CLI**: New `cco config generate` command creates complete YAML configuration
- **Comprehensive Testing**: 100% test coverage for all providers including streaming and tool calls
- **Default Model Management**: Auto-populated curated model lists for all providers
- **Streaming Tool Calls**: Fixed complex streaming tool parameter issues across all providers

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
