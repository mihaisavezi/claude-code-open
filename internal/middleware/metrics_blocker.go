package middleware

import (
	"log/slog"
	"net/http"
	"strings"
)

type MetricsBlockerMiddleware struct {
	logger *slog.Logger
}

func NewMetricsBlockerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	mbm := &MetricsBlockerMiddleware{
		logger: logger,
	}
	return mbm.middleware
}

func (mbm *MetricsBlockerMiddleware) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if request is targeting Claude Code metrics endpoints
		host := r.Host
		if host == "" {
			host = r.Header.Get("Host")
		}

		if mbm.isMetricsRequest(host, r.URL.Path) {
			// Return a proper metrics response to prevent logging
			mbm.sendMetricsResponse(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (mbm *MetricsBlockerMiddleware) sendMetricsResponse(w http.ResponseWriter) {
	// Set headers to mimic Anthropic's metrics response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
	w.Header().Set("Via", "1.1 google")
	w.Header().Set("Cf-Cache-Status", "DYNAMIC")
	w.Header().Set("X-Robots-Tag", "none")
	w.Header().Set("Server", "cloudflare")

	// Send 200 OK with metrics-like response to match Claude Code's expected format
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"accepted_count":0,"rejected_count":0}`))
}

func (mbm *MetricsBlockerMiddleware) isMetricsRequest(host, path string) bool {
	// Block requests to api.anthropic.com for Claude Code metrics endpoints
	if strings.Contains(host, "api.anthropic.com") {
		metricsPaths := []string{
			"/api/claude_code/metrics",
			"/claude_code/metrics",
		}
		for _, metricsPath := range metricsPaths {
			if strings.HasPrefix(path, metricsPath) {
				return true
			}
		}
	}

	return false
}
