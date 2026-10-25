package idempotency_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Akash8959/idempotent-payment-api/internal/idempotency"
)

// fakeStore simulates the database for idempotency unit tests.
type fakeStore struct {
	mu          sync.Mutex
	records     map[string]*idempotency.Record
	acquireErr  error
	reclaimErr  error
	completeErr error
	deleteErr   error
	getErr      error
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[string]*idempotency.Record)}
}

func (f *fakeStore) Acquire(ctx context.Context, key, hash string, ttl time.Duration) (*idempotency.AcquireResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.acquireErr != nil {
		return nil, f.acquireErr
	}

	rec, exists := f.records[key]
	if !exists {
		now := time.Now().UTC()
		newRec := &idempotency.Record{
			Key:         key,
			RequestHash: hash,
			Status:      idempotency.StatusInProgress,
			CreatedAt:   now,
			ExpiresAt:   now.Add(ttl),
			LockedAt:    now,
		}
		f.records[key] = newRec
		return &idempotency.AcquireResult{
			Acquired: true,
			Record:   newRec,
		}, nil
	}

	return &idempotency.AcquireResult{
		Acquired: false,
		Record:   rec,
	}, nil
}

// ReclaimStuckLock simulates atomic UPDATE ... WHERE status='in_progress' AND locked_at < NOW() - 30s
func (f *fakeStore) ReclaimStuckLock(ctx context.Context, key, hash string, recoveryTimeout time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.reclaimErr != nil {
		return false, f.reclaimErr
	}

	rec, exists := f.records[key]
	if !exists {
		return false, nil
	}

	now := time.Now().UTC()
	if rec.Status == idempotency.StatusInProgress && now.Sub(rec.LockedAt) >= recoveryTimeout {
		// Atomic row update: only one caller can win this transition
		rec.LockedAt = now
		rec.RequestHash = hash
		return true, nil
	}

	return false, nil
}

func (f *fakeStore) Complete(ctx context.Context, key string, statusCode int, responseBody []byte, paymentID *string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.completeErr != nil {
		return nil, f.completeErr
	}

	rec, exists := f.records[key]
	if !exists {
		return nil, idempotency.ErrKeyNotFound
	}

	rec.Status = idempotency.StatusCompleted
	rec.ResponseCode = &statusCode
	rec.ResponseBody = responseBody
	rec.PaymentID = paymentID
	return responseBody, nil
}

func (f *fakeStore) Get(ctx context.Context, key string) (*idempotency.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.getErr != nil {
		return nil, f.getErr
	}

	rec, exists := f.records[key]
	if !exists {
		return nil, idempotency.ErrKeyNotFound
	}
	return rec, nil
}

func (f *fakeStore) DeleteExpired(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.deleteErr != nil {
		return 0, f.deleteErr
	}

	var deleted int64
	now := time.Now().UTC()
	for k, rec := range f.records {
		if rec.IsExpired(now) {
			delete(f.records, k)
			deleted++
		}
	}
	return deleted, nil
}

func TestIdempotencyService_FirstRequest(t *testing.T) {
	store := newFakeStore()
	svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)

	decision, err := svc.Evaluate(context.Background(), "key-1", "POST", "/v1/payments", []byte(`{"amount": 1000}`))
	require.NoError(t, err)
	assert.Equal(t, idempotency.ActionExecute, decision.Action)
}

func TestIdempotencyService_ReplayCompletedRequest(t *testing.T) {
	store := newFakeStore()
	svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
	ctx := context.Background()

	key := "key-replay"
	body := []byte(`{"amount": 1000, "currency": "USD"}`)

	// First execution
	dec1, err := svc.Evaluate(ctx, key, "POST", "/v1/payments", body)
	require.NoError(t, err)
	require.Equal(t, idempotency.ActionExecute, dec1.Action)

	// Complete the key with a 201 response
	paymentID := "pay_999"
	respBody := []byte(`{"id": "pay_999", "status": "succeeded"}`)
	_, err = svc.Complete(ctx, key, 201, respBody, &paymentID)
	require.NoError(t, err)

	// Second execution with identical payload: must replay cached response
	dec2, err := svc.Evaluate(ctx, key, "POST", "/v1/payments", body)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ActionReplay, dec2.Action)
	assert.Equal(t, 201, dec2.StoredResponseCode)
	assert.Equal(t, respBody, dec2.StoredResponseBody)
	assert.Equal(t, &paymentID, dec2.PaymentID)
}

func TestIdempotencyService_PayloadMismatchReturns422Action(t *testing.T) {
	store := newFakeStore()
	svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
	ctx := context.Background()

	key := "key-mismatch"
	body1 := []byte(`{"amount": 1000, "currency": "USD"}`)
	body2 := []byte(`{"amount": 5000, "currency": "USD"}`) // Modified payload

	// First execution & completion
	_, err := svc.Evaluate(ctx, key, "POST", "/v1/payments", body1)
	require.NoError(t, err)
	_, err = svc.Complete(ctx, key, 201, []byte(`{}`), nil)
	require.NoError(t, err)

	// Second execution with modified body: must result in ActionMismatch (422)
	decMismatch, err := svc.Evaluate(ctx, key, "POST", "/v1/payments", body2)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ActionMismatch, decMismatch.Action)
}

func TestIdempotencyService_InProgressConflictReturns409Action(t *testing.T) {
	store := newFakeStore()
	svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
	ctx := context.Background()

	key := "key-conflict"
	body := []byte(`{"amount": 1000}`)

	// First execution claims the key (in_progress)
	dec1, err := svc.Evaluate(ctx, key, "POST", "/v1/payments", body)
	require.NoError(t, err)
	require.Equal(t, idempotency.ActionExecute, dec1.Action)

	// Second execution arrives while first is still in progress: must return ActionConflict (409)
	dec2, err := svc.Evaluate(ctx, key, "POST", "/v1/payments", body)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ActionConflict, dec2.Action)
	assert.Equal(t, 5, dec2.RetryAfterSeconds)
}

func TestIdempotencyService_StuckLockReclaimRule(t *testing.T) {
	store := newFakeStore()
	svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
	ctx := context.Background()

	key := "key-stuck"
	body := []byte(`{"amount": 2000}`)

	// Setup: insert key that is in_progress but locked 35 seconds ago (stuck)
	reqHash, err := idempotency.ComputeRequestHash("POST", "/v1/payments", body)
	require.NoError(t, err)

	now := time.Now().UTC()
	store.records[key] = &idempotency.Record{
		Key:         key,
		RequestHash: reqHash,
		Status:      idempotency.StatusInProgress,
		CreatedAt:   now.Add(-40 * time.Second),
		ExpiresAt:   now.Add(24 * time.Hour),
		LockedAt:    now.Add(-35 * time.Second), // Stuck: > 30s
	}

	// Rule 3: Stuck lock reclaim must trigger atomic UPDATE and allow execution
	dec, err := svc.Evaluate(ctx, key, "POST", "/v1/payments", body)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ActionExecute, dec.Action, "stuck key should be reclaimed and executed")
}

func TestIdempotencyService_TwoRequestsRacingToReclaimStuckLock(t *testing.T) {
	// Rule 3 explicit verification:
	// "the stuck-lock reclaim must be one atomic UPDATE ... WHERE status='in_progress' AND locked_at < now() - interval '30 seconds',
	// and check rows affected so two requests cannot both reclaim the key. Add a test for exactly that."
	store := newFakeStore()
	svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
	ctx := context.Background()

	key := "key-race-reclaim"
	body := []byte(`{"amount": 3000}`)
	reqHash, err := idempotency.ComputeRequestHash("POST", "/v1/payments", body)
	require.NoError(t, err)

	now := time.Now().UTC()
	store.records[key] = &idempotency.Record{
		Key:         key,
		RequestHash: reqHash,
		Status:      idempotency.StatusInProgress,
		CreatedAt:   now.Add(-50 * time.Second),
		ExpiresAt:   now.Add(24 * time.Hour),
		LockedAt:    now.Add(-45 * time.Second), // Stuck
	}

	var wg sync.WaitGroup
	results := make([]idempotency.Action, 2)
	wg.Add(2)

	// Launch two concurrent requests attempting to evaluate and reclaim the same stuck key
	for i := 0; i < 2; i++ {
		idx := i
		go func() {
			defer wg.Done()
			dec, evalErr := svc.Evaluate(ctx, key, "POST", "/v1/payments", body)
			if evalErr == nil && dec != nil {
				results[idx] = dec.Action
			}
		}()
	}
	wg.Wait()

	// Exactly one must win ActionExecute (rows affected == 1), the other must receive ActionConflict (rows affected == 0)
	var executeCount, conflictCount int
	for _, action := range results {
		if action == idempotency.ActionExecute {
			executeCount++
		} else if action == idempotency.ActionConflict {
			conflictCount++
		}
	}

	assert.Equal(t, 1, executeCount, "exactly one concurrent request must win the stuck lock reclaim")
	assert.Equal(t, 1, conflictCount, "the losing concurrent request must receive a 409 conflict")
}

func TestIdempotencyService_ErrorCases(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid json in request body returns error", func(t *testing.T) {
		store := newFakeStore()
		svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
		_, err := svc.Evaluate(ctx, "k1", "POST", "/v1/payments", []byte(`{invalid`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to compute request hash")
	})

	t.Run("acquire error propagates", func(t *testing.T) {
		store := newFakeStore()
		store.acquireErr = errors.New("db disconnect")
		svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
		_, err := svc.Evaluate(ctx, "k1", "POST", "/v1/payments", []byte(`{"a": 1}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to acquire idempotency key")
	})

	t.Run("reclaim error propagates", func(t *testing.T) {
		store := newFakeStore()
		now := time.Now().UTC()
		body := []byte(`{"amount": 100}`)
		h, _ := idempotency.ComputeRequestHash("POST", "/v1/payments", body)
		store.records["stuck-err"] = &idempotency.Record{
			Key:         "stuck-err",
			RequestHash: h,
			Status:      idempotency.StatusInProgress,
			LockedAt:    now.Add(-60 * time.Second),
		}
		store.reclaimErr = errors.New("reclaim db failure")
		svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)

		_, err := svc.Evaluate(ctx, "stuck-err", "POST", "/v1/payments", body)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to reclaim stuck lock")
	})

	t.Run("unknown status returns error", func(t *testing.T) {
		store := newFakeStore()
		body := []byte(`{"amount": 100}`)
		h, _ := idempotency.ComputeRequestHash("POST", "/v1/payments", body)
		store.records["unknown-status"] = &idempotency.Record{
			Key:         "unknown-status",
			RequestHash: h,
			Status:      "unknown_val",
		}
		svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
		_, err := svc.Evaluate(ctx, "unknown-status", "POST", "/v1/payments", body)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown idempotency status")
	})
}

func TestIdempotencyService_CleanupExpired(t *testing.T) {
	store := newFakeStore()
	svc := idempotency.NewService(store, 24*time.Hour, 30*time.Second)
	ctx := context.Background()

	now := time.Now().UTC()
	store.records["expired-key"] = &idempotency.Record{
		Key:       "expired-key",
		ExpiresAt: now.Add(-1 * time.Hour),
		Status:    idempotency.StatusCompleted,
	}
	store.records["valid-key"] = &idempotency.Record{
		Key:       "valid-key",
		ExpiresAt: now.Add(1 * time.Hour),
		Status:    idempotency.StatusCompleted,
	}

	deleted, err := svc.Cleanup(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	_, err = store.Get(ctx, "expired-key")
	assert.ErrorIs(t, err, idempotency.ErrKeyNotFound)

	valid, err := store.Get(ctx, "valid-key")
	assert.NoError(t, err)
	assert.NotNil(t, valid)
}
