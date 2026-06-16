package model

// App is a registered application namespace (admin-managed).
type App struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	CreatedAt   int64  `json:"created_at"`
	CreatedBy   string `json:"created_by"`
}

// CreateAppRequest is the body of POST /api/v1/admin/apps.
type CreateAppRequest struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
}
