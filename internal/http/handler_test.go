package http_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	appHTTP "github.com/example/idempotent-payment-api/internal/http"
	"github.com/example/idempotent-payment-api/internal/idempotency"
	"github.com/example/idempotent-payment-api/internal/payment"
	"github.com/example/idempotent-payment-api/internal/processor"
)

// fakePaymentService implements appHTTP.PaymentService in-memory.
type fakePaymentService struct {
	payments  map[string]*payment.Payment
	createErr error
	getErr    error
}

func newFakePaymentService() *fakePaymentService {
	return &fakePaymentService{payments: make(map[string]*payment.Payment)}
}

func (f *fakePaymentService) CreatePending(ctx context.Context, input payment.CreatePaymentInput) (*payment.Payment, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	p := &payment.Payment{
		ID:          "pay_" + input.CustomerID,
		CustomerID:  input.CustomerID,
		Amount:      input.Amount,
		Currency:    input.Currency,
		Description: input.Description,
		Status:      payment.StatusPending,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	f.payments[p.ID] = p
	return p, nil
}

func (f *fakePaymentService) UpdatePaymentStatus(ctx context.Context, id string, target payment.Status) (*payment.Payment, error) {
	p, ok := f.payments[id]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	p.Status = target
	p.UpdatedAt = time.Now().UTC()
	return p, nil
}

func (f *fakePaymentService) GetPayment(ctx context.Context, id string) (*payment.Payment, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	p, ok := f.payments[id]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	return p, nil
}

// fakeIdempotencyCoordinator implements appHTTP.IdempotencyCoordinator in-memory.
type fakeIdempotencyCoordinator struct {
	decision *idempotency.ExecutionDecision
	evalErr  error
	complete map[string][]byte
}

func newFakeIdempotencyCoordinator() *fakeIdempotencyCoordinator {
	return &fakeIdempotencyCoordinator{
		decision: &idempotency.ExecutionDecision{Action: idempotency.ActionExecute},
		complete: make(map[string][]byte),
	}
}

func (f *fakeIdempotencyCoordinator) Evaluate(ctx context.Context, key, method, path string, rawBody []byte) (*idempotency.ExecutionDecision, error) {
	if f.evalErr != nil {
		return nil, f.evalErr
	}
	return f.decision, nil
}

func (f *fakeIdempotencyCoordinator) Complete(ctx context.Context, key string, statusCode int, responseBody []byte, paymentID *string) error {
	f.complete[key] = responseBody
	return nil
}

// fakeDBChecker simulates DB ping.
type fakeDBChecker struct {
	pingErr error
}

func (f *fakeDBChecker) Ping(ctx context.Context) error {
	return f.pingErr
}

func setupTestRouter(
	paySvc appHTTP.PaymentService,
	idemCoord *fakeIdempotencyCoordinator,
	proc processor.Processor,
	dbChecker *fakeDBChecker,
) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := appHTTP.NewHandler(paySvc, idemCoord, proc, dbChecker, logger)
	return appHTTP.NewRouter(handler, logger)
}

func TestHandler_CreatePayment(t *testing.T) {
	validBody := `{"amount": 1500, "currency": "INR", "customer_id": "cus_123", "description": "Test payment"}`

	t.Run("201 on success", func(t *testing.T) {
		paySvc := newFakePaymentService()
		idemCoord := newFakeIdempotencyCoordinator()
		proc := processor.NewSimulatedProcessor(processor.ModeSuccess)
		proc.SetLatency(1 * time.Millisecond)
		router := setupTestRouter(paySvc, idemCoord, proc, &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-success")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Contains(t, rec.Body.String(), `"status":"succeeded"`)
		assert.NotEmpty(t, rec.Header().Get(appHTTP.HeaderRequestID), "request ID header must be present")
	})

	t.Run("400 on missing key", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody))
		// No Idempotency-Key header
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "MISSING_IDEMPOTENCY_KEY")
	})

	t.Run("400 on bad input (amount <= 0)", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		badBody := `{"amount": 0, "currency": "INR", "customer_id": "cus_123"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(badBody))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-bad-amt")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "VALIDATION_ERROR")
	})

	t.Run("400 on bad currency", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		badBody := `{"amount": 100, "currency": "GBP", "customer_id": "cus_123"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(badBody))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-bad-cur")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "VALIDATION_ERROR")
	})

	t.Run("400 on invalid JSON syntax", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(`{bad json`))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-bad-json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "INVALID_JSON")
	})

	t.Run("422 on payload mismatch", func(t *testing.T) {
		idemCoord := newFakeIdempotencyCoordinator()
		idemCoord.decision = &idempotency.ExecutionDecision{Action: idempotency.ActionMismatch}

		router := setupTestRouter(newFakePaymentService(), idemCoord, processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-mismatch")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "IDEMPOTENCY_PAYLOAD_MISMATCH")
	})

	t.Run("409 on in-progress conflict with Retry-After header", func(t *testing.T) {
		idemCoord := newFakeIdempotencyCoordinator()
		idemCoord.decision = &idempotency.ExecutionDecision{
			Action:            idempotency.ActionConflict,
			RetryAfterSeconds: 5,
		}

		router := setupTestRouter(newFakePaymentService(), idemCoord, processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-conflict")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "5", rec.Header().Get(appHTTP.HeaderRetryAfter))
		assert.Contains(t, rec.Body.String(), "IDEMPOTENCY_IN_PROGRESS")
	})

	t.Run("replay header present and identical cached response returned", func(t *testing.T) {
		cachedResponse := []byte(`{"id":"pay_cached","status":"succeeded","amount":1500}`)
		idemCoord := newFakeIdempotencyCoordinator()
		idemCoord.decision = &idempotency.ExecutionDecision{
			Action:             idempotency.ActionReplay,
			StoredResponseCode: http.StatusCreated,
			StoredResponseBody: cachedResponse,
		}

		router := setupTestRouter(newFakePaymentService(), idemCoord, processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-replayed")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Equal(t, "true", rec.Header().Get(appHTTP.HeaderIdempotentReplay))
		assert.Equal(t, string(cachedResponse), rec.Body.String())
	})

	t.Run("processor failure transitions payment to failed", func(t *testing.T) {
		paySvc := newFakePaymentService()
		proc := processor.NewSimulatedProcessor(processor.ModeFailure)
		proc.SetLatency(1 * time.Millisecond)

		router := setupTestRouter(paySvc, newFakeIdempotencyCoordinator(), proc, &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-proc-fail")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Contains(t, rec.Body.String(), `"status":"failed"`)
	})

	t.Run("processor timeout returns 504", func(t *testing.T) {
		paySvc := newFakePaymentService()
		proc := processor.NewSimulatedProcessor(processor.ModeTimeout)

		router := setupTestRouter(paySvc, newFakeIdempotencyCoordinator(), proc, &fakeDBChecker{})

		// Pass context with fast timeout
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody)).WithContext(ctx)
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-proc-timeout")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.Contains(t, rec.Body.String(), "PROCESSOR_TIMEOUT")
	})

	t.Run("oversize request body returns 413", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		oversize := strings.Repeat("x", 2*1024*1024) // 2 MB > 1 MB limit
		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(oversize))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, "key-large")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), "PAYLOAD_TOO_LARGE")
	})
}

func TestHandler_GetPayment(t *testing.T) {
	t.Run("200 on existing payment", func(t *testing.T) {
		paySvc := newFakePaymentService()
		paySvc.payments["pay_123"] = &payment.Payment{
			ID:         "pay_123",
			CustomerID: "cus_1",
			Amount:     500,
			Currency:   payment.CurrencyUSD,
			Status:     payment.StatusSucceeded,
		}

		router := setupTestRouter(paySvc, newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodGet, "/v1/payments/pay_123", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"id":"pay_123"`)
		assert.Contains(t, rec.Body.String(), `"status":"succeeded"`)
	})

	t.Run("404 on unknown payment", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodGet, "/v1/payments/unknown_id", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "PAYMENT_NOT_FOUND")
	})
}

func TestHandler_HealthzAndReadyz(t *testing.T) {
	t.Run("healthz returns 200 ok", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"status":"ok"`)
	})

	t.Run("readyz returns 200 when db is healthy", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{pingErr: nil})

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"status":"ready"`)
	})

	t.Run("readyz returns 503 when db ping fails", func(t *testing.T) {
		router := setupTestRouter(newFakePaymentService(), newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{pingErr: errors.New("connection refused")})

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "DATABASE_UNAVAILABLE")
	})
}

func TestHandler_PanicRecovery(t *testing.T) {
	paySvc := newFakePaymentService()
	paySvc.getErr = nil

	// Custom handler that deliberately panics during execution
	panicPaySvc := &panicPaymentService{}
	router := setupTestRouter(panicPaySvc, newFakeIdempotencyCoordinator(), processor.NewSimulatedProcessor(processor.ModeSuccess), &fakeDBChecker{})

	req := httptest.NewRequest(http.MethodGet, "/v1/payments/trigger_panic", nil)
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		router.ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "INTERNAL_SERVER_ERROR")
}

type panicPaymentService struct {
	fakePaymentService
}

func (p *panicPaymentService) GetPayment(ctx context.Context, id string) (*payment.Payment, error) {
	panic("unexpected fatal runtime error in business logic")
}
