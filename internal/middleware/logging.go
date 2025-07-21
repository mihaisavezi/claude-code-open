package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status int
	length int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(data)
	rw.length += n
	return n, err
}

func NewLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			
			wrapped := &responseWriter{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)
			
			logger.Info("HTTP Request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapped.status,
				"duration", duration,
				"length", wrapped.length,
				"remote_addr", r.RemoteAddr,
				"user_agent", r.Header.Get("User-Agent"),
			)
		})
	}
}