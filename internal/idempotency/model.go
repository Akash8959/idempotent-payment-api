// Package idempotency implements request deduplication, canonical request hashing,
// database-backed concurrency control, and stuck lock recovery.
package idempotency

import (
	"errors"
	"time"
)

// Status represents the processing state of an idempotency key.
type Status string

const (
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

var (
	ErrPayloadMismatch = errors.New("idempotency key was previously used with a different request payload")
	ErrInProgress      = errors.New("a request with this idempotency key is currently in progress")
	ErrKeyNotFound     = errors.New("idempotency key not found")
	ErrKeyExpired      = errors.New("idempotency key has expired")
)

// Action dictates the decision made by the idempotency coordinator.
type Action int

const (
	ActionExecute  Action = iota // First request or recovered key: proceed with execution
	ActionReplay                 // Completed request with matching hash: return cached response
	ActionConflict               // Currently in flight by another worker: return 409
	ActionMismatch               // Key reused with different hash: return 422
)

// ExecutionDecision encapsulates the required transport action.
type ExecutionDecision struct {
	Action             Action
	StoredResponseCode int
	StoredResponseBody []byte
	PaymentID          *string
	RetryAfterSeconds  int
}

// Record holds persistent state for an idempotency key.
type Record struct {
	Key          string
	RequestHash  string
	Status       Status
	ResponseCode *int
	ResponseBody []byte
	PaymentID    *string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	LockedAt     time.Time
}

// IsExpired reports whether the record has passed its TTL.
func (r *Record) IsExpired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt)
}

// IsStuck reports whether an in_progress key has exceeded the recovery timeout.
func (r *Record) IsStuck(now time.Time, recoveryTimeout time.Duration) bool {
	if r.Status != StatusInProgress {
		return false
	}
	return now.Sub(r.LockedAt) >= recoveryTimeout
}
