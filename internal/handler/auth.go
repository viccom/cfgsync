package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/model"
)

// Register creates a new user and returns tokens.
func Register(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		req.Email = strings.TrimSpace(strings.ToLower(req.Email))
		if !validEmail(req.Email) || len(req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "invalid email or password too short (>=8)")
			return
		}

		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hash failed")
			return
		}
		uid := newID()
		now := time.Now().Unix()
		_, err = db.ExecContext(r.Context(),
			`INSERT INTO users (id, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			uid, req.Email, hash, now, now)
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "email already registered")
				return
			}
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		issueAndRespond(w, db, r, cfg, uid, req.Email)
	}
}

// Login validates credentials and returns tokens.
func Login(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		email := strings.TrimSpace(strings.ToLower(req.Email))
		if !validEmail(email) || req.Password == "" {
			writeError(w, http.StatusBadRequest, "invalid email or password")
			return
		}
		var (
			uid  string
			hash string
		)
		err := db.QueryRowContext(r.Context(),
			`SELECT id, password_hash FROM users WHERE email = ?`, email,
		).Scan(&uid, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		if err := auth.VerifyPassword(hash, req.Password); err != nil {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		issueAndRespond(w, db, r, cfg, uid, email)
	}
}

// Refresh exchanges a valid refresh token for a new access + refresh pair.
func Refresh(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		var (
			uid       string
			email     string
			expiresAt int64
			revoked   *int64
		)
		err := db.QueryRowContext(r.Context(),
			`SELECT u.id, u.email, rt.expires_at, rt.revoked_at
			   FROM refresh_tokens rt JOIN users u ON u.id = rt.user_id
			  WHERE rt.id = ?`, req.RefreshToken,
		).Scan(&uid, &email, &expiresAt, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid refresh token")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		if revoked != nil || expiresAt < time.Now().Unix() {
			writeError(w, http.StatusUnauthorized, "refresh expired or revoked")
			return
		}
		issueAndRespond(w, db, r, cfg, uid, email)
	}
}

// Logout revokes the current refresh token.
func Logout(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			// If no body, treat as best-effort success.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, err := db.ExecContext(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = ? WHERE id = ?`,
			time.Now().Unix(), req.RefreshToken)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- helpers ---

func issueAndRespond(w http.ResponseWriter, db *sql.DB, r *http.Request, cfg *config.Config, uid, email string) {
	access, err := auth.IssueAccess(cfg.JWTSecret, uid, email, cfg.AccessTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue access failed")
		return
	}
	refresh := newID()
	now := time.Now().Unix()
	_, err = db.ExecContext(r.Context(),
		`INSERT INTO refresh_tokens (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		refresh, uid, now+int64(cfg.RefreshTTL.Seconds()), now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue refresh failed")
		return
	}
	writeJSON(w, http.StatusOK, model.AuthResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(cfg.AccessTTL.Seconds()),
	})
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
