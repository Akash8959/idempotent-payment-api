// Package integration provides end-to-end integration tests using testcontainers-go
// to spin up a genuine PostgreSQL 16 container, verifying migrations, transactional consistency,
// and database-level concurrency under 50 simultaneous requests.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	appHTTP "github.com/Akash8959/idempotent-payment-api/internal/http"
	"github.com/Akash8959/idempotent-payment-api/internal/idempotency"
	"github.com/Akash8959/idempotent-payment-api/internal/payment"
	"github.com/Akash8959/idempotent-payment-api/internal/processor"
	"github.com/Akash8959/idempotent-payment-api/internal/store"
)

var (
	testPool *pgxpool.Pool
	testDSN  string
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("test_payment_db"),
		postgres.WithUsername("test_user"),
		postgres.WithPassword("test_password"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Printf("skipping integration tests: docker / postgres testcontainer startup failed: %v\n", err)
		return
	}
	defer func() {
		_ = pgContainer.Terminate(context.Background())
	}()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Printf("failed to get connection string: %v\n", err)
		return
	}
	testDSN = connStr

	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		fmt.Printf("failed to initialize test pgx pool: %v\n", err)
		return
	}
	testPool = pool
	defer testPool.Close()

	m.Run()
}

func setupTestApp(t *testing.T, procMode processor.Mode, recoveryTimeout time.Duration) (http.Handler, *processor.SimulatedProcessor) {
	if testPool == nil {
		t.Skip("skipping integration test: postgres container unavailable")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	paymentRepo := store.NewPaymentRepo(testPool)
	idempotencyRepo := store.NewIdempotencyRepo(testPool)

	paymentSvc := payment.NewService(paymentRepo)
	if recoveryTimeout == 0 {
		recoveryTimeout = 30 * time.Second
	}
	idempotencySvc := idempotency.NewService(idempotencyRepo, 24*time.Hour, recoveryTimeout)

	proc := processor.NewSimulatedProcessor(procMode)
	proc.SetLatency(5 * time.Millisecond)

	handler := appHTTP.NewHandler(paymentSvc, idempotencySvc, proc, testPool, logger)
	router := appHTTP.NewRouter(handler, logger)

	return router, proc
}

func resetDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := testPool.Exec(ctx, "TRUNCATE TABLE idempotency_keys, payments CASCADE")
	require.NoError(t, err)
}

func TestIntegration_Migrations(t *testing.T) {
	if testDSN == "" {
		t.Skip("skipping: postgres container unavailable")
	}

	// 1. Up migration
	err := store.MigrateUp(testDSN)
	require.NoError(t, err, "initial migrate up should succeed")

	// 2. Down migration
	err = store.MigrateDown(testDSN)
	require.NoError(t, err, "migrate down should succeed")

	// 3. Re-apply up migration
	err = store.MigrateUp(testDSN)
	require.NoError(t, err, "subsequent migrate up should succeed")
}

func TestIntegration_CreatePaymentEndToEndAndGet(t *testing.T) {
	router, _ := setupTestApp(t, processor.ModeSuccess, 0)
	resetDatabase(t)

	key := "idem_key_e2e_" + uuid.NewString()
	body := `{"amount": 2500, "currency": "USD", "customer_id": "cus_john", "description": "e2e order"}`

	// 1. Create payment
	createReq := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	createReq.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	createRec := httptest.NewRecorder()

	router.ServeHTTP(createRec, createReq)

	require.Equal(t, http.StatusCreated, createRec.Code)
	var created payment.Payment
	err := json.Unmarshal(createRec.Body.Bytes(), &created)
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, payment.StatusSucceeded, created.Status)
	assert.Equal(t, int64(2500), created.Amount)
	assert.Equal(t, payment.CurrencyUSD, created.Currency)

	// 2. GET payment by ID
	getReq := httptest.NewRequest(http.MethodGet, "/v1/payments/"+created.ID, nil)
	getRec := httptest.NewRecorder()

	router.ServeHTTP(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code)
	var fetched payment.Payment
	err = json.Unmarshal(getRec.Body.Bytes(), &fetched)
	require.NoError(t, err)
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, created.Status, fetched.Status)
}

func TestIntegration_RetrySameRequest5Times(t *testing.T) {
	router, _ := setupTestApp(t, processor.ModeSuccess, 0)
	resetDatabase(t)

	key := "idem_key_retry5_" + uuid.NewString()
	body := `{"amount": 1200, "currency": "INR", "customer_id": "cus_alice"}`

	var firstResponse string
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
		req.Header.Set(appHTTP.HeaderIdempotencyKey, key)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusCreated, rec.Code)
		if i == 0 {
			firstResponse = rec.Body.String()
			assert.Empty(t, rec.Header().Get(appHTTP.HeaderIdempotentReplay), "first call should not be marked replayed")
		} else {
			assert.Equal(t, firstResponse, rec.Body.String(), "retry responses must be identical")
			assert.Equal(t, "true", rec.Header().Get(appHTTP.HeaderIdempotentReplay), "retries must contain replay header")
		}
	}

	// Verify exactly one row in payments table
	var count int
	err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM payments").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "payments table must have exactly 1 record after 5 retries")
}

func TestIntegration_Concurrency50Goroutines(t *testing.T) {
	// Rule 5: Concurrency safety must come from the database, not from in-memory locks:
	// use a unique constraint on the key plus INSERT ... ON CONFLICT so that 50 simultaneous
	// requests with the same key produce exactly one payment.
	router, _ := setupTestApp(t, processor.ModeSuccess, 0)
	resetDatabase(t)

	key := "idem_key_concurrent_50_" + uuid.NewString()
	body := `{"amount": 5000, "currency": "EUR", "customer_id": "cus_concurrency"}`

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	type respRecord struct {
		code int
		body string
	}
	responses := make([]respRecord, goroutines)

	startBarrier := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			<-startBarrier // Release all 50 goroutines at the exact same moment

			req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
			req.Header.Set(appHTTP.HeaderIdempotencyKey, key)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)
			responses[idx] = respRecord{
				code: rec.Code,
				body: rec.Body.String(),
			}
		}()
	}

	close(startBarrier)
	wg.Wait()

	// Verify all responses: must be either 201 Created or 409 Conflict
	var createdCount, conflictCount int
	var successBody string

	for _, r := range responses {
		switch r.code {
		case http.StatusCreated:
			createdCount++
			if successBody == "" {
				successBody = r.body
			} else {
				assert.Equal(t, successBody, r.body, "all 201 responses must have the exact same body")
			}
		case http.StatusConflict:
			conflictCount++
			assert.Contains(t, r.body, "IDEMPOTENCY_IN_PROGRESS")
		default:
			t.Fatalf("unexpected HTTP status code during concurrency test: %d, body: %s", r.code, r.body)
		}
	}

	// At least 1 must have succeeded, remaining were 409 (or replay if completed quickly)
	assert.GreaterOrEqual(t, createdCount, 1)

	// In the database: EXACTLY ONE row in payments table!
	var paymentRowCount int
	err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM payments").Scan(&paymentRowCount)
	require.NoError(t, err)
	assert.Equal(t, 1, paymentRowCount, "CRITICAL: payments table must contain exactly ONE payment row")

	// Exactly ONE row in idempotency_keys table
	var keyRowCount int
	err = testPool.QueryRow(context.Background(), "SELECT count(*) FROM idempotency_keys WHERE key = $1", key).Scan(&keyRowCount)
	require.NoError(t, err)
	assert.Equal(t, 1, keyRowCount)
}

func TestIntegration_PayloadMismatchReturns422(t *testing.T) {
	router, _ := setupTestApp(t, processor.ModeSuccess, 0)
	resetDatabase(t)

	key := "idem_key_mismatch_" + uuid.NewString()
	bodyA := `{"amount": 1000, "currency": "INR", "customer_id": "cus_1"}`
	bodyB := `{"amount": 9999, "currency": "INR", "customer_id": "cus_1"}` // Altered amount

	// First request succeeds
	reqA := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(bodyA))
	reqA.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	recA := httptest.NewRecorder()
	router.ServeHTTP(recA, reqA)
	require.Equal(t, http.StatusCreated, recA.Code)

	// Second request with altered payload must return 422
	reqB := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(bodyB))
	reqB.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	recB := httptest.NewRecorder()
	router.ServeHTTP(recB, reqB)

	assert.Equal(t, http.StatusUnprocessableEntity, recB.Code)
	assert.Contains(t, recB.Body.String(), "IDEMPOTENCY_PAYLOAD_MISMATCH")

	// Verify no second payment created
	var count int
	err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM payments").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestIntegration_ProcessorFailure(t *testing.T) {
	router, _ := setupTestApp(t, processor.ModeFailure, 0)
	resetDatabase(t)

	key := "idem_key_fail_" + uuid.NewString()
	body := `{"amount": 1500, "currency": "USD", "customer_id": "cus_fail"}`

	// First execution: processor fails
	req1 := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	req1.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	require.Equal(t, http.StatusCreated, rec1.Code)
	var p1 payment.Payment
	err := json.Unmarshal(rec1.Body.Bytes(), &p1)
	require.NoError(t, err)
	assert.Equal(t, payment.StatusFailed, p1.Status)

	// Retry with same key: returns stored failed result
	req2 := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	req2.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	assert.Equal(t, http.StatusCreated, rec2.Code)
	assert.Equal(t, "true", rec2.Header().Get(appHTTP.HeaderIdempotentReplay))
	var p2 payment.Payment
	err = json.Unmarshal(rec2.Body.Bytes(), &p2)
	require.NoError(t, err)
	assert.Equal(t, payment.StatusFailed, p2.Status)
	assert.Equal(t, rec1.Body.String(), rec2.Body.String())
}

func TestIntegration_ProcessorTimeoutAndStuckRecovery(t *testing.T) {
	// Setup app with 1 second recovery timeout for test speed
	recoveryTimeout := 1 * time.Second
	router, proc := setupTestApp(t, processor.ModeTimeout, recoveryTimeout)
	resetDatabase(t)

	key := "idem_key_timeout_" + uuid.NewString()
	body := `{"amount": 4000, "currency": "EUR", "customer_id": "cus_timeout"}`

	// First request times out
	ctxFast, cancelFast := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelFast()

	req1 := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body)).WithContext(ctxFast)
	req1.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	assert.Equal(t, http.StatusGatewayTimeout, rec1.Code)

	// An immediate retry before recovery timeout returns 409 Conflict
	reqImmediate := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	reqImmediate.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	recImmediate := httptest.NewRecorder()
	router.ServeHTTP(recImmediate, reqImmediate)
	assert.Equal(t, http.StatusConflict, recImmediate.Code)

	// Wait past recovery timeout (> 1s)
	time.Sleep(1100 * time.Millisecond)

	// Switch processor back to success
	proc.SetMode(processor.ModeSuccess)

	// Retry should reclaim stuck in_progress lock and proceed to success!
	reqRecover := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	reqRecover.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	recRecover := httptest.NewRecorder()
	router.ServeHTTP(recRecover, reqRecover)

	assert.Equal(t, http.StatusCreated, recRecover.Code)
	var pRecover payment.Payment
	err := json.Unmarshal(recRecover.Body.Bytes(), &pRecover)
	require.NoError(t, err)
	assert.Equal(t, payment.StatusSucceeded, pRecover.Status)
}

func TestIntegration_ExpiredKeyReuseAndCleanup(t *testing.T) {
	if testPool == nil {
		t.Skip("skipping: postgres container unavailable")
	}
	resetDatabase(t)

	key := "idem_key_expired_test"
	ctx := context.Background()

	// Insert an expired key directly into DB (expires_at in the past)
	past := time.Now().UTC().Add(-2 * time.Hour)
	_, err := testPool.Exec(ctx, `
		INSERT INTO idempotency_keys (key, request_hash, status, response_code, response_body, expires_at, locked_at, created_at)
		VALUES ($1, 'dummyhash', 'completed', 201, '{"status":"old"}', $2, $2, $2)
	`, key, past)
	require.NoError(t, err)

	// Run cleanup
	idemRepo := store.NewIdempotencyRepo(testPool)
	deleted, err := idemRepo.DeleteExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// Now a new payment request with this previously expired key can proceed cleanly!
	router, _ := setupTestApp(t, processor.ModeSuccess, 0)
	body := `{"amount": 1000, "currency": "INR", "customer_id": "cus_expired_reused"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	req.Header.Set(appHTTP.HeaderIdempotencyKey, key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	var pExpired payment.Payment
	err = json.Unmarshal(rec.Body.Bytes(), &pExpired)
	require.NoError(t, err)
	assert.Equal(t, payment.StatusSucceeded, pExpired.Status)
}

func TestIntegration_TransactionRollbackNoOrphanPayment(t *testing.T) {
	if testPool == nil {
		t.Skip("skipping: postgres container unavailable")
	}
	resetDatabase(t)

	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	require.NoError(t, err)

	// Insert payment within transaction
	paymentRepo := store.NewPaymentRepo(testPool)
	p := &payment.Payment{
		ID:         uuid.NewString(),
		CustomerID: "cus_tx_test",
		Amount:     9999,
		Currency:   payment.CurrencyUSD,
		Status:     payment.StatusPending,
	}

	err = paymentRepo.CreateTx(ctx, tx, p)
	require.NoError(t, err)

	// Simulate error when updating idempotency key -> Rollback transaction
	err = tx.Rollback(ctx)
	require.NoError(t, err)

	// Verify no orphan payment remains in the database
	var count int
	err = testPool.QueryRow(ctx, "SELECT count(*) FROM payments WHERE id = $1", p.ID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "payment must not exist after transaction rollback")
}
