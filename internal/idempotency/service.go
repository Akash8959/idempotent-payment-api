package idempotency

import (
	"context"
	"fmt"
	"time"
)

// AcquireResult captures whether a key was acquired or already exists.
type AcquireResult struct {
	Acquired bool
	Record   *Record
}

// Store defines persistence operations required for idempotency key management.
type Store interface {
	Acquire(ctx context.Context, key, hash string, ttl time.Duration) (*AcquireResult, error)
	ReclaimStuckLock(ctx context.Context, key, hash string, recoveryTimeout time.Duration) (bool, error)
	Complete(ctx context.Context, key string, statusCode int, responseBody []byte, paymentID *string) ([]byte, error)
	Get(ctx context.Context, key string) (*Record, error)
	DeleteExpired(ctx context.Context) (int64, error)
}

// Service coordinates idempotency decision logic and key lifecycle operations.
type Service struct {
	store             Store
	keyTTL            time.Duration
	recoveryTimeout   time.Duration
	retryAfterSeconds int
}

// NewService constructs a new idempotency service.
func NewService(store Store, keyTTL, recoveryTimeout time.Duration) *Service {
	return &Service{
		store:             store,
		keyTTL:            keyTTL,
		recoveryTimeout:   recoveryTimeout,
		retryAfterSeconds: 5,
	}
}

// Evaluate determines whether a request is a fresh execution, a valid replay,
// a concurrent conflict, or a payload mismatch.
func (s *Service) Evaluate(ctx context.Context, key, method, path string, rawBody []byte) (*ExecutionDecision, error) {
	reqHash, err := ComputeRequestHash(method, path, rawBody)
	if err != nil {
		return nil, fmt.Errorf("failed to compute request hash: %w", err)
	}

	acquireRes, err := s.store.Acquire(ctx, key, reqHash, s.keyTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire idempotency key: %w", err)
	}

	// 1. Brand new key claimed: proceed with payment creation
	if acquireRes.Acquired {
		return &ExecutionDecision{
			Action: ActionExecute,
		}, nil
	}

	existing := acquireRes.Record
	if existing == nil {
		return nil, fmt.Errorf("idempotency record missing after conflict check")
	}

	now := time.Now().UTC()

	// 2. Previously completed request: check hash match
	if existing.Status == StatusCompleted {
		if existing.RequestHash != reqHash {
			return &ExecutionDecision{
				Action: ActionMismatch,
			}, nil
		}

		code := 200
		if existing.ResponseCode != nil {
			code = *existing.ResponseCode
		}

		return &ExecutionDecision{
			Action:             ActionReplay,
			StoredResponseCode: code,
			StoredResponseBody: existing.ResponseBody,
			PaymentID:          existing.PaymentID,
		}, nil
	}

	// 3. Key is in_progress: check if stuck beyond recovery timeout
	if existing.Status == StatusInProgress {
		if existing.IsStuck(now, s.recoveryTimeout) {
			// Stuck lock recovery: attempt atomic reclaim
			reclaimed, err := s.store.ReclaimStuckLock(ctx, key, reqHash, s.recoveryTimeout)
			if err != nil {
				return nil, fmt.Errorf("failed to reclaim stuck lock: %w", err)
			}
			if reclaimed {
				// Reclaim won: this worker proceeds to execute
				return &ExecutionDecision{
					Action: ActionExecute,
				}, nil
			}
			// Another concurrent worker won the reclaim: return 409
			return &ExecutionDecision{
				Action:            ActionConflict,
				RetryAfterSeconds: s.retryAfterSeconds,
			}, nil
		}

		// Actively running under threshold: return 409 Conflict with Retry-After
		return &ExecutionDecision{
			Action:            ActionConflict,
			RetryAfterSeconds: s.retryAfterSeconds,
		}, nil
	}

	return nil, fmt.Errorf("unknown idempotency status: %s", existing.Status)
}

// Complete saves the final HTTP response status code and JSON body against the key,
// returning the stored byte representation as persisted in the database.
func (s *Service) Complete(ctx context.Context, key string, statusCode int, responseBody []byte, paymentID *string) ([]byte, error) {
	return s.store.Complete(ctx, key, statusCode, responseBody, paymentID)
}

// Cleanup removes expired keys from storage.
func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	return s.store.DeleteExpired(ctx)
}
