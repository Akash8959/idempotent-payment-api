package processor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/idempotent-payment-api/internal/processor"
)

func TestSimulatedProcessor(t *testing.T) {
	req := processor.PaymentRequest{
		PaymentID:  "pay_123",
		CustomerID: "cus_abc",
		Amount:     1500,
		Currency:   "INR",
	}

	t.Run("success mode produces successful result", func(t *testing.T) {
		proc := processor.NewSimulatedProcessor(processor.ModeSuccess)
		proc.SetLatency(1 * time.Millisecond)

		res, err := proc.Process(context.Background(), req)
		require.NoError(t, err)
		assert.True(t, res.Success)
		assert.Contains(t, res.ProcessorTransactionID, "txn_ok_")
		assert.Empty(t, res.FailureReason)
	})

	t.Run("failure mode produces declined result", func(t *testing.T) {
		proc := processor.NewSimulatedProcessor(processor.ModeFailure)
		proc.SetLatency(1 * time.Millisecond)

		res, err := proc.Process(context.Background(), req)
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.Contains(t, res.ProcessorTransactionID, "txn_failed_")
		assert.Equal(t, "card_declined_insufficient_funds", res.FailureReason)
	})

	t.Run("timeout mode respects context cancellation", func(t *testing.T) {
		proc := processor.NewSimulatedProcessor(processor.ModeTimeout)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		res, err := proc.Process(ctx, req)
		require.Error(t, err)
		assert.Nil(t, res)
		assert.ErrorIs(t, err, processor.ErrProcessorTimeout)
	})

	t.Run("runtime mode changes work dynamically", func(t *testing.T) {
		proc := processor.NewSimulatedProcessor(processor.ModeSuccess)
		proc.SetLatency(1 * time.Millisecond)

		res1, err := proc.Process(context.Background(), req)
		require.NoError(t, err)
		assert.True(t, res1.Success)

		proc.SetMode(processor.ModeFailure)
		res2, err := proc.Process(context.Background(), req)
		require.NoError(t, err)
		assert.False(t, res2.Success)
	})
}
