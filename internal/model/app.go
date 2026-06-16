package model

// App is a registered application namespace (admin-managed).
type App struct {
	AppID          string `json:"app_id"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	CreatedAt      int64  `json:"created_at"`
	CreatedBy      string `json:"created_by"`                 // user_id of the admin who registered
	CreatedByEmail string `json:"created_by_email,omitempty"` // only populated in admin views
}

// CreateAppRequest is the body of POST /api/v1/admin/apps.
type CreateAppRequest struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
}

// PatchAppRequest is the body of PATCH /api/v1/admin/apps/{app_id}.
// Both fields are optional; only provided fields are updated.
type PatchAppRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
}
