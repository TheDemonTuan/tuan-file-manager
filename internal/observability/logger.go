package observability

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const (
	RequestIDKey contextKey = "request_id"
	IdentityKey  contextKey = "identity"
)

func InitLogger(env string) *slog.Logger {
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	if env == "development" {
		opts.Level = slog.LevelDebug
	}

	handler = slog.NewJSONHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	bytesRead  int64
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesRead += int64(n)
	return n, err
}

func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("CF-Ray")
		if reqID == "" {
			reqID = r.Header.Get("X-Request-ID")
		}
		if reqID == "" {
			reqID = uuid.New().String()
		}

		ctx := context.WithValue(r.Context(), RequestIDKey, reqID)
		w.Header().Set("X-Request-ID", reqID)

		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r.WithContext(ctx))

		duration := time.Since(start)

		// Sanitize path if it looks like a share token URL to avoid logging token plaintext
		path := r.URL.Path
		if strings.HasPrefix(path, "/s/") {
			parts := strings.Split(path, "/")
			if len(parts) >= 3 && parts[2] != "" {
				parts[2] = "[REDACTED_TOKEN]"
				path = strings.Join(parts, "/")
			}
		}

		slog.Info("http_request",
			"request_id", reqID,
			"method", r.Method,
			"path", path,
			"status", wrapped.statusCode,
			"duration_ms", duration.Milliseconds(),
			"remote_ip", sanitizeIP(r.RemoteAddr),
			"user_agent", r.UserAgent(),
		)
	})
}

func sanitizeIP(addr string) string {
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
