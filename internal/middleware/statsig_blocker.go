package middleware

import (
	"log/slog"
	"net/http"
	"strings"
)

type StatsigBlockerMiddleware struct {
	logger *slog.Logger
}

func NewStatsigBlockerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	sbm := &StatsigBlockerMiddleware{
		logger: logger,
	}

	return sbm.middleware
}

func (sbm *StatsigBlockerMiddleware) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if request is targeting statsig.anthropic.com
		host := r.Host
		if host == "" {
			host = r.Header.Get("Host")
		}

		if sbm.isStatsigRequest(host, r.URL.Path) {
			// Return a proper Statsig-like response
			sbm.sendStatsigResponse(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (sbm *StatsigBlockerMiddleware) sendStatsigResponse(w http.ResponseWriter) {
	// Set headers to mimic Statsig's response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Permissions-Policy", "interest-cohort=()")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("X-Response-Time", "0 ms")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Alt-Svc", `h3=":443"; ma=2592000,h3-29=":443"; ma=2592000`)
	w.Header().Set("Via", "1.1 google, 1.1 google")

	// Send 202 Accepted with success response
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"success":true}`))
}

func (sbm *StatsigBlockerMiddleware) isStatsigRequest(host, path string) bool {
	// Block requests to statsig.anthropic.com
	if strings.Contains(host, "statsig.anthropic.com") {
		return true
	}

	// Also block based on common statsig paths in case they use different hosts
	statsigPaths := []string{
		"/v1/initialize",
		"/v1/log_event",
		"/v1/rgstr",
		"/statsig",
		"/telemetry",
		"/analytics",
	}

	for _, statsigPath := range statsigPaths {
		if strings.HasPrefix(path, statsigPath) {
			return true
		}
	}

	return false
}
