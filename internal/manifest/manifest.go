// Package manifest parses and validates manifest.yaml from uploaded app
// packages. Field-level rules live in spec §4.4 (docs/superpowers/specs/
// 2026-06-18-app-market-design.md).
package manifest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/viccom/cfgsync/internal/semver"
)

// Manifest is the parsed manifest.yaml structure. Mirrors model.Manifest
// shape; this package owns parsing + validation invariants, model owns
// JSON serialization for the API layer.
type Manifest struct {
	SchemaVersion int                         `yaml:"schema_version" json:"schema_version"`
	Version       string                      `yaml:"version" json:"version"`
	DisplayName   string                      `yaml:"display_name" json:"display_name"`
	Description   string                      `yaml:"description" json:"description,omitempty"`
	Summary       string                      `yaml:"summary" json:"summary,omitempty"`
	License       string                      `yaml:"license" json:"license,omitempty"`
	Homepage      string                      `yaml:"homepage" json:"homepage,omitempty"`
	Tags          []string                    `yaml:"tags" json:"tags,omitempty"`
	Keywords      []string                    `yaml:"keywords" json:"keywords,omitempty"`
	Author        *Author                     `yaml:"author" json:"author,omitempty"`
	RequiresOS    []string                    `yaml:"requires_os" json:"requires_os,omitempty"`
	Platforms     map[string]Platform         `yaml:"platforms" json:"platforms,omitempty"`
	Visibility    string                      `yaml:"visibility" json:"visibility,omitempty"`
	Extra         json.RawMessage             `yaml:"extra" json:"extra,omitempty"`
}

// Author is the optional author block.
type Author struct {
	Name  string `yaml:"name" json:"name,omitempty"`
	Email string `yaml:"email" json:"email,omitempty"`
	URL   string `yaml:"url" json:"url,omitempty"`
}

// Platform describes one (os-arch) → binary mapping inside the package.
type Platform struct {
	Path   string `yaml:"path" json:"path"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
}

// FieldError reports one invalid field with a path-style name (e.g.
// "platforms.linux-amd64.path") and a human-readable reason.
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// ValidationError contains one or more field-level errors. The HTTP layer
// serializes Fields into the 400 invalid_manifest response body so callers
// can fix every issue in one round-trip.
type ValidationError struct {
	Fields []FieldError `json:"fields"`
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("manifest validation failed:")
	for _, f := range e.Fields {
		fmt.Fprintf(&b, " [%s] %s;", f.Field, f.Reason)
	}
	return b.String()
}

// Field-level caps enforced at parse time. spec §4.4.
const (
	MaxDisplayName = 128
	MaxDescription = 200
	MaxSummary     = 200
	MaxLicense     = 64
	MaxHomepage    = 512
	MaxAuthorField = 128
	MaxTagLen      = 24
	MaxTags        = 8
	MaxKeywordLen  = 32
	MaxKeywords    = 16
)

// AllowedOS enumerates the only strings allowed in requires_os.
var AllowedOS = map[string]bool{
	"linux":   true,
	"windows": true,
	"darwin":  true,
	"any":     true,
}

// AllowedVisibility enumerates the only strings allowed in visibility.
var AllowedVisibility = map[string]bool{
	"public":   true,
	"unlisted": true,
	"private":  true,
}

// AllowedPlatforms enumerates the only (os-arch) keys allowed in platforms.
// Mirrors the PACKAGE_PLATFORM_WHITELIST env default in spec §8.
var AllowedPlatforms = map[string]bool{
	"linux-amd64":   true,
	"linux-arm64":   true,
	"windows-amd64": true,
	"windows-arm64": true,
	"darwin-amd64":  true,
	"darwin-arm64":  true,
}

// tagPattern enforces a lowercase kebab-case tag identifier (spec §4.4):
// first char [a-z0-9], rest [a-z0-9-], total length 1..24.
var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// ParseAndValidate decodes YAML bytes and runs every spec §4.4 rule.
// Returns the parsed manifest, the semver-decomposed version (zero on error),
// and an error implementing *ValidationError when any field fails.
func ParseAndValidate(raw []byte) (Manifest, semver.Parsed, error) {
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Manifest{}, semver.Parsed{}, &ValidationError{
			Fields: []FieldError{{Field: "$", Reason: "yaml decode: " + err.Error()}},
		}
	}
	var vErr ValidationError

	if m.SchemaVersion != 1 {
		vErr.Fields = append(vErr.Fields, FieldError{
			Field:  "schema_version",
			Reason: fmt.Sprintf("must be 1, got %d", m.SchemaVersion),
		})
	}

	ver, err := semver.Parse(m.Version)
	if err != nil {
		vErr.Fields = append(vErr.Fields, FieldError{
			Field: "version", Reason: err.Error(),
		})
	}

	// display_name: required, bounded.
	switch {
	case m.DisplayName == "":
		vErr.Fields = append(vErr.Fields, FieldError{Field: "display_name", Reason: "required"})
	case len(m.DisplayName) > MaxDisplayName:
		vErr.Fields = append(vErr.Fields, FieldError{Field: "display_name", Reason: "too long"})
	}

	if len(m.Description) > MaxDescription {
		vErr.Fields = append(vErr.Fields, FieldError{Field: "description", Reason: "too long"})
	}
	if len(m.Summary) > MaxSummary {
		vErr.Fields = append(vErr.Fields, FieldError{Field: "summary", Reason: "too long"})
	}
	if len(m.License) > MaxLicense {
		vErr.Fields = append(vErr.Fields, FieldError{Field: "license", Reason: "too long"})
	}
	if m.Homepage != "" {
		if len(m.Homepage) > MaxHomepage {
			vErr.Fields = append(vErr.Fields, FieldError{Field: "homepage", Reason: "too long"})
		} else if _, err := url.Parse(m.Homepage); err != nil {
			vErr.Fields = append(vErr.Fields, FieldError{Field: "homepage", Reason: "invalid URL"})
		}
	}

	validateTags(&m, &vErr)
	validateKeywords(&m, &vErr)
	validateRequiresOS(&m, &vErr)
	validatePlatforms(&m, &vErr)

	if m.Visibility != "" && !AllowedVisibility[m.Visibility] {
		vErr.Fields = append(vErr.Fields, FieldError{
			Field:  "visibility",
			Reason: fmt.Sprintf("%q not in whitelist", m.Visibility),
		})
	}

	if m.Author != nil {
		if len(m.Author.Name) > MaxAuthorField ||
			len(m.Author.Email) > MaxAuthorField ||
			len(m.Author.URL) > MaxAuthorField {
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  "author",
				Reason: fmt.Sprintf("each field must be ≤ %d chars", MaxAuthorField),
			})
		}
	}

	if len(vErr.Fields) > 0 {
		return m, ver, &vErr
	}
	return m, ver, nil
}

func validateTags(m *Manifest, vErr *ValidationError) {
	if len(m.Tags) > MaxTags {
		vErr.Fields = append(vErr.Fields, FieldError{
			Field:  "tags",
			Reason: fmt.Sprintf("too many (max %d)", MaxTags),
		})
	}
	seen := make(map[string]bool, len(m.Tags))
	for i, t := range m.Tags {
		switch {
		case len(t) > MaxTagLen:
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  fmt.Sprintf("tags[%d]", i),
				Reason: fmt.Sprintf("too long (max %d)", MaxTagLen),
			})
		case !tagPattern.MatchString(t):
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  fmt.Sprintf("tags[%d]", i),
				Reason: "must match [a-z0-9][a-z0-9-]{0,23}",
			})
		case seen[t]:
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  fmt.Sprintf("tags[%d]", i),
				Reason: fmt.Sprintf("duplicate %q", t),
			})
		default:
			seen[t] = true
		}
	}
}

func validateKeywords(m *Manifest, vErr *ValidationError) {
	if len(m.Keywords) > MaxKeywords {
		vErr.Fields = append(vErr.Fields, FieldError{
			Field:  "keywords",
			Reason: fmt.Sprintf("too many (max %d)", MaxKeywords),
		})
	}
	for i, k := range m.Keywords {
		if len(k) == 0 || len(k) > MaxKeywordLen {
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  fmt.Sprintf("keywords[%d]", i),
				Reason: fmt.Sprintf("length must be 1..%d", MaxKeywordLen),
			})
		}
	}
}

func validateRequiresOS(m *Manifest, vErr *ValidationError) {
	for i, os := range m.RequiresOS {
		if !AllowedOS[os] {
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  fmt.Sprintf("requires_os[%d]", i),
				Reason: fmt.Sprintf("%q not in whitelist {linux,windows,darwin,any}", os),
			})
		}
	}
}

func validatePlatforms(m *Manifest, vErr *ValidationError) {
	for k, p := range m.Platforms {
		if !AllowedPlatforms[k] {
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  "platforms." + k,
				Reason: "platform key not in whitelist",
			})
			continue
		}
		if !isSafeRelPath(p.Path) {
			vErr.Fields = append(vErr.Fields, FieldError{
				Field:  "platforms." + k + ".path",
				Reason: "must be a safe relative path inside the package",
			})
		}
	}
}

// isSafeRelPath rejects absolute paths, ".." segments, backslashes, and
// Windows drive letters. The repo layer re-checks at FS access time; this
// guard catches obvious bad inputs at parse time so the error message is
// actionable in the upload response.
func isSafeRelPath(p string) bool {
	if p == "" {
		return false
	}
	if strings.ContainsAny(p, "\\\x00") {
		return false
	}
	if strings.HasPrefix(p, "/") {
		return false
	}
	if len(p) >= 2 && p[1] == ':' {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
