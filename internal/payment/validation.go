package payment

import (
	"strings"
)

const maxDescriptionLength = 500

// ValidateCreatePayment validates the incoming payment creation payload.
func ValidateCreatePayment(input CreatePaymentInput) error {
	if err := ValidateIdempotencyKey(input.IdempotencyKey); err != nil {
		return err
	}

	if input.Amount <= 0 {
		return ErrInvalidAmount
	}

	if err := ValidateCurrency(input.Currency); err != nil {
		return err
	}

	if strings.TrimSpace(input.CustomerID) == "" {
		return ErrMissingCustomerID
	}

	if len(input.Description) > maxDescriptionLength {
		return ErrDescriptionTooLong
	}

	return nil
}

// ValidateCurrency ensures the currency is one of INR, USD, EUR.
func ValidateCurrency(c Currency) error {
	switch c {
	case CurrencyINR, CurrencyUSD, CurrencyEUR:
		return nil
	default:
		return ErrInvalidCurrency
	}
}

// ValidateIdempotencyKey verifies key length constraints (1-255 characters).
func ValidateIdempotencyKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) == 0 || len(key) > 255 {
		return ErrInvalidIdempotencyKey
	}
	return nil
}
