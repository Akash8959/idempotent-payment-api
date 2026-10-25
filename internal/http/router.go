package http

import (
	"log/slog"
	"net/http"
)

const maxRequestBodyBytes = 1024 * 1024 // 1 MB limit

// NewRouter constructs the standard Go 1.22 pattern-matching HTTP router
// decorated with production-grade cross-cutting middleware.
func NewRouter(handler *Handler, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Register Go 1.22 pattern routes
	mux.HandleFunc("POST /v1/payments", handler.CreatePayment)
	mux.HandleFunc("GET /v1/payments/{id}", handler.GetPayment)
	mux.HandleFunc("GET /healthz", handler.Healthz)
	mux.HandleFunc("GET /readyz", handler.Readyz)

	// Wrap middleware chain in standard reverse-onion order
	// 1. Request ID (sets X-Request-ID first so logger & recoverer see it)
	// 2. Panic Recoverer (catches panics from inner handlers)
	// 3. Structured Logger (logs request with status and timing)
	// 4. Request Body Size Limiter (enforces 1MB max payload)
	var h http.Handler = mux
	h = BodySizeLimitMiddleware(maxRequestBodyBytes)(h)
	h = StructuredLoggerMiddleware(logger)(h)
	h = RecovererMiddleware(logger)(h)
	h = RequestIDMiddleware(h)

	return h
}
