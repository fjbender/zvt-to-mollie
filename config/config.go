package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// MollieAPIKey is the Mollie API key (required). Env: MOLLIE_API_KEY.
	MollieAPIKey string
	// MollieTerminalID is the Mollie terminal ID for point-of-sale payments (required). Env: MOLLIE_TERMINAL_ID.
	MollieTerminalID string
	// ZVTPassword is the 6-digit ZVT terminal password (required). Env: ZVT_PASSWORD.
	ZVTPassword string
	// ZVTListenAddr is the TCP address for the ZVT listener. Env: ZVT_LISTEN_ADDR. Default: ":20007".
	ZVTListenAddr string
	// ZVTTerminalID is the terminal ID reported in receipts. Env: ZVT_TERMINAL_ID.
	ZVTTerminalID string
	// ZVTCurrencyCode is the ISO 4217 numeric currency code. Env: ZVT_CURRENCY_CODE. Default: "978" (EUR).
	ZVTCurrencyCode string
	// ZVTTLSCert is the path to the TLS certificate file. Env: ZVT_TLS_CERT. Optional.
	ZVTTLSCert string
	// ZVTTLSKey is the path to the TLS key file. Env: ZVT_TLS_KEY. Optional.
	ZVTTLSKey string
	// MollieAPITimeout is the HTTP timeout for Mollie API requests. Env: MOLLIE_API_TIMEOUT. Default: 30s.
	MollieAPITimeout time.Duration
	// StateDBPath is the path to the bbolt state database file. Env: STATE_DB_PATH. Default: "state.db".
	StateDBPath string
	// HTTPListenAddr is the TCP address for the health/readiness HTTP server. Env: HTTP_LISTEN_ADDR. Default: ":8080".
	HTTPListenAddr string
}

// Load reads configuration from environment variables and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{
		MollieAPIKey:     os.Getenv("MOLLIE_API_KEY"),
		MollieTerminalID: os.Getenv("MOLLIE_TERMINAL_ID"),
		ZVTPassword:      os.Getenv("ZVT_PASSWORD"),
		ZVTListenAddr:    envOr("ZVT_LISTEN_ADDR", ":20007"),
		ZVTTerminalID:    os.Getenv("ZVT_TERMINAL_ID"),
		ZVTCurrencyCode:  envOr("ZVT_CURRENCY_CODE", "978"),
		ZVTTLSCert:       os.Getenv("ZVT_TLS_CERT"),
		ZVTTLSKey:        os.Getenv("ZVT_TLS_KEY"),
		MollieAPITimeout: parseDuration("MOLLIE_API_TIMEOUT", 30*time.Second),
		StateDBPath:      envOr("STATE_DB_PATH", "state.db"),
		HTTPListenAddr:   envOr("HTTP_LISTEN_ADDR", ":8080"),
	}

	var errs []error
	if cfg.MollieAPIKey == "" {
		errs = append(errs, errors.New("MOLLIE_API_KEY is required"))
	}
	if cfg.MollieTerminalID == "" {
		errs = append(errs, errors.New("MOLLIE_TERMINAL_ID is required"))
	}
	if cfg.ZVTPassword == "" {
		errs = append(errs, errors.New("ZVT_PASSWORD is required"))
	}
	return cfg, errors.Join(errs...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	// Accept plain seconds (e.g. "60") or Go duration strings (e.g. "60s").
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return fallback
}
