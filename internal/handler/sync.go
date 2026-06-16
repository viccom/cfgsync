package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/model"
)

// GetConfig returns the current config snapshot for the (user, app_id) in context.
// New pairs (no row yet) get {version: 0, payload: ""}.
func GetConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		at := auth.AppToken(r.Context())
		var c model.Config
		err := db.QueryRowContext(r.Context(),
			`SELECT version, payload, updated_at, updated_by FROM configs WHERE user_id = ? AND app_id = ?`,
			at.UserID, at.AppID,
		).Scan(&c.Version, &c.Payload, &c.UpdatedAt, &c.UpdatedBy)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, model.Config{Version: 0})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// PutConfig upserts the config with optimistic locking. ?force=true bypasses version check.
func PutConfig(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		at := auth.AppToken(r.Context())

		// Cap body read so a hostile client cannot OOM us with a huge body
		// (MaxPayloadBytes + 1KB slack for the JSON envelope).
		r.Body = http.MaxBytesReader(w, r.Body, int64(cfg.MaxPayloadBytes)+1024)

		var req model.PutConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
					"error":     "payload_too_large",
					"max_bytes": cfg.MaxPayloadBytes,
				})
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		if req.UpdatedBy == "" {
			writeError(w, http.StatusBadRequest, "missing_updated_by")
			return
		}
		if len(req.Payload) > cfg.MaxPayloadBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
				"error":     "payload_too_large",
				"max_bytes": cfg.MaxPayloadBytes,
			})
			return
		}

		force := r.URL.Query().Get("force") == "true"

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer tx.Rollback()

		var current int64
		row := tx.QueryRowContext(r.Context(),
			`SELECT version FROM configs WHERE user_id = ? AND app_id = ?`, at.UserID, at.AppID)

		now := time.Now().Unix()
		var newVer int64

		switch err := row.Scan(&current); {
		case errors.Is(err, sql.ErrNoRows):
			if req.Version != 0 && !force {
				writeConflict(w, tx, at.UserID, at.AppID)
				return
			}
			if _, err := tx.ExecContext(r.Context(),
				`INSERT INTO configs (user_id, app_id, version, payload, updated_at, updated_by) VALUES (?, ?, 1, ?, ?, ?)`,
				at.UserID, at.AppID, req.Payload, now, req.UpdatedBy); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			newVer = 1

		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal")
			return

		default:
			if req.Version != current && !force {
				writeConflict(w, tx, at.UserID, at.AppID)
				return
			}
			newVer = current + 1
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE configs SET version = ?, payload = ?, updated_at = ?, updated_by = ?
				 WHERE user_id = ? AND app_id = ?`,
				newVer, req.Payload, now, req.UpdatedBy, at.UserID, at.AppID); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
		}

		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO config_history (user_id, app_id, version, payload, updated_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			at.UserID, at.AppID, newVer, req.Payload, req.UpdatedBy, now); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		writeJSON(w, http.StatusOK, model.Config{
			Version:   newVer,
			Payload:   req.Payload,
			UpdatedAt: now,
			UpdatedBy: req.UpdatedBy,
		})
	}
}

func writeConflict(w http.ResponseWriter, tx *sql.Tx, uid, appID string) {
	var c model.Config
	err := tx.QueryRow(
		`SELECT version, payload, updated_at, updated_by FROM configs WHERE user_id = ? AND app_id = ?`,
		uid, appID,
	).Scan(&c.Version, &c.Payload, &c.UpdatedAt, &c.UpdatedBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":              "version_conflict",
		"current_version":    c.Version,
		"current_payload":    c.Payload,
		"current_updated_at": c.UpdatedAt,
		"current_updated_by": c.UpdatedBy,
	})
}
