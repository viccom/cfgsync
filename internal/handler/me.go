package handler

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/model"
)

// CreateAppToken issues a new app token for (user, app_id), replacing any existing one.
// The plaintext token is returned exactly once; only the SHA-256 hash is stored.
func CreateAppToken(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())
		appID := r.PathValue("app_id")

		var exists int
		if err := db.QueryRowContext(r.Context(),
			`SELECT 1 FROM apps WHERE app_id = ?`, appID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		var req model.CreateAppTokenRequest
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}

		plaintext := cfg.AppTokenPrefix + auth.NewID()
		sum := sha256.Sum256([]byte(plaintext))
		tokenHash := hex.EncodeToString(sum[:])
		tokenPrefix := plaintext
		if len(tokenPrefix) > 12 {
			tokenPrefix = tokenPrefix[:12]
		}

		now := time.Now().Unix()
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer tx.Rollback()

		if _, err := tx.ExecContext(r.Context(),
			`DELETE FROM app_tokens WHERE user_id = ? AND app_id = ?`, uid, appID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			tokenHash, tokenPrefix, uid, appID, req.Label, now); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		writeJSON(w, http.StatusOK, model.CreateAppTokenResponse{
			Token:     plaintext,
			AppID:     appID,
			Label:     req.Label,
			CreatedAt: now,
		})
	}
}

// ListMyTokens returns all app_tokens owned by the authenticated user.
// Token hashes and plaintext are NEVER included; only the public prefix.
func ListMyTokens(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())

		rows, err := db.QueryContext(r.Context(),
			`SELECT token_prefix, app_id, label, created_at, last_used_at
			   FROM app_tokens
			  WHERE user_id = ?
			  ORDER BY created_at DESC`, uid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer rows.Close()

		tokens := make([]model.AppTokenInfo, 0)
		for rows.Next() {
			var info model.AppTokenInfo
			var lastUsed sql.NullInt64
			if err := rows.Scan(&info.TokenPrefix, &info.AppID, &info.Label, &info.CreatedAt, &lastUsed); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			if lastUsed.Valid {
				info.LastUsedAt = lastUsed.Int64
			}
			tokens = append(tokens, info)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"tokens": tokens,
		})
	}
}

// DeleteAppToken revokes one of the user's app tokens, identified by token_prefix.
// prefix is the first 12 chars of the plaintext token (the only stable identifier
// a client can store without keeping the plaintext secret).
func DeleteAppToken(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())
		prefix := r.PathValue("token_prefix")
		if prefix == "" {
			writeError(w, http.StatusBadRequest, "invalid_token_prefix")
			return
		}

		res, err := db.ExecContext(r.Context(),
			`DELETE FROM app_tokens WHERE user_id = ? AND token_prefix = ?`,
			uid, prefix)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// DeleteAppData wipes all data the user has stored for a given app_id:
// configs row, all config_history rows, and the app_token for this (user, app_id).
// Idempotent — returns 204 whether or not anything existed. The user's other
// (user, app_id) pairs and other users' data are untouched.
func DeleteAppData(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())
		appID := r.PathValue("app_id")
		if appID == "" {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer tx.Rollback()

		for _, q := range []string{
			`DELETE FROM configs        WHERE user_id = ? AND app_id = ?`,
			`DELETE FROM config_history WHERE user_id = ? AND app_id = ?`,
			`DELETE FROM app_tokens     WHERE user_id = ? AND app_id = ?`,
		} {
			if _, err := tx.ExecContext(r.Context(), q, uid, appID); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
		}

		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
