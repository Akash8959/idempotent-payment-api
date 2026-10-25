// Package payment encapsulates the payment domain entity, validation rules,
// and state machine transitions.
package payment

import (
	"errors"
	"time"
)

// Currency represents supported ISO currency codes.
type Currency string

const (
	CurrencyINR Currency = "INR"
	CurrencyUSD Currency = "USD"
	CurrencyEUR Currency = "EUR"
)

// Status represents the lifecycle status of a payment.
type Status string

const (
	StatusPending   Status = "pending"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

var (
	ErrInvalidAmount          = errors.New("amount must be an integer greater than zero")
	ErrInvalidCurrency        = errors.New("currency must be one of INR, USD, EUR")
	ErrMissingCustomerID      = errors.New("customer_id is required")
	ErrDescriptionTooLong     = errors.New("description cannot exceed 500 characters")
	ErrInvalidIdempotencyKey  = errors.New("idempotency key must be between 1 and 255 characters")
	ErrInvalidStateTransition = errors.New("invalid payment state transition")
	ErrPaymentNotFound        = errors.New("payment not found")
)

// Payment is the core domain model representing a financial charge.
type Payment struct {
	ID          string    `json:"id"`
	CustomerID  string    `json:"customer_id"`
	Amount      int64     `json:"amount"` // In smallest currency unit (e.g. paise, cents)
	Currency    Currency  `json:"currency"`
	Description string    `json:"description,omitempty"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreatePaymentInput defines the parameters for creating a new payment.
type CreatePaymentInput struct {
	IdempotencyKey string   `json:"-"`
	Amount         int64    `json:"amount"`
	Currency       Currency `json:"currency"`
	CustomerID     string   `json:"customer_id"`
	Description    string   `json:"description,omitempty"`
}
