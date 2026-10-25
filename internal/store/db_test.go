package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/idempotent-payment-api/internal/config"
	"github.com/example/idempotent-payment-api/internal/store"
)

func TestStore_ErrorDefinitions(t *testing.T) {
	assert.Equal(t, "payment not found", store.ErrPaymentNotFound.Error())
	assert.Equal(t, "idempotency key not found", store.ErrKeyNotFound.Error())
}

func TestStore_NewPool_InvalidConfig(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host:     "invalid.non-existent.host.local",
		Port:     "9999",
		User:     "fake_user",
		Password: "fake_password",
		Name:     "fake_db",
		SSLMode:  "disable",
		MaxConns: 5,
		MinConns: 1,
	}

	_, err := store.NewPool(context.Background(), cfg)
	require.Error(t, err)
}

func TestStore_Migrate_InvalidDSN(t *testing.T) {
	err := store.MigrateUp("invalid-dsn-string")
	require.Error(t, err)

	err = store.MigrateDown("invalid-dsn-string")
	require.Error(t, err)
}
