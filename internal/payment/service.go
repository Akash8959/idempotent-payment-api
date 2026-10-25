package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Repository abstracts the storage operations required by the payment domain service.
type Repository interface {
	Create(ctx context.Context, p *Payment) error
	GetByID(ctx context.Context, id string) (*Payment, error)
	UpdateStatus(ctx context.Context, id string, status Status) error
}

// Service provides payment orchestration and domain rule enforcement.
type Service struct {
	repo Repository
}

// NewService constructs a new payment domain service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// CreatePending initializes a validated payment in the 'pending' state.
func (s *Service) CreatePending(ctx context.Context, input CreatePaymentInput) (*Payment, error) {
	if err := ValidateCreatePayment(input); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	p := &Payment{
		ID:          uuid.NewString(),
		CustomerID:  input.CustomerID,
		Amount:      input.Amount,
		Currency:    input.Currency,
		Description: input.Description,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.repo.Create(ctx, p); err != nil {
		return nil, fmt.Errorf("failed to save payment: %w", err)
	}

	return p, nil
}

// UpdatePaymentStatus transitions a payment to a new status obeying the state machine.
func (s *Service) UpdatePaymentStatus(ctx context.Context, id string, target Status) (*Payment, error) {
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	nextStatus, err := Transition(existing.Status, target)
	if err != nil {
		return nil, err
	}

	if err := s.repo.UpdateStatus(ctx, id, nextStatus); err != nil {
		return nil, fmt.Errorf("failed to persist updated status: %w", err)
	}

	existing.Status = nextStatus
	existing.UpdatedAt = time.Now().UTC()
	return existing, nil
}

// GetPayment retrieves a payment by its unique ID.
func (s *Service) GetPayment(ctx context.Context, id string) (*Payment, error) {
	if id == "" {
		return nil, ErrPaymentNotFound
	}
	return s.repo.GetByID(ctx, id)
}
