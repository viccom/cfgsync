package manifest

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, yamlStr string) (Manifest, error) {
	t.Helper()
	m, _, err := ParseAndValidate([]byte(yamlStr))
	return m, err
}

func expectField(t *testing.T, err error, field, reasonContains string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
		return
	}
	for _, f := range ve.Fields {
		if f.Field == field && (reasonContains == "" || strings.Contains(f.Reason, reasonContains)) {
			return
		}
	}
	t.Errorf("expected field %q (reason containing %q) in %+v", field, reasonContains, ve.Fields)
}

const validFull = `
schema_version: 1
version: "1.2.3"
display_name: "My App"
description: "one-liner"
summary: "card text"
license: "MIT"
homepage: "https://example.com"
tags: ["cli", "ai"]
keywords: ["automation"]
author:
  name: "Jane"
  email: "jane@example.com"
  url: "https://example.com/~jane"
requires_os: ["linux", "windows"]
platforms:
  linux-amd64:
    path: bin/linux-amd64/myapp
  windows-amd64:
    path: bin/windows-amd64/myapp.exe
visibility: public
`

func TestParseAndValidate_Full(t *testing.T) {
	m, err := mustParse(t, validFull)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if m.DisplayName != "My App" {
		t.Errorf("DisplayName=%q", m.DisplayName)
	}
	if len(m.Platforms) != 2 {
		t.Errorf("expected 2 platforms, got %d", len(m.Platforms))
	}
	if !AllowedPlatforms["linux-amd64"] {
		t.Errorf("platform whitelist missing linux-amd64")
	}
}

const validMinimal = `
schema_version: 1
version: "0.1.0"
display_name: "Bare"
`

func TestParseAndValidate_Minimal(t *testing.T) {
	if _, err := mustParse(t, validMinimal); err != nil {
		t.Fatalf("minimal manifest must pass: %v", err)
	}
}

func TestParseAndValidate_BadSchemaVersion(t *testing.T) {
	yamlStr := strings.Replace(validMinimal, "schema_version: 1", "schema_version: 2", 1)
	_, err := mustParse(t, yamlStr)
	expectField(t, err, "schema_version", "")
}

func TestParseAndValidate_BadVersion(t *testing.T) {
	yamlStr := strings.Replace(validMinimal, `version: "0.1.0"`, `version: "not-a-version"`, 1)
	_, err := mustParse(t, yamlStr)
	expectField(t, err, "version", "")
}

func TestParseAndValidate_BuildMetadataRejected(t *testing.T) {
	yamlStr := strings.Replace(validMinimal, `version: "0.1.0"`, `version: "1.0.0+build42"`, 1)
	_, err := mustParse(t, yamlStr)
	expectField(t, err, "version", "")
}

func TestParseAndValidate_MissingDisplayName(t *testing.T) {
	yamlStr := `
schema_version: 1
version: "1.0.0"
`
	_, err := mustParse(t, yamlStr)
	expectField(t, err, "display_name", "required")
}

func TestParseAndValidate_HomepageScheme(t *testing.T) {
	cases := map[string]bool{
		"https://example.com":     true,
		"http://localhost:8080/x": true,
		"javascript:alert(1)":     false,
		"data:text/html,<script>": false,
		"file:///etc/passwd":      false,
		"example.com":             false, // missing scheme
		"ftp://example.com":       false,
		"mailto:foo@example.com":  false,
		"HTTPS://example.com":     true, // scheme is case-insensitive per RFC 3986
	}
	for raw, wantOK := range cases {
		t.Run(raw, func(t *testing.T) {
			yamlStr := "schema_version: 1\nversion: \"1.0.0\"\ndisplay_name: \"X\"\nhomepage: \"" + raw + "\"\n"
			_, err := mustParse(t, yamlStr)
			if wantOK && err != nil {
				t.Errorf("expected ok, got %v", err)
			}
			if !wantOK && err == nil {
				t.Errorf("expected error for homepage=%q", raw)
				return
			}
			if !wantOK && err != nil {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Errorf("expected ValidationError, got %T: %v", err, err)
					return
				}
				foundHomepage := false
				for _, f := range ve.Fields {
					if f.Field == "homepage" {
						foundHomepage = true
					}
				}
				if !foundHomepage {
					t.Errorf("expected homepage field error, got %+v", ve.Fields)
				}
			}
		})
	}
}

func TestParseAndValidate_OversizedFields(t *testing.T) {
	yamlStr := `
schema_version: 1
version: "1.0.0"
display_name: "` + strings.Repeat("x", 129) + `"
description: "` + strings.Repeat("y", 201) + `"
summary: "` + strings.Repeat("z", 201) + `"
license: "` + strings.Repeat("L", 65) + `"
homepage: "` + strings.Repeat("h", 513) + `"
`
	_, err := mustParse(t, yamlStr)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	wantFields := []string{"display_name", "description", "summary", "license", "homepage"}
	gotFields := make(map[string]bool, len(ve.Fields))
	for _, f := range ve.Fields {
		gotFields[f.Field] = true
	}
	for _, w := range wantFields {
		if !gotFields[w] {
			t.Errorf("expected error on field %q, got %+v", w, ve.Fields)
		}
	}
}

func TestParseAndValidate_Tags(t *testing.T) {
	cases := []struct {
		name    string
		tags    string
		wantErr bool
	}{
		{"empty list", `tags: []`, false},
		{"one good", `tags: ["cli"]`, false},
		{"eight (max)", `tags: ["a", "b", "c", "d", "e", "f", "g", "h"]`, false},
		{"nine (over)", `tags: ["a", "b", "c", "d", "e", "f", "g", "h", "i"]`, true},
		{"uppercase", `tags: ["Cli"]`, true},
		{"too long", `tags: ["` + strings.Repeat("a", 25) + `"]`, true},
		{"duplicate", `tags: ["cli", "cli"]`, true},
		{"dash first", `tags: ["-bad"]`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			yamlStr := "schema_version: 1\nversion: \"1.0.0\"\ndisplay_name: \"X\"\n" + c.tags + "\n"
			_, err := mustParse(t, yamlStr)
			if c.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

func TestParseAndValidate_RequiresOS(t *testing.T) {
	yamlStr := `
schema_version: 1
version: "1.0.0"
display_name: "X"
requires_os: ["linux", "bsd"]
`
	_, err := mustParse(t, yamlStr)
	expectField(t, err, "requires_os[1]", "bsd")
}

func TestParseAndValidate_Platforms_UnknownKey(t *testing.T) {
	yamlStr := `
schema_version: 1
version: "1.0.0"
display_name: "X"
platforms:
  freebsd-amd64:
    path: bin/freebsd/myapp
`
	_, err := mustParse(t, yamlStr)
	expectField(t, err, "platforms.freebsd-amd64", "")
}

func TestParseAndValidate_Platforms_UnsafePath(t *testing.T) {
	cases := map[string]string{
		"absolute":     "/etc/passwd",
		"dotdot":       "../escape",
		"backslash":    `bin\evil`,
		"drive_letter": `c:/windows/system32/evil.exe`,
		"empty":        "",
		"leading_dot":  "./myapp",
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			// Single-quoted YAML so backslashes and other chars survive verbatim.
			yamlStr := `
schema_version: 1
version: "1.0.0"
display_name: "X"
platforms:
  linux-amd64:
    path: '` + p + `'
`
			_, err := mustParse(t, yamlStr)
			expectField(t, err, "platforms.linux-amd64.path", "")
		})
	}
}

func TestParseAndValidate_Visibility(t *testing.T) {
	cases := map[string]bool{
		"":         true,
		"public":   true,
		"unlisted": true,
		"private":  true,
		"secret":   false,
	}
	for vis, wantOK := range cases {
		t.Run(vis, func(t *testing.T) {
			yamlStr := "schema_version: 1\nversion: \"1.0.0\"\ndisplay_name: \"X\"\nvisibility: \"" + vis + "\"\n"
			_, err := mustParse(t, yamlStr)
			if wantOK && err != nil {
				t.Errorf("expected ok, got %v", err)
			}
			if !wantOK && err == nil {
				t.Errorf("expected error for visibility=%q", vis)
			}
		})
	}
}

func TestParseAndValidate_MultipleErrorsAggregated(t *testing.T) {
	yamlStr := `
schema_version: 2
version: "not-semver"
display_name: ""
`
	_, err := mustParse(t, yamlStr)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	// At least three distinct field errors.
	if len(ve.Fields) < 3 {
		t.Errorf("expected ≥3 errors, got %d (%+v)", len(ve.Fields), ve.Fields)
	}
}

func TestParseAndValidate_YAMLSyntaxError(t *testing.T) {
	yamlStr := "schema_version: 1\nversion: 1.0.0\n  bad: indent\n"
	_, err := mustParse(t, yamlStr)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("expected ValidationError on YAML syntax error, got %T %v", err, err)
	}
}

func TestValidationError_ErrorString(t *testing.T) {
	e := &ValidationError{Fields: []FieldError{{Field: "x", Reason: "r"}}}
	if !strings.Contains(e.Error(), "manifest validation failed") {
		t.Errorf("unexpected Error() = %q", e.Error())
	}
}

func TestIsSafeRelPath(t *testing.T) {
	cases := map[string]bool{
		"bin/linux/myapp":   true,
		"bin/linux-a/myapp": true,
		"myapp":             true,
		"":                  false,
		"/etc/passwd":       false,
		"../escape":         false,
		"./myapp":           false,
		"a/../b":            false,
		"bin\\evil":         false,
		"c:/x":              false,
	}
	for p, want := range cases {
		if got := isSafeRelPath(p); got != want {
			t.Errorf("isSafeRelPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestParseAndValidate_ExtraFieldRejected covers L8: an "extra:" block in
// the YAML used to be declared as json.RawMessage, but yaml.v3 cannot
// round-trip arbitrary YAML into RawMessage (errors on scalars, produces
// invalid JSON on lists). The field was removed until a yaml.Node-based
// implementation is needed. Until then, an extra: block must NOT silently
// pass — yaml.v3's strict mode would catch unknown fields, but our default
// is lenient, so we expect the block to be ignored rather than fail. The
// test pins the current behavior so a future change is intentional.
func TestParseAndValidate_ExtraFieldRejected(t *testing.T) {
	cases := []struct {
		name  string
		extra string
	}{
		{"scalar_string", `extra: "hello"`},
		{"map", `extra:
  k1: v1`},
		{"list", `extra: [1, 2, 3]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			yamlStr := "schema_version: 1\nversion: \"1.0.0\"\ndisplay_name: \"X\"\n" + c.extra + "\n"
			// yaml.v3 default is lenient — extra is silently ignored
			// because the field does not exist on the struct.
			if _, err := mustParse(t, yamlStr); err != nil {
				t.Errorf("expected extra block to be silently ignored, got error: %v", err)
			}
		})
	}
}
