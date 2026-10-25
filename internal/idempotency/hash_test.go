package idempotency_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/idempotent-payment-api/internal/idempotency"
)

func TestCanonicalizeJSON(t *testing.T) {
	t.Run("sorts keys deterministically", func(t *testing.T) {
		jsonA := []byte(`{"customer_id": "cus_123", "amount": 1500, "currency": "INR"}`)
		jsonB := []byte(`{"currency": "INR", "amount": 1500, "customer_id": "cus_123"}`)

		canonicalA, errA := idempotency.CanonicalizeJSON(jsonA)
		require.NoError(t, errA)

		canonicalB, errB := idempotency.CanonicalizeJSON(jsonB)
		require.NoError(t, errB)

		assert.Equal(t, string(canonicalA), string(canonicalB))
	})

	t.Run("handles nested objects with different key order", func(t *testing.T) {
		jsonA := []byte(`{"data": {"z": 1, "a": 2}, "meta": {"foo": "bar"}}`)
		jsonB := []byte(`{"meta": {"foo": "bar"}, "data": {"a": 2, "z": 1}}`)

		canonicalA, err := idempotency.CanonicalizeJSON(jsonA)
		require.NoError(t, err)

		canonicalB, err := idempotency.CanonicalizeJSON(jsonB)
		require.NoError(t, err)

		assert.Equal(t, string(canonicalA), string(canonicalB))
	})

	t.Run("strips formatting whitespace", func(t *testing.T) {
		jsonCompact := []byte(`{"amount":1500,"currency":"USD"}`)
		jsonSpaced := []byte(`{
			"amount": 1500,
			"currency": "USD"
		}`)

		canonicalCompact, err := idempotency.CanonicalizeJSON(jsonCompact)
		require.NoError(t, err)

		canonicalSpaced, err := idempotency.CanonicalizeJSON(jsonSpaced)
		require.NoError(t, err)

		assert.Equal(t, string(canonicalCompact), string(canonicalSpaced))
	})

	t.Run("empty or whitespace returns empty bytes", func(t *testing.T) {
		canonical, err := idempotency.CanonicalizeJSON([]byte("   "))
		require.NoError(t, err)
		assert.Empty(t, canonical)
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		_, err := idempotency.CanonicalizeJSON([]byte(`{not valid json}`))
		require.Error(t, err)
	})
}

func TestComputeRequestHash(t *testing.T) {
	method := "POST"
	path := "/v1/payments"

	t.Run("identical payload with different field ordering produces identical hash", func(t *testing.T) {
		body1 := []byte(`{"amount": 1000, "currency": "INR", "customer_id": "c1"}`)
		body2 := []byte(`{"customer_id": "c1", "currency": "INR", "amount": 1000}`)

		hash1, err1 := idempotency.ComputeRequestHash(method, path, body1)
		require.NoError(t, err1)

		hash2, err2 := idempotency.ComputeRequestHash(method, path, body2)
		require.NoError(t, err2)

		assert.Equal(t, hash1, hash2, "hashes must be identical despite JSON key ordering")
		assert.Len(t, hash1, 64, "SHA-256 hex string must be 64 characters")
	})

	t.Run("different payload produces different hash", func(t *testing.T) {
		body1 := []byte(`{"amount": 1000, "currency": "INR", "customer_id": "c1"}`)
		body2 := []byte(`{"amount": 2000, "currency": "INR", "customer_id": "c1"}`)

		hash1, err1 := idempotency.ComputeRequestHash(method, path, body1)
		require.NoError(t, err1)

		hash2, err2 := idempotency.ComputeRequestHash(method, path, body2)
		require.NoError(t, err2)

		assert.NotEqual(t, hash1, hash2, "hashes must differ for different amounts")
	})

	t.Run("different HTTP method produces different hash", func(t *testing.T) {
		body := []byte(`{"amount": 1000}`)
		hashPOST, err := idempotency.ComputeRequestHash("POST", path, body)
		require.NoError(t, err)

		hashPUT, err := idempotency.ComputeRequestHash("PUT", path, body)
		require.NoError(t, err)

		assert.NotEqual(t, hashPOST, hashPUT)
	})

	t.Run("different path produces different hash", func(t *testing.T) {
		body := []byte(`{"amount": 1000}`)
		hashA, err := idempotency.ComputeRequestHash(method, "/v1/payments", body)
		require.NoError(t, err)

		hashB, err := idempotency.ComputeRequestHash(method, "/v2/payments", body)
		require.NoError(t, err)

		assert.NotEqual(t, hashA, hashB)
	})
}
