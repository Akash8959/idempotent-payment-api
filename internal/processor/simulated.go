package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Mode determines the behavior of the simulated payment processor.
type Mode string

const (
	ModeSuccess Mode = "success"
	ModeFailure Mode = "failure"
	ModeTimeout Mode = "timeout"
)

// SimulatedProcessor provides a testable in-memory payment processor.
type SimulatedProcessor struct {
	mu      sync.RWMutex
	mode    Mode
	latency time.Duration
}

// NewSimulatedProcessor constructs a simulated processor with a default mode.
func NewSimulatedProcessor(mode Mode) *SimulatedProcessor {
	return &SimulatedProcessor{
		mode:    mode,
		latency: 10 * time.Millisecond,
	}
}

// SetMode dynamically overrides the simulation mode at runtime.
func (s *SimulatedProcessor) SetMode(m Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m
}

// SetLatency overrides artificial processing delay.
func (s *SimulatedProcessor) SetLatency(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = d
}

// Process executes the simulated charge honoring the configured mode and context deadline.
func (s *SimulatedProcessor) Process(ctx context.Context, req PaymentRequest) (*ProcessResult, error) {
	s.mu.RLock()
	currentMode := s.mode
	currentLatency := s.latency
	s.mu.RUnlock()

	switch currentMode {
	case ModeTimeout:
		// Simulate upstream hanging until context is cancelled or times out
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ErrProcessorTimeout, ctx.Err())
		case <-time.After(35 * time.Second):
			return nil, ErrProcessorTimeout
		}

	case ModeFailure:
		// Simulate network latency then return a declined transaction
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(currentLatency):
		}

		return &ProcessResult{
			ProcessorTransactionID: fmt.Sprintf("txn_failed_%s", uuid.NewString()[:8]),
			Success:                false,
			FailureReason:          "card_declined_insufficient_funds",
		}, nil

	case ModeSuccess:
		fallthrough
	default:
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(currentLatency):
		}

		return &ProcessResult{
			ProcessorTransactionID: fmt.Sprintf("txn_ok_%s", uuid.NewString()[:8]),
			Success:                true,
		}, nil
	}
}
