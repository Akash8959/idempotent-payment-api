// Package processor defines the abstraction for external payment gateways
// and provides a configurable simulated processor for local and automated testing.
package processor

import (
	"context"
	"errors"
)

var (
	// ErrProcessorTimeout indicates the gateway did not respond within the allocated deadline.
	ErrProcessorTimeout = errors.New("payment processor timed out")
	// ErrProcessorUnavailable indicates the upstream gateway is temporarily unreachable.
	ErrProcessorUnavailable = errors.New("payment processor temporarily unavailable")
)

// PaymentRequest contains the information sent to the external gateway.
type PaymentRequest struct {
	PaymentID   string
	CustomerID  string
	Amount      int64
	Currency    string
	Description string
}

// ProcessResult represents the gateway's processing outcome.
type ProcessResult struct {
	ProcessorTransactionID string
	Success                bool
	FailureReason          string
}

// Processor defines the contract for communicating with payment gateways.
type Processor interface {
	Process(ctx context.Context, req PaymentRequest) (*ProcessResult, error)
}
