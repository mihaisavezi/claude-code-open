package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Davincible/claude-code-router-go/internal/config"
	"github.com/Davincible/claude-code-router-go/internal/providers"
	"github.com/Davincible/claude-code-router-go/internal/middleware"
	"github.com/Davincible/claude-code-router-go/internal/handlers"
)

type Server struct {
	config   *config.Manager
	registry *providers.Registry
	logger   *slog.Logger
	server   *http.Server
}

func New(configManager *config.Manager, logger *slog.Logger) *Server {
	registry := providers.NewRegistry()
	registry.Initialize()

	return &Server{
		config:   configManager,
		registry: registry,
		logger:   logger,
	}
}

func (s *Server) Start() error {
	cfg := s.config.Get()
	if cfg == nil {
		return fmt.Errorf("configuration not loaded")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	
	// Setup routes
	mux := s.setupRoutes()
	
	s.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	s.logger.Info("Starting server", "address", addr)

	// Start server in goroutine
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("Server error", "error", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	s.logger.Info("Server is shutting down...")

	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("server forced to shutdown: %w", err)
	}

	s.logger.Info("Server exited")
	return nil
}

func (s *Server) Stop() error {
	if s.server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

func (s *Server) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Create handlers
	proxyHandler := handlers.NewProxyHandler(s.config, s.registry, s.logger)
	healthHandler := handlers.NewHealthHandler(s.logger)

	// Setup middleware chains
	middlewareSet := middleware.NewMiddlewareSet(s.config, s.logger)

	// Apply middleware chains to routes
	mux.Handle("/health", middlewareSet.HealthChain().Handler(healthHandler))
	mux.Handle("/", middlewareSet.DefaultChain().Handler(proxyHandler))

	return mux
}