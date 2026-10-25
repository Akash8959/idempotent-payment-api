package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/idempotent-payment-api/internal/idempotency"
	"github.com/example/idempotent-payment-api/internal/payment"
	"github.com/example/idempotent-payment-api/internal/processor"
)

const (
	HeaderIdempotencyKey    = "Idempotency-Key"
	HeaderIdempotentReplay  = "Idempotent-Replayed"
	HeaderRetryAfter        = "Retry-After"
)

// DBChecker defines readiness check capability.
type DBChecker interface {
	Ping(ctx context.Context) error
}

// PaymentService abstracts payment operations for HTTP handlers.
type PaymentService interface {
	CreatePending(ctx context.Context, input payment.CreatePaymentInput) (*payment.Payment, error)
	UpdatePaymentStatus(ctx context.Context, id string, target payment.Status) (*payment.Payment, error)
	GetPayment(ctx context.Context, id string) (*payment.Payment, error)
}

// IdempotencyCoordinator abstracts idempotency evaluation and storage.
type IdempotencyCoordinator interface {
	Evaluate(ctx context.Context, key, method, path string, rawBody []byte) (*idempotency.ExecutionDecision, error)
	Complete(ctx context.Context, key string, statusCode int, responseBody []byte, paymentID *string) error
}

// Handler aggregates application dependencies to handle HTTP requests.
type Handler struct {
	paymentSvc     PaymentService
	idempotencySvc IdempotencyCoordinator
	processor      processor.Processor
	dbChecker      DBChecker
	logger         *slog.Logger
}

// NewHandler constructs a new Handler.
func NewHandler(
	paymentSvc PaymentService,
	idempotencySvc IdempotencyCoordinator,
	proc processor.Processor,
	dbChecker DBChecker,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		paymentSvc:     paymentSvc,
		idempotencySvc: idempotencySvc,
		processor:      proc,
		dbChecker:      dbChecker,
		logger:         logger,
	}
}

// CreatePayment handles POST /v1/payments with end-to-end idempotency protection.
func (h *Handler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Validate Idempotency-Key header presence
	key := strings.TrimSpace(r.Header.Get(HeaderIdempotencyKey))
	if key == "" {
		WriteError(w, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key header is required")
		return
	}
	if len(key) > 255 {
		WriteError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency key must not exceed 255 characters")
		return
	}

	// 2. Read request body respecting size limits
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if HandleMaxBytesError(err) {
			WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request payload exceeds maximum allowed size")
			return
		}
		WriteError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Failed to read request body")
		return
	}

	// 3. Parse JSON request body
	var req struct {
		Amount      int64            `json:"amount"`
		Currency    payment.Currency `json:"currency"`
		CustomerID  string           `json:"customer_id"`
		Description string           `json:"description,omitempty"`
	}

	if err := json.Unmarshal(rawBody, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON syntax in request body")
		return
	}

	input := payment.CreatePaymentInput{
		IdempotencyKey: key,
		Amount:         req.Amount,
		Currency:       req.Currency,
		CustomerID:     req.CustomerID,
		Description:    req.Description,
	}

	// 4. Validate domain rules before database interaction
	if err := payment.ValidateCreatePayment(input); err != nil {
		WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}

	// 5. Evaluate idempotency state (atomic acquire or replay/conflict/mismatch check)
	decision, err := h.idempotencySvc.Evaluate(ctx, key, r.Method, r.URL.Path, rawBody)
	if err != nil {
		h.logger.ErrorContext(ctx, "idempotency evaluation failed", slog.Any("error", err))
		WriteError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Failed to evaluate idempotency key")
		return
	}

	switch decision.Action {
	case idempotency.ActionReplay:
		// Safe replay: return previously recorded response verbatim with replay header
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set(HeaderIdempotentReplay, "true")
		w.WriteHeader(decision.StoredResponseCode)
		_, _ = w.Write(decision.StoredResponseBody)
		return

	case idempotency.ActionMismatch:
		// Payload mismatch: client reused key with altered request parameters
		WriteError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_PAYLOAD_MISMATCH",
			"Idempotency key was previously used with a different request payload")
		return

	case idempotency.ActionConflict:
		// Concurrency conflict: another request is in flight
		w.Header().Set(HeaderRetryAfter, strconv.Itoa(decision.RetryAfterSeconds))
		WriteError(w, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS",
			"A request with this idempotency key is currently being processed")
		return

	case idempotency.ActionExecute:
		// We acquired or reclaimed the lock: execute payment flow
		h.executePayment(ctx, w, key, input)
		return

	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Unexpected idempotency state")
		return
	}
}

// executePayment handles payment record creation, gateway processing, and state completion.
func (h *Handler) executePayment(ctx context.Context, w http.ResponseWriter, key string, input payment.CreatePaymentInput) {
	// 1. Create payment in 'pending' status
	p, err := h.paymentSvc.CreatePending(ctx, input)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to create pending payment", slog.Any("error", err))
		WriteError(w, http.StatusInternalServerError, "PAYMENT_CREATION_FAILED", "Failed to create payment record")
		return
	}

	// 2. Call external processor
	procReq := processor.PaymentRequest{
		PaymentID:   p.ID,
		CustomerID:  p.CustomerID,
		Amount:      p.Amount,
		Currency:    string(p.Currency),
		Description: p.Description,
	}

	procRes, procErr := h.processor.Process(ctx, procReq)
	if procErr != nil {
		// Transient or timeout error: do not complete idempotency key, leave it to recovery rule
		h.logger.ErrorContext(ctx, "processor error encountered", slog.Any("error", procErr))
		if errors.Is(procErr, processor.ErrProcessorTimeout) || errors.Is(procErr, context.DeadlineExceeded) {
			WriteError(w, http.StatusGatewayTimeout, "PROCESSOR_TIMEOUT", "Payment processor timed out")
			return
		}
		WriteError(w, http.StatusBadGateway, "PROCESSOR_ERROR", "Payment processor communication error")
		return
	}

	// 3. Update payment status according to processor outcome
	targetStatus := payment.StatusSucceeded
	if !procRes.Success {
		targetStatus = payment.StatusFailed
	}

	updatedPayment, err := h.paymentSvc.UpdatePaymentStatus(ctx, p.ID, targetStatus)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to update payment status", slog.Any("error", err))
		WriteError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Failed to finalize payment status")
		return
	}

	// 4. Serialize final HTTP response and store in idempotency key
	statusCode := http.StatusCreated
	respBytes, err := json.Marshal(updatedPayment)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to marshal response", slog.Any("error", err))
		WriteError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Failed to serialize response")
		return
	}

	if err := h.idempotencySvc.Complete(ctx, key, statusCode, respBytes, &updatedPayment.ID); err != nil {
		h.logger.ErrorContext(ctx, "failed to complete idempotency key", slog.Any("error", err))
		WriteError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Failed to complete idempotency record")
		return
	}

	// 5. Send response to client
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write(respBytes)
}

// GetPayment handles GET /v1/payments/{id}.
func (h *Handler) GetPayment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Payment not found")
		return
	}

	p, err := h.paymentSvc.GetPayment(r.Context(), id)
	if err != nil {
		if errors.Is(err, payment.ErrPaymentNotFound) {
			WriteError(w, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Payment not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "failed to get payment", slog.Any("error", err))
		WriteError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Failed to retrieve payment")
		return
	}

	WriteJSON(w, http.StatusOK, p)
}

// Healthz handles GET /healthz (liveness check).
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz handles GET /readyz (readiness check verifying database connectivity).
func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	if h.dbChecker != nil {
		if err := h.dbChecker.Ping(r.Context()); err != nil {
			h.logger.WarnContext(r.Context(), "readiness ping failed", slog.Any("error", err))
			WriteError(w, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", fmt.Sprintf("database ping failed: %v", err))
			return
		}
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
