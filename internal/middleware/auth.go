package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Davincible/claude-code-open/internal/config"
)

type AuthMiddleware struct {
	config *config.Manager
	logger *slog.Logger
}

func NewAuthMiddleware(config *config.Manager, logger *slog.Logger) func(http.Handler) http.Handler {
	am := &AuthMiddleware{
		config: config,
		logger: logger,
	}

	return am.middleware
}

func (am *AuthMiddleware) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := am.authenticate(r); err != nil {
			am.logger.Error("Authentication failed", "error", err, "remote_addr", r.RemoteAddr)
			http.Error(w, "Proxy API key not authorized", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func (am *AuthMiddleware) authenticate(r *http.Request) error {
	cfg := am.config.Get()

	// Skip auth for health checks or if no API key is configured
	if r.URL.Path == "/health" || cfg.APIKey == "" {
		return nil
	}

	var token string

	// Check Authorization header
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	} else if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		token = apiKey
	}

	if token == "" {
		return errors.New("no authentication token provided")
	}

	if token != cfg.APIKey {
		return errors.New("invalid API key")
	}

	return nil
}
