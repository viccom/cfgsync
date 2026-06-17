package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadDotEnv_NoOpOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.env")
	if err := LoadDotEnv(missing); err != nil {
		t.Errorf("missing file should be no-op, got %v", err)
	}
}

func TestLoadDotEnv_ParsesKeyValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.env")
	body := strings.Join([]string{
		"# comment",
		"",
		"FOO=bar",
		"SPACED = value-with-spaces-around-eq",
		`QUOTED="double quoted value"`,
		`SINGLE='single quoted value'`,
		"BADLINE_WITHOUT_EQUALS",
		"EMPTY_KEY=",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Start clean so LoadDotEnv is the only setter.
	for _, k := range []string{"FOO", "SPACED", "QUOTED", "SINGLE", "EMPTY_KEY"} {
		os.Unsetenv(k)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}

	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("FOO = %q, want %q", got, "bar")
	}
	if got := os.Getenv("SPACED"); got != "value-with-spaces-around-eq" {
		t.Errorf("SPACED = %q, want %q", got, "value-with-spaces-around-eq")
	}
	if got := os.Getenv("QUOTED"); got != "double quoted value" {
		t.Errorf("QUOTED = %q, want %q", got, "double quoted value")
	}
	if got := os.Getenv("SINGLE"); got != "single quoted value" {
		t.Errorf("SINGLE = %q, want %q", got, "single quoted value")
	}
	if got := os.Getenv("EMPTY_KEY"); got != "" {
		t.Errorf("EMPTY_KEY = %q, want empty", got)
	}
}

func TestLoadDotEnv_DoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.env")
	if err := os.WriteFile(path, []byte("MY_KEY=from_file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Explicit env must win.
	os.Setenv("MY_KEY", "from_env")
	defer os.Unsetenv("MY_KEY")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("MY_KEY"); got != "from_env" {
		t.Errorf("MY_KEY = %q, want %q (env should win over file)", got, "from_env")
	}
}

func TestGenerateDefaultConfig_RandomSecretsAndReadableContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfgsync.env")

	pw, err := GenerateDefaultConfig(path)
	if err != nil {
		t.Fatalf("GenerateDefaultConfig: %v", err)
	}

	if len(pw) != 16 {
		t.Errorf("admin password length = %d, want 16", len(pw))
	}
	for _, c := range pw {
		if !strings.ContainsRune(passwordChars, c) {
			t.Errorf("admin password contains non-alnum char %q in %q", c, pw)
		}
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)

	// JWT_SECRET line must be a 64-char hex string.
	if !strings.Contains(s, "JWT_SECRET=") {
		t.Errorf("config missing JWT_SECRET line")
	}
	for _, line := range strings.Split(s, "\n") {
		rest, ok := strings.CutPrefix(line, "JWT_SECRET=")
		if !ok {
			continue
		}
		secret := strings.TrimSpace(rest)
		if len(secret) != 64 {
			t.Errorf("JWT_SECRET length = %d, want 64 (32 bytes hex)", len(secret))
		}
		for _, c := range secret {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Errorf("JWT_SECRET contains non-hex char %q in %q", c, secret)
			}
		}
	}

	// File must be 0600 on Unix; Windows has no Unix perm bits, skip.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
		}
	}

	// Two consecutive runs must produce DIFFERENT secrets (randomness).
	pw2, _ := GenerateDefaultConfig(path + ".2")
	if pw == pw2 {
		t.Errorf("two generated passwords are identical (%q); randomness is broken", pw)
	}
}
