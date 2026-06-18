package model

// App is a registered application namespace (admin-managed).
type App struct {
	AppID          string   `json:"app_id"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description"`
	Summary        string   `json:"summary,omitempty"`          // v3: card summary
	OwnerUserID    string   `json:"owner_user_id,omitempty"`    // v3: app owner (admin who published)
	Visibility     string   `json:"visibility,omitempty"`       // v3: public|unlisted|private
	IconPath       string   `json:"icon_path,omitempty"`        // v3: repo-relative icon path
	LatestVersion  string   `json:"latest_version,omitempty"`   // v3: cached latest release version
	Tags           []string `json:"tags,omitempty"`             // v3: multi-tag labels
	CreatedAt      int64    `json:"created_at"`
	CreatedBy      string   `json:"created_by"`                 // user_id of the admin who registered
	CreatedByEmail string   `json:"created_by_email,omitempty"` // only populated in admin views
	UpdatedAt      int64    `json:"updated_at,omitempty"`       // v3: bumped on any metadata or release change
}

// CreateAppRequest is the body of POST /api/v1/admin/apps.
type CreateAppRequest struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
}

// PatchAppRequest is the body of PATCH /api/v1/admin/apps/{app_id}.
// All fields are optional; only provided fields are updated.
type PatchAppRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
	Summary     *string `json:"summary,omitempty"`    // v3
	Visibility  *string `json:"visibility,omitempty"` // v3
}
