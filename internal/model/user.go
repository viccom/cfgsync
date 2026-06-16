package model

// User is the server-side representation of a registered user.
type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	IsAdmin      bool   `json:"is_admin"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// AdminUserInfo is the public-safe user summary returned by admin listing endpoints.
// password_hash is never included.
type AdminUserInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt int64  `json:"created_at"`
}
