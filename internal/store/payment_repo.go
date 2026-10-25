package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/example/idempotent-payment-api/internal/payment"
)

var (
	// ErrPaymentNotFound is returned when a requested payment does not exist.
	ErrPaymentNotFound = errors.New("payment not found")
)

// PaymentRepo handles persistence of payments in PostgreSQL.
type PaymentRepo struct {
	pool *pgxpool.Pool
}

// NewPaymentRepo constructs a new PaymentRepo.
func NewPaymentRepo(pool *pgxpool.Pool) *PaymentRepo {
	return &PaymentRepo{pool: pool}
}

// Create inserts a new payment record.
func (r *PaymentRepo) Create(ctx context.Context, p *payment.Payment) error {
	query := `
		INSERT INTO payments (id, customer_id, amount, currency, description, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}

	_, err := r.pool.Exec(ctx, query,
		p.ID, p.CustomerID, p.Amount, string(p.Currency), p.Description, string(p.Status), p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert payment: %w", err)
	}
	return nil
}

// CreateTx inserts a new payment record within an active database transaction.
func (r *PaymentRepo) CreateTx(ctx context.Context, tx pgx.Tx, p *payment.Payment) error {
	query := `
		INSERT INTO payments (id, customer_id, amount, currency, description, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}

	_, err := tx.Exec(ctx, query,
		p.ID, p.CustomerID, p.Amount, string(p.Currency), p.Description, string(p.Status), p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert payment in tx: %w", err)
	}
	return nil
}

// GetByID retrieves a payment by its unique ID.
func (r *PaymentRepo) GetByID(ctx context.Context, id string) (*payment.Payment, error) {
	query := `
		SELECT id, customer_id, amount, currency, COALESCE(description, ''), status, created_at, updated_at
		FROM payments
		WHERE id = $1
	`
	var p payment.Payment
	var curStr, statusStr string

	err := r.pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.CustomerID, &p.Amount, &curStr, &p.Description, &statusStr, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, payment.ErrPaymentNotFound
		}
		return nil, fmt.Errorf("failed to query payment by id: %w", err)
	}
	p.Currency = payment.Currency(curStr)
	p.Status = payment.Status(statusStr)
	return &p, nil
}

// UpdateStatus updates the status and updated_at timestamp of a payment.
func (r *PaymentRepo) UpdateStatus(ctx context.Context, id string, status payment.Status) error {
	query := `
		UPDATE payments
		SET status = $2, updated_at = NOW()
		WHERE id = $1
	`
	tag, err := r.pool.Exec(ctx, query, id, string(status))
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return payment.ErrPaymentNotFound
	}
	return nil
}

// UpdateStatusTx updates the status of a payment within an active transaction.
func (r *PaymentRepo) UpdateStatusTx(ctx context.Context, tx pgx.Tx, id string, status payment.Status) error {
	query := `
		UPDATE payments
		SET status = $2, updated_at = NOW()
		WHERE id = $1
	`
	tag, err := tx.Exec(ctx, query, id, string(status))
	if err != nil {
		return fmt.Errorf("failed to update payment status in tx: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return payment.ErrPaymentNotFound
	}
	return nil
}
