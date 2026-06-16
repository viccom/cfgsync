package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/model"
)

// Register creates a new user (non-admin by default) and returns tokens.
func Register(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		req.Email = strings.TrimSpace(strings.ToLower(req.Email))
		if !validEmail(req.Email) || len(req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "invalid_email_or_password")
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		uid := auth.NewID()
		now := time.Now().Unix()
		_, err = db.ExecContext(r.Context(),
			`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?)`,
			uid, req.Email, hash, now, now)
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "email_already_registered")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		issueAndRespond(w, db, r, cfg, uid, req.Email, false)
	}
}

// Login validates credentials and returns tokens.
func Login(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		email := strings.TrimSpace(strings.ToLower(req.Email))
		if !validEmail(email) || req.Password == "" {
			writeError(w, http.StatusBadRequest, "invalid_email_or_password")
			return
		}
		var (
			uid     string
			hash    string
			isAdmin bool
		)
		err := db.QueryRowContext(r.Context(),
			`SELECT id, password_hash, is_admin FROM users WHERE email = ?`, email,
		).Scan(&uid, &hash, &isAdmin)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if err := auth.VerifyPassword(hash, req.Password); err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		issueAndRespond(w, db, r, cfg, uid, email, isAdmin)
	}
}

// Refresh exchanges a valid refresh token for a new pair (and revokes the old one).
func Refresh(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		var (
			uid       string
			email     string
			isAdmin   bool
			expiresAt int64
			revoked   *int64
		)
		err := db.QueryRowContext(r.Context(),
			`SELECT u.id, u.email, u.is_admin, rt.expires_at, rt.revoked_at
			   FROM refresh_tokens rt JOIN users u ON u.id = rt.user_id
			  WHERE rt.id = ?`, req.RefreshToken,
		).Scan(&uid, &email, &isAdmin, &expiresAt, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if revoked != nil || expiresAt < time.Now().Unix() {
			writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
			return
		}
		_, _ = db.ExecContext(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = ? WHERE id = ?`,
			time.Now().Unix(), req.RefreshToken)
		issueAndRespond(w, db, r, cfg, uid, email, isAdmin)
	}
}

// Logout revokes the refresh token in the body. Requires user token (UserMW).
func Logout(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, err := db.ExecContext(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = ? WHERE id = ?`,
			time.Now().Unix(), req.RefreshToken)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- helpers ---

func issueAndRespond(w http.ResponseWriter, db *sql.DB, r *http.Request, cfg *config.Config, uid, email string, isAdmin bool) {
	access, err := auth.IssueAccess(cfg.JWTSecret, uid, email, isAdmin, cfg.AccessTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	refresh := auth.NewID()
	now := time.Now().Unix()
	_, err = db.ExecContext(r.Context(),
		`INSERT INTO refresh_tokens (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		refresh, uid, now+int64(cfg.RefreshTTL.Seconds()), now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, model.AuthResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(cfg.AccessTTL.Seconds()),
	})
}

func validEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.IndexByte(s[at+1:], '.') < 0 {
		return false
	}
	return true
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}
