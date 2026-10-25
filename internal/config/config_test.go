package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/Akash8959/idempotent-payment-api/internal/config"
)

func TestConfig_DefaultValues(t *testing.T) {
	// Ensure clean environment for test keys
	cleanKeys := []string{
		"PORT", "ENV", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD",
		"DB_NAME", "DB_SSLMODE", "DB_MAX_CONNS", "DB_MIN_CONNS",
		"IDEMPOTENCY_KEY_TTL", "IDEMPOTENCY_RECOVERY_TIMEOUT",
		"PROCESSOR_DEFAULT_MODE", "LOG_LEVEL",
	}
	for _, k := range cleanKeys {
		os.Unsetenv(k)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected config to load with defaults, got error: %v", err)
	}

	if cfg.Server.Port != "8080" {
		t.Errorf("expected default Port 8080, got %s", cfg.Server.Port)
	}
	if cfg.Database.Name != "payment_db" {
		t.Errorf("expected default DB_NAME payment_db, got %s", cfg.Database.Name)
	}
	if cfg.Idempotency.KeyTTL != 24*time.Hour {
		t.Errorf("expected default KeyTTL 24h, got %v", cfg.Idempotency.KeyTTL)
	}
	if cfg.Idempotency.RecoveryTimeout != 30*time.Second {
		t.Errorf("expected default RecoveryTimeout 30s, got %v", cfg.Idempotency.RecoveryTimeout)
	}
	if cfg.Processor.DefaultMode != "success" {
		t.Errorf("expected default processor mode 'success', got %s", cfg.Processor.DefaultMode)
	}

	expectedDSN := "postgres://postgres:postgres@localhost:5432/payment_db?sslmode=disable"
	if cfg.Database.DSN() != expectedDSN {
		t.Errorf("expected DSN %s, got %s", expectedDSN, cfg.Database.DSN())
	}
}

func TestConfig_CustomEnvironment(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_NAME", "custom_payments")
	t.Setenv("IDEMPOTENCY_KEY_TTL", "12h")
	t.Setenv("PROCESSOR_DEFAULT_MODE", "failure")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load custom config: %v", err)
	}

	if cfg.Server.Port != "9090" {
		t.Errorf("expected Port 9090, got %s", cfg.Server.Port)
	}
	if cfg.Database.Host != "db.internal" {
		t.Errorf("expected DB_HOST db.internal, got %s", cfg.Database.Host)
	}
	if cfg.Database.Name != "custom_payments" {
		t.Errorf("expected DB_NAME custom_payments, got %s", cfg.Database.Name)
	}
	if cfg.Idempotency.KeyTTL != 12*time.Hour {
		t.Errorf("expected KeyTTL 12h, got %v", cfg.Idempotency.KeyTTL)
	}
	if cfg.Processor.DefaultMode != "failure" {
		t.Errorf("expected processor mode failure, got %s", cfg.Processor.DefaultMode)
	}
}

func TestConfig_ValidationFailures(t *testing.T) {
	testCases := []struct {
		name    string
		envKey  string
		envVal  string
		wantErr string
	}{
		{
			name:    "invalid processor mode",
			envKey:  "PROCESSOR_DEFAULT_MODE",
			envVal:  "invalid_mode",
			wantErr: "unsupported PROCESSOR_DEFAULT_MODE",
		},
		{
			name:    "negative key TTL",
			envKey:  "IDEMPOTENCY_KEY_TTL",
			envVal:  "-5m",
			wantErr: "IDEMPOTENCY_KEY_TTL must be positive",
		},
		{
			name:    "negative recovery timeout",
			envKey:  "IDEMPOTENCY_RECOVERY_TIMEOUT",
			envVal:  "-1s",
			wantErr: "IDEMPOTENCY_RECOVERY_TIMEOUT must be positive",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envKey, tc.envVal)
			_, err := config.Load()
			if err == nil {
				t.Fatalf("expected validation error containing %q, got nil", tc.wantErr)
			}
		})
	}
}
