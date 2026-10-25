package http_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appHTTP "github.com/example/idempotent-payment-api/internal/http"
)

func TestRequestIDMiddleware(t *testing.T) {
	t.Run("generates new request ID if header is absent", func(t *testing.T) {
		handler := appHTTP.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := appHTTP.GetRequestID(r.Context())
			assert.NotEmpty(t, reqID)
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		respHeader := rec.Header().Get(appHTTP.HeaderRequestID)
		assert.NotEmpty(t, respHeader)
	})

	t.Run("preserves existing incoming request ID", func(t *testing.T) {
		customID := "client-trace-12345"
		handler := appHTTP.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := appHTTP.GetRequestID(r.Context())
			assert.Equal(t, customID, reqID)
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set(appHTTP.HeaderRequestID, customID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		assert.Equal(t, customID, rec.Header().Get(appHTTP.HeaderRequestID))
	})
}

func TestRecovererMiddleware(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	panicHandler := appHTTP.RecovererMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated fatal crash")
	}))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		panicHandler.ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "INTERNAL_SERVER_ERROR")
}

func TestBodySizeLimitMiddleware(t *testing.T) {
	maxBytes := int64(100) // Small limit for testing
	limitedHandler := appHTTP.BodySizeLimitMiddleware(maxBytes)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil && appHTTP.HandleMaxBytesError(err) {
			appHTTP.WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body too large")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("payload within limit succeeds", func(t *testing.T) {
		body := strings.Repeat("a", 50)
		req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
		rec := httptest.NewRecorder()

		limitedHandler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("payload exceeding limit returns 413", func(t *testing.T) {
		body := strings.Repeat("a", 200)
		req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
		rec := httptest.NewRecorder()

		limitedHandler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), "PAYLOAD_TOO_LARGE")
	})
}

func TestStructuredLoggerMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	loggedHandler := appHTTP.StructuredLoggerMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/test-log", nil)
	rec := httptest.NewRecorder()

	loggedHandler.ServeHTTP(rec, req)

	logOutput := buf.String()
	require.NotEmpty(t, logOutput)
	assert.Contains(t, logOutput, "POST")
	assert.Contains(t, logOutput, "/test-log")
	assert.Contains(t, logOutput, "status=201")
}
