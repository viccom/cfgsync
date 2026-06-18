package model

// AppReleaseSummary is the lightweight release info embedded in app detail
// responses. The full manifest is not embedded here — it lives on the
// release endpoint. This keeps list/detail responses small.
type AppReleaseSummary struct {
	Version         string   `json:"version"`
	CreatedAt       int64    `json:"created_at"`
	PackageSize     int64    `json:"package_size"`
	Platforms       []string `json:"platforms,omitempty"`
	DownloadURL     string   `json:"download_url,omitempty"`
	ReleaseNotesURL string   `json:"release_notes_url,omitempty"`
}
