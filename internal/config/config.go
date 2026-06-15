// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds runtime configuration for the cloud server.
type Config struct {
	Listen     string        // LISTEN, default ":28972"
	DBPath     string        // DB_PATH, default "./data.db"
	JWTSecret  []byte        // JWT_SECRET, required, must be >= 32 bytes
	AccessTTL  time.Duration // ACCESS_TTL, default 1h
	RefreshTTL time.Duration // REFRESH_TTL, default 30d
}

// Load reads configuration from the environment.
// Returns an error if JWT_SECRET is missing or shorter than 32 bytes.
func Load() (*Config, error) {
	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be set and at least 32 bytes (use: openssl rand -hex 32)")
	}

	return &Config{
		Listen:     getEnv("LISTEN", ":28972"),
		DBPath:     getEnv("DB_PATH", "./data.db"),
		JWTSecret:  []byte(secret),
		AccessTTL:  getDuration("ACCESS_TTL", time.Hour),
		RefreshTTL: getDuration("REFRESH_TTL", 30*24*time.Hour),
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