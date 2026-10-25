package payment_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/idempotent-payment-api/internal/payment"
)

func TestStateMachine(t *testing.T) {
	testCases := []struct {
		name        string
		from        payment.Status
		to          payment.Status
		shouldAllow bool
	}{
		// Valid transitions
		{
			name:        "pending to succeeded",
			from:        payment.StatusPending,
			to:          payment.StatusSucceeded,
			shouldAllow: true,
		},
		{
			name:        "pending to failed",
			from:        payment.StatusPending,
			to:          payment.StatusFailed,
			shouldAllow: true,
		},
		// Invalid transitions from pending
		{
			name:        "pending to pending",
			from:        payment.StatusPending,
			to:          payment.StatusPending,
			shouldAllow: false,
		},
		// Invalid transitions from succeeded (terminal)
		{
			name:        "succeeded to pending",
			from:        payment.StatusSucceeded,
			to:          payment.StatusPending,
			shouldAllow: false,
		},
		{
			name:        "succeeded to failed",
			from:        payment.StatusSucceeded,
			to:          payment.StatusFailed,
			shouldAllow: false,
		},
		{
			name:        "succeeded to succeeded",
			from:        payment.StatusSucceeded,
			to:          payment.StatusSucceeded,
			shouldAllow: false,
		},
		// Invalid transitions from failed (terminal)
		{
			name:        "failed to pending",
			from:        payment.StatusFailed,
			to:          payment.StatusPending,
			shouldAllow: false,
		},
		{
			name:        "failed to succeeded",
			from:        payment.StatusFailed,
			to:          payment.StatusSucceeded,
			shouldAllow: false,
		},
		{
			name:        "failed to failed",
			from:        payment.StatusFailed,
			to:          payment.StatusFailed,
			shouldAllow: false,
		},
		// Unknown statuses
		{
			name:        "unknown to succeeded",
			from:        "unknown",
			to:          payment.StatusSucceeded,
			shouldAllow: false,
		},
		{
			name:        "pending to unknown",
			from:        payment.StatusPending,
			to:          "unknown",
			shouldAllow: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			allowed := payment.CanTransition(tc.from, tc.to)
			assert.Equal(t, tc.shouldAllow, allowed, "CanTransition check mismatch")

			next, err := payment.Transition(tc.from, tc.to)
			if tc.shouldAllow {
				require.NoError(t, err)
				assert.Equal(t, tc.to, next)
			} else {
				require.Error(t, err)
				require.ErrorIs(t, err, payment.ErrInvalidStateTransition)
				assert.Equal(t, tc.from, next)
			}
		})
	}
}
