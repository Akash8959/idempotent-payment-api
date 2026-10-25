package payment_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/idempotent-payment-api/internal/payment"
)

// mockRepo implements payment.Repository in-memory.
type mockRepo struct {
	payments  map[string]*payment.Payment
	createErr error
	updateErr error
}

func newMockRepo() *mockRepo {
	return &mockRepo{payments: make(map[string]*payment.Payment)}
}

func (m *mockRepo) Create(ctx context.Context, p *payment.Payment) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.payments[p.ID] = p
	return nil
}

func (m *mockRepo) GetByID(ctx context.Context, id string) (*payment.Payment, error) {
	p, ok := m.payments[id]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	// Return copy
	cpy := *p
	return &cpy, nil
}

func (m *mockRepo) UpdateStatus(ctx context.Context, id string, status payment.Status) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	p, ok := m.payments[id]
	if !ok {
		return payment.ErrPaymentNotFound
	}
	p.Status = status
	p.UpdatedAt = time.Now().UTC()
	return nil
}

func TestPaymentService_CreatePending(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepo()
	svc := payment.NewService(repo)

	input := payment.CreatePaymentInput{
		IdempotencyKey: "key-123",
		Amount:         2500,
		Currency:       payment.CurrencyUSD,
		CustomerID:     "cus_abc",
		Description:    "Test Payment",
	}

	p, err := svc.CreatePending(ctx, input)
	require.NoError(t, err)
	assert.NotEmpty(t, p.ID)
	assert.Equal(t, payment.StatusPending, p.Status)
	assert.Equal(t, int64(2500), p.Amount)
	assert.Equal(t, payment.CurrencyUSD, p.Currency)
	assert.Equal(t, "cus_abc", p.CustomerID)
	assert.Equal(t, "Test Payment", p.Description)

	// Test validation error propagation
	invalidInput := input
	invalidInput.Amount = -1
	_, err = svc.CreatePending(ctx, invalidInput)
	require.ErrorIs(t, err, payment.ErrInvalidAmount)

	// Test repo error propagation
	repo.createErr = errors.New("db error")
	_, err = svc.CreatePending(ctx, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save payment")
}

func TestPaymentService_UpdatePaymentStatus(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepo()
	svc := payment.NewService(repo)

	input := payment.CreatePaymentInput{
		IdempotencyKey: "key-123",
		Amount:         1000,
		Currency:       payment.CurrencyEUR,
		CustomerID:     "cus_xyz",
	}

	p, err := svc.CreatePending(ctx, input)
	require.NoError(t, err)

	// Valid transition: pending -> succeeded
	updated, err := svc.UpdatePaymentStatus(ctx, p.ID, payment.StatusSucceeded)
	require.NoError(t, err)
	assert.Equal(t, payment.StatusSucceeded, updated.Status)

	// Illegal transition: succeeded -> failed
	_, err = svc.UpdatePaymentStatus(ctx, p.ID, payment.StatusFailed)
	require.ErrorIs(t, err, payment.ErrInvalidStateTransition)

	// Unknown payment id
	_, err = svc.UpdatePaymentStatus(ctx, "non-existent-id", payment.StatusSucceeded)
	require.ErrorIs(t, err, payment.ErrPaymentNotFound)
}

func TestPaymentService_GetPayment(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepo()
	svc := payment.NewService(repo)

	// Empty ID returns ErrPaymentNotFound
	_, err := svc.GetPayment(ctx, "")
	require.ErrorIs(t, err, payment.ErrPaymentNotFound)

	// Non-existent ID returns ErrPaymentNotFound
	_, err = svc.GetPayment(ctx, "missing")
	require.ErrorIs(t, err, payment.ErrPaymentNotFound)

	// Successfully retrieve existing payment
	created, err := svc.CreatePending(ctx, payment.CreatePaymentInput{
		IdempotencyKey: "key-456",
		Amount:         500,
		Currency:       payment.CurrencyINR,
		CustomerID:     "cus_456",
	})
	require.NoError(t, err)

	fetched, err := svc.GetPayment(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, created.Amount, fetched.Amount)
}
