package model

import "encoding/json"

// Manifest is the parsed manifest.yaml from an uploaded package.
// YAML and JSON tags coexist so the same struct drives both decode (pkg YAML)
// and API JSON responses.
type Manifest struct {
	SchemaVersion int                       `yaml:"schema_version" json:"schema_version"`
	Version       string                    `yaml:"version" json:"version"`
	DisplayName   string                    `yaml:"display_name" json:"display_name"`
	Description   string                    `yaml:"description" json:"description,omitempty"`
	Summary       string                    `yaml:"summary" json:"summary,omitempty"`
	License       string                    `yaml:"license" json:"license,omitempty"`
	Homepage      string                    `yaml:"homepage" json:"homepage,omitempty"`
	Tags          []string                  `yaml:"tags" json:"tags,omitempty"`
	Keywords      []string                  `yaml:"keywords" json:"keywords,omitempty"`
	Author        *ManifestAuthor           `yaml:"author" json:"author,omitempty"`
	RequiresOS    []string                  `yaml:"requires_os" json:"requires_os,omitempty"`
	Platforms     map[string]ManifestPlatform `yaml:"platforms" json:"platforms,omitempty"`
	Visibility    string                    `yaml:"visibility" json:"visibility,omitempty"`
	Extra         json.RawMessage           `yaml:"extra" json:"extra,omitempty"`
}

// ManifestAuthor is the optional author block inside manifest.yaml.
type ManifestAuthor struct {
	Name  string `yaml:"name" json:"name,omitempty"`
	Email string `yaml:"email" json:"email,omitempty"`
	URL   string `yaml:"url" json:"url,omitempty"`
}

// ManifestPlatform describes one (os-arch) → binary mapping inside the package.
type ManifestPlatform struct {
	Path   string `yaml:"path" json:"path"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
}

// AppRelease is a published version of an app. Used in dev and admin views.
type AppRelease struct {
	ID            int64     `json:"id"`
	AppID         string    `json:"app_id"`
	Version       string    `json:"version"`
	VersionMajor  int       `json:"version_major,omitempty"`
	VersionMinor  int       `json:"version_minor,omitempty"`
	VersionPatch  int       `json:"version_patch,omitempty"`
	VersionPre    string    `json:"version_pre,omitempty"`
	Manifest      *Manifest `json:"manifest,omitempty"`
	PackageSize   int64     `json:"package_size"`
	PackageSHA256 string    `json:"package_sha256"`
	ReleaseNotes  string    `json:"release_notes,omitempty"`
	CreatedAt     int64     `json:"created_at"`
	CreatedBy     string    `json:"created_by"`
}

// AppReleaseSummary is the lightweight release info embedded in app detail
// responses (avoids embedding the full manifest on every list view).
type AppReleaseSummary struct {
	Version         string   `json:"version"`
	CreatedAt       int64    `json:"created_at"`
	PackageSize     int64    `json:"package_size"`
	Platforms       []string `json:"platforms,omitempty"`
	DownloadURL     string   `json:"download_url,omitempty"`
	ReleaseNotesURL string   `json:"release_notes_url,omitempty"`
}

// CreateReleaseResponse is returned by POST /api/v1/dev/apps/{app_id}/releases.
type CreateReleaseResponse struct {
	AppID         string    `json:"app_id"`
	Version       string    `json:"version"`
	PackageSize   int64     `json:"package_size"`
	PackageSHA256 string    `json:"package_sha256"`
	Manifest      *Manifest `json:"manifest"`
	Docs          []string  `json:"docs"`
	Assets        []string  `json:"assets"`
	CreatedAt     int64     `json:"created_at"`
}
