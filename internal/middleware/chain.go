package middleware

import (
	"log/slog"
	"net/http"

	"github.com/mihaisavezi/claude-code-open/internal/config"
)

// Middleware represents a middleware function
type Middleware func(http.Handler) http.Handler

// Chain represents a middleware chain
type Chain struct {
	middlewares []Middleware
}

// New creates a new middleware chain
func New(middlewares ...Middleware) Chain {
	return Chain{middlewares: middlewares}
}

// Then adds more middleware to the chain
func (c Chain) Then(middlewares ...Middleware) Chain {
	return Chain{middlewares: append(c.middlewares, middlewares...)}
}

// Handler applies all middleware in the chain to the given handler
func (c Chain) Handler(handler http.Handler) http.Handler {
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		handler = c.middlewares[i](handler)
	}

	return handler
}

// MiddlewareSet contains all configured middleware for easy composition
type MiddlewareSet struct {
	StatsigBlocker Middleware
	MetricsBlocker Middleware
	Logging        Middleware
	Auth           Middleware
}

// NewMiddlewareSet creates a complete set of middleware with proper dependencies
func NewMiddlewareSet(config *config.Manager, logger *slog.Logger) MiddlewareSet {
	return MiddlewareSet{
		StatsigBlocker: NewStatsigBlockerMiddleware(logger),
		MetricsBlocker: NewMetricsBlockerMiddleware(logger),
		Logging:        NewLoggingMiddleware(logger),
		Auth:           NewAuthMiddleware(config, logger),
	}
}

// DefaultChain returns the standard middleware chain for most endpoints
func (ms MiddlewareSet) DefaultChain() Chain {
	return New(
		ms.StatsigBlocker, // Block telemetry first
		ms.MetricsBlocker, // Block metrics second
		ms.Logging,        // Log requests third
		ms.Auth,           // Authenticate last
	)
}

// HealthChain returns the middleware chain for health endpoints (no auth)
func (ms MiddlewareSet) HealthChain() Chain {
	return New(
		ms.StatsigBlocker, // Block telemetry first
		ms.MetricsBlocker, // Block metrics second
		ms.Logging,        // Log requests third
	)
}

// PublicChain returns the middleware chain for public endpoints (no auth, minimal logging)
func (ms MiddlewareSet) PublicChain() Chain {
	return New(
		ms.StatsigBlocker, // Block telemetry first
		ms.MetricsBlocker, // Block metrics second
	)
}
