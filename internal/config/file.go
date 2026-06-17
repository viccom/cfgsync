package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// LoadDotEnv reads KEY=VALUE lines from path into the process environment.
// Missing file is a no-op (returns nil). Existing env vars are NOT overwritten
// — explicit env settings on the command line / shell take precedence over
// file values, so deployments that already use systemd EnvironmentFile or
// `JWT_SECRET=... ./cfgsync` keep working unchanged.
//
// Lines starting with '#' and blank lines are skipped. Values may be quoted
// with double or single quotes; quotes are stripped.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if n := len(val); n >= 2 {
			first, last := val[0], val[n-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : n-1]
			}
		}
		if _, ok := os.LookupEnv(key); !ok {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}

// GenerateDefaultConfig writes a starter cfgsync.env at path with a freshly
// generated 32-byte JWT_SECRET and a random 16-char bootstrap admin password.
// Other fields are commented-out defaults the user can uncomment to override.
//
// Returns the plaintext admin password so the caller (cmd/server) can log it
// once on first run. The password is NOT stored in hashed form in the file —
// cfgsync.env is a configuration artifact the user is expected to protect
// (0600 perms); the server itself stores only the bcrypt hash in the DB.
func GenerateDefaultConfig(path string) (adminPassword string, err error) {
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", fmt.Errorf("generate jwt secret: %w", err)
	}
	pw, err := randomPassword(16)
	if err != nil {
		return "", fmt.Errorf("generate admin password: %w", err)
	}

	body := strings.Join([]string{
		"# cfgsync configuration file.",
		"# Lines are KEY=VALUE. '#' starts a comment. Empty lines are ignored.",
		"# Edit this file then restart cfgsync.",
		"",
		"# JWT signing key (HS256, 32 bytes / 64 hex chars).",
		"# Regenerated only when this file is deleted. Rotating JWT_SECRET",
		"# invalidates all existing access tokens (access TTL is 1h by default).",
		"JWT_SECRET=" + hex.EncodeToString(secretBytes),
		"",
		"# HTTP listen address. Default :28972.",
		"LISTEN=:28972",
		"",
		"# SQLite file path. Default ./data.db next to the executable.",
		"# DB_PATH=./data.db",
		"",
		"# Bootstrap admin (created on first start if absent, ignored after).",
		"# The password below is random; the server logs it once at startup.",
		"# Change it by editing the users table directly or by deleting the DB",
		"# and restarting with a new value here.",
		"BOOTSTRAP_ADMIN_EMAIL=admin@example.com",
		"BOOTSTRAP_ADMIN_PASSWORD=" + pw,
		"",
		"# Quotas (defaults shown). Uncomment to override.",
		"# USER_STORAGE_LIMIT_MB=100",
		"# USER_APP_TOKEN_LIMIT=100",
		"# HISTORY_PER_APP=50",
		"# MAX_PAYLOAD_BYTES=4194304",
		"# APP_TOKEN_PREFIX=1rc_",
		"",
	}, "\n")

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}

const passwordChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomPassword(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range bytes {
		out[i] = passwordChars[int(b)%len(passwordChars)]
	}
	return string(out), nil
}
