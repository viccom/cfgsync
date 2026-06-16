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
