// Package config handles environment-driven application configuration.
// It loads settings from environment variables, sets sensible production defaults,
// and validates critical values at startup to fail fast if improperly configured.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config aggregates all subsystem configuration for the payment API service.
type Config struct {
	Server      ServerConfig
	Database    DatabaseConfig
	Idempotency IdempotencyConfig
	Processor   ProcessorConfig
	Log         LogConfig
}

// ServerConfig holds HTTP server tuning parameters.
type ServerConfig struct {
	Port         string
	Env          string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	ShutdownWait time.Duration
}

// DatabaseConfig holds PostgreSQL connection parameters and pool settings.
type DatabaseConfig struct {
	Host            string
	Port            string
	User            string
	Password        string
	Name            string
	SSLMode         string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DSN builds a PostgreSQL connection string suitable for pgxpool.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode,
	)
}

// IdempotencyConfig holds TTL and recovery rules for idempotency keys.
type IdempotencyConfig struct {
	KeyTTL          time.Duration
	RecoveryTimeout time.Duration
}

// ProcessorConfig holds simulation and execution settings for the payment processor.
type ProcessorConfig struct {
	DefaultMode string // "success", "failure", "timeout"
	Timeout     time.Duration
}

// LogConfig holds structured logging configuration.
type LogConfig struct {
	Level string // "DEBUG", "INFO", "WARN", "ERROR"
}

// Load reads configuration from the environment, populating defaults where unset.
func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:         getEnv("PORT", "8080"),
			Env:          getEnv("ENV", "development"),
			ReadTimeout:  getDurationEnv("SERVER_READ_TIMEOUT", 10*time.Second),
			WriteTimeout: getDurationEnv("SERVER_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:  getDurationEnv("SERVER_IDLE_TIMEOUT", 60*time.Second),
			ShutdownWait: getDurationEnv("SHUTDOWN_TIMEOUT", 10*time.Second),
		},
		Database: DatabaseConfig{
			Host:            getEnv("DB_HOST", "localhost"),
			Port:            getEnv("DB_PORT", "5432"),
			User:            getEnv("DB_USER", "postgres"),
			Password:        getEnv("DB_PASSWORD", "postgres"),
			Name:            getEnv("DB_NAME", "payment_db"),
			SSLMode:         getEnv("DB_SSLMODE", "disable"),
			MaxConns:        int32(getIntEnv("DB_MAX_CONNS", 25)),
			MinConns:        int32(getIntEnv("DB_MIN_CONNS", 5)),
			MaxConnLifetime: getDurationEnv("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: getDurationEnv("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
		},
		Idempotency: IdempotencyConfig{
			KeyTTL:          getDurationEnv("IDEMPOTENCY_KEY_TTL", 24*time.Hour),
			RecoveryTimeout: getDurationEnv("IDEMPOTENCY_RECOVERY_TIMEOUT", 30*time.Second),
		},
		Processor: ProcessorConfig{
			DefaultMode: strings.ToLower(getEnv("PROCESSOR_DEFAULT_MODE", "success")),
			Timeout:     getDurationEnv("PROCESSOR_TIMEOUT", 5*time.Second),
		},
		Log: LogConfig{
			Level: strings.ToUpper(getEnv("LOG_LEVEL", "INFO")),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// Validate ensures critical settings satisfy operational constraints.
func (c *Config) Validate() error {
	if c.Server.Port == "" {
		return fmt.Errorf("PORT cannot be empty")
	}
	if c.Database.Host == "" {
		return fmt.Errorf("DB_HOST cannot be empty")
	}
	if c.Database.Name == "" {
		return fmt.Errorf("DB_NAME cannot be empty")
	}
	if c.Database.MaxConns <= 0 {
		return fmt.Errorf("DB_MAX_CONNS must be greater than 0")
	}
	if c.Idempotency.KeyTTL <= 0 {
		return fmt.Errorf("IDEMPOTENCY_KEY_TTL must be positive")
	}
	if c.Idempotency.RecoveryTimeout <= 0 {
		return fmt.Errorf("IDEMPOTENCY_RECOVERY_TIMEOUT must be positive")
	}
	switch c.Processor.DefaultMode {
	case "success", "failure", "timeout":
	default:
		return fmt.Errorf("unsupported PROCESSOR_DEFAULT_MODE: %q (expected success, failure, or timeout)", c.Processor.DefaultMode)
	}
	return nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getIntEnv(key string, defaultVal int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	dur, err := time.ParseDuration(valStr)
	if err != nil {
		return defaultVal
	}
	return dur
}
