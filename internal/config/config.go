// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds runtime configuration for the cloud server.
type Config struct {
	Listen     string
	DBPath     string
	JWTSecret  []byte
	AccessTTL  time.Duration
	RefreshTTL time.Duration

	BootstrapAdminEmail    string
	BootstrapAdminPassword string

	UserStorageLimit  int64 // bytes
	UserAppTokenLimit int
	HistoryPerApp     int
	MaxPayloadBytes   int
	AppTokenPrefix    string
}

// Load reads configuration from the environment. Before reading env vars, it
// pulls KEY=VALUE pairs from a dotenv file (default ./cfgsync.env, or the
// file pointed to by CFGSYNC_CONFIG) so the server runs out of the box by
// double-clicking the binary with a sibling cfgsync.env. Explicit env vars
// on the process override file values — see LoadDotEnv.
func Load() (*Config, error) {
	cfgFile := os.Getenv("CFGSYNC_CONFIG")
	if cfgFile == "" {
		cfgFile = "cfgsync.env"
	}
	if err := LoadDotEnv(cfgFile); err != nil {
		return nil, fmt.Errorf("load %s: %w", cfgFile, err)
	}

	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be set and at least 32 bytes (set it in %s or via env; first run auto-generates %s)", cfgFile, cfgFile)
	}

	return &Config{
		Listen:     getEnv("LISTEN", ":28972"),
		DBPath:     getEnv("DB_PATH", "./data.db"),
		JWTSecret:  []byte(secret),
		AccessTTL:  getDuration("ACCESS_TTL", time.Hour),
		RefreshTTL: getDuration("REFRESH_TTL", 30*24*time.Hour),

		BootstrapAdminEmail:    os.Getenv("BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),

		UserStorageLimit:  getInt64("USER_STORAGE_LIMIT_MB", 100) * 1024 * 1024,
		UserAppTokenLimit: int(getInt64("USER_APP_TOKEN_LIMIT", 100)),
		HistoryPerApp:     int(getInt64("HISTORY_PER_APP", 50)),
		MaxPayloadBytes:   int(getInt64("MAX_PAYLOAD_BYTES", 4*1024*1024)),
		AppTokenPrefix:    getEnv("APP_TOKEN_PREFIX", "1rc_"),
	}, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func getInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}
