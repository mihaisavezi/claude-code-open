package handlers

import (
	"log/slog"
	"net/http"
)

type HealthHandler struct {
	logger *slog.Logger
}

func NewHealthHandler(logger *slog.Logger) *HealthHandler {
	return &HealthHandler{
		logger: logger,
	}
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write([]byte("OK")); err != nil {
		h.logger.Error("Failed to write health check response", "error", err)
	}
}
