package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/example/idempotent-payment-api/internal/idempotency"
)

var (
	// ErrKeyNotFound indicates the idempotency key was not found.
	ErrKeyNotFound = errors.New("idempotency key not found")
)

// IdempotencyRepo manages persistence and concurrency control for idempotency keys.
type IdempotencyRepo struct {
	pool *pgxpool.Pool
}

// NewIdempotencyRepo constructs a new IdempotencyRepo.
func NewIdempotencyRepo(pool *pgxpool.Pool) *IdempotencyRepo {
	return &IdempotencyRepo{pool: pool}
}

// Acquire attempts an atomic INSERT ... ON CONFLICT DO NOTHING to claim an idempotency key.
// If the key is new, Acquired is true. If the key already exists, Acquired is false
// and the existing record is returned for inspection (replay, conflict, or mismatch).
func (r *IdempotencyRepo) Acquire(ctx context.Context, key, requestHash string, ttl time.Duration) (*idempotency.AcquireResult, error) {
	queryInsert := `
		INSERT INTO idempotency_keys (key, request_hash, status, locked_at, expires_at, created_at)
		VALUES ($1, $2, 'in_progress', NOW(), NOW() + ($3 * INTERVAL '1 millisecond'), NOW())
		ON CONFLICT (key) DO NOTHING
		RETURNING key, request_hash, status, response_code, response_body, payment_id, created_at, expires_at, locked_at
	`
	ttlMs := ttl.Milliseconds()
	var rec idempotency.Record
	var statusStr string

	err := r.pool.QueryRow(ctx, queryInsert, key, requestHash, ttlMs).Scan(
		&rec.Key,
		&rec.RequestHash,
		&statusStr,
		&rec.ResponseCode,
		&rec.ResponseBody,
		&rec.PaymentID,
		&rec.CreatedAt,
		&rec.ExpiresAt,
		&rec.LockedAt,
	)

	if err == nil {
		rec.Status = idempotency.Status(statusStr)
		return &idempotency.AcquireResult{
			Acquired: true,
			Record:   &rec,
		}, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("failed to insert idempotency key: %w", err)
	}

	// Key already exists (conflict occurred) - read current record
	existing, err := r.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to read existing key on conflict: %w", err)
	}

	return &idempotency.AcquireResult{
		Acquired: false,
		Record:   existing,
	}, nil
}

// ReclaimStuckLock attempts an atomic UPDATE on a stuck in_progress key whose locked_at
// is older than recoveryTimeout. Returns true if and only if this caller won the reclaim.
func (r *IdempotencyRepo) ReclaimStuckLock(ctx context.Context, key, requestHash string, recoveryTimeout time.Duration) (bool, error) {
	query := `
		UPDATE idempotency_keys
		SET locked_at = NOW(),
		    request_hash = $2
		WHERE key = $1
		  AND status = 'in_progress'
		  AND locked_at < NOW() - ($3 * INTERVAL '1 millisecond')
	`
	timeoutMs := recoveryTimeout.Milliseconds()
	tag, err := r.pool.Exec(ctx, query, key, requestHash, timeoutMs)
	if err != nil {
		return false, fmt.Errorf("failed to execute stuck lock reclaim: %w", err)
	}

	return tag.RowsAffected() == 1, nil
}

// Complete updates an acquired idempotency key to completed status with its HTTP response,
// returning the exact stored byte representation as formatted in PostgreSQL JSONB.
func (r *IdempotencyRepo) Complete(ctx context.Context, key string, statusCode int, responseBody []byte, paymentID *string) ([]byte, error) {
	query := `
		UPDATE idempotency_keys
		SET status = 'completed',
		    response_code = $2,
		    response_body = $3,
		    payment_id = $4
		WHERE key = $1
		RETURNING response_body
	`
	var stored []byte
	err := r.pool.QueryRow(ctx, query, key, statusCode, responseBody, paymentID).Scan(&stored)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("failed to complete idempotency key: %w", err)
	}
	return stored, nil
}

// CompleteTx updates an idempotency key within an active database transaction.
func (r *IdempotencyRepo) CompleteTx(ctx context.Context, tx pgx.Tx, key string, statusCode int, responseBody []byte, paymentID *string) ([]byte, error) {
	query := `
		UPDATE idempotency_keys
		SET status = 'completed',
		    response_code = $2,
		    response_body = $3,
		    payment_id = $4
		WHERE key = $1
		RETURNING response_body
	`
	var stored []byte
	err := tx.QueryRow(ctx, query, key, statusCode, responseBody, paymentID).Scan(&stored)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("failed to complete idempotency key in tx: %w", err)
	}
	return stored, nil
}

// Get retrieves an idempotency record by its key.
func (r *IdempotencyRepo) Get(ctx context.Context, key string) (*idempotency.Record, error) {
	query := `
		SELECT key, request_hash, status, response_code, response_body, payment_id, created_at, expires_at, locked_at
		FROM idempotency_keys
		WHERE key = $1
	`
	var rec idempotency.Record
	var statusStr string

	err := r.pool.QueryRow(ctx, query, key).Scan(
		&rec.Key,
		&rec.RequestHash,
		&statusStr,
		&rec.ResponseCode,
		&rec.ResponseBody,
		&rec.PaymentID,
		&rec.CreatedAt,
		&rec.ExpiresAt,
		&rec.LockedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("failed to query idempotency key: %w", err)
	}
	rec.Status = idempotency.Status(statusStr)
	return &rec, nil
}

// DeleteExpired removes keys that have passed their expiration deadline.
func (r *IdempotencyRepo) DeleteExpired(ctx context.Context) (int64, error) {
	query := `DELETE FROM idempotency_keys WHERE expires_at < NOW()`
	tag, err := r.pool.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired keys: %w", err)
	}
	return tag.RowsAffected(), nil
}
