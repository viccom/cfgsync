package model

// CreateAppTokenRequest is the body of POST /api/v1/me/apps/{app_id}/token.
type CreateAppTokenRequest struct {
	Label string `json:"label,omitempty"`
}

// CreateAppTokenResponse is returned when a user creates/replaces an app token.
// Token is the plaintext token; it is returned exactly once.
type CreateAppTokenResponse struct {
	Token     string `json:"token"`
	AppID     string `json:"app_id"`
	Label     string `json:"label"`
	CreatedAt int64  `json:"created_at"`
}

// AppTokenInfo is the list item returned by GET /api/v1/me/tokens.
// TokenHash and plaintext are NEVER included.
type AppTokenInfo struct {
	TokenPrefix string `json:"token_prefix"`
	AppID       string `json:"app_id"`
	Label       string `json:"label"`
	CreatedAt   int64  `json:"created_at"`
	LastUsedAt  int64  `json:"last_used_at,omitempty"`
}
