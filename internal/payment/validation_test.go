package payment_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Akash8959/idempotent-payment-api/internal/payment"
)

func TestValidateCreatePayment(t *testing.T) {
	validInput := payment.CreatePaymentInput{
		IdempotencyKey: "test-key-123",
		Amount:         1500,
		Currency:       payment.CurrencyINR,
		CustomerID:     "cus_123",
		Description:    "Monthly Subscription",
	}

	t.Run("valid input passes", func(t *testing.T) {
		err := payment.ValidateCreatePayment(validInput)
		assert.NoError(t, err)
	})

	t.Run("amount less than or equal to zero fails", func(t *testing.T) {
		in := validInput
		in.Amount = 0
		err := payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrInvalidAmount)

		in.Amount = -500
		err = payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrInvalidAmount)
	})

	t.Run("unsupported currency fails", func(t *testing.T) {
		in := validInput
		in.Currency = "GBP"
		err := payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrInvalidCurrency)

		in.Currency = ""
		err = payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrInvalidCurrency)
	})

	t.Run("supported currencies succeed", func(t *testing.T) {
		for _, c := range []payment.Currency{payment.CurrencyINR, payment.CurrencyUSD, payment.CurrencyEUR} {
			in := validInput
			in.Currency = c
			assert.NoError(t, payment.ValidateCreatePayment(in))
		}
	})

	t.Run("missing or whitespace customer_id fails", func(t *testing.T) {
		in := validInput
		in.CustomerID = ""
		err := payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrMissingCustomerID)

		in.CustomerID = "   "
		err = payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrMissingCustomerID)
	})

	t.Run("oversize description (> 500 chars) fails", func(t *testing.T) {
		in := validInput
		in.Description = strings.Repeat("a", 501)
		err := payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrDescriptionTooLong)

		// Exactly 500 should pass
		in.Description = strings.Repeat("a", 500)
		assert.NoError(t, payment.ValidateCreatePayment(in))
	})

	t.Run("idempotency key empty, whitespace, or over 255 chars fails", func(t *testing.T) {
		in := validInput
		in.IdempotencyKey = ""
		err := payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrInvalidIdempotencyKey)

		in.IdempotencyKey = "   "
		err = payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrInvalidIdempotencyKey)

		in.IdempotencyKey = strings.Repeat("k", 256)
		err = payment.ValidateCreatePayment(in)
		require.ErrorIs(t, err, payment.ErrInvalidIdempotencyKey)

		// Exactly 255 chars should pass
		in.IdempotencyKey = strings.Repeat("k", 255)
		assert.NoError(t, payment.ValidateCreatePayment(in))
	})
}
