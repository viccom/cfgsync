package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/model"
)

// GetConfig returns the current config snapshot for the authenticated user.
// New users (no row yet) get {version:0}.
func GetConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())
		var c model.Config
		err := db.QueryRowContext(r.Context(),
			`SELECT version, payload, updated_at, updated_by FROM configs WHERE user_id = ?`, uid,
		).Scan(&c.Version, &c.Payload, &c.UpdatedAt, &c.UpdatedBy)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, model.Config{Version: 0})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// PutConfig upserts the config snapshot with optimistic locking.
func PutConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())
		var req model.PutConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.UpdatedBy == "" {
			writeError(w, http.StatusBadRequest, "updated_by required")
			return
		}

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		defer tx.Rollback()

		var current int64
		row := tx.QueryRowContext(r.Context(),
			`SELECT version FROM configs WHERE user_id = ?`, uid)

		now := time.Now().Unix()

		switch err := row.Scan(&current); {
		case errors.Is(err, sql.ErrNoRows):
			// New user: only accept version == 0.
			if req.Version != 0 {
				writeConflict(w, tx, uid)
				return
			}
			if _, err := tx.ExecContext(r.Context(),
				`INSERT INTO configs (user_id, version, payload, updated_at, updated_by) VALUES (?, 1, ?, ?, ?)`,
				uid, req.Payload, now, req.UpdatedBy); err != nil {
				writeError(w, http.StatusInternalServerError, "insert failed")
				return
			}
			current = 1

		case err != nil:
			writeError(w, http.StatusInternalServerError, "db error")
			return

		default:
			if req.Version != current {
				writeConflict(w, tx, uid)
				return
			}
			newVer := current + 1
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE configs SET version = ?, payload = ?, updated_at = ?, updated_by = ? WHERE user_id = ? AND version = ?`,
				newVer, req.Payload, now, req.UpdatedBy, uid, current); err != nil {
				writeError(w, http.StatusInternalServerError, "update failed")
				return
			}
			current = newVer
		}

		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO config_history (user_id, version, payload, created_at) VALUES (?, ?, ?, ?)`,
			uid, current, req.Payload, now); err != nil {
			writeError(w, http.StatusInternalServerError, "history insert failed")
			return
		}

		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "commit failed")
			return
		}

		writeJSON(w, http.StatusOK, model.Config{
			Version:   current,
			Payload:   req.Payload,
			UpdatedAt: now,
			UpdatedBy: req.UpdatedBy,
		})
	}
}

// writeConflict reads the current config and returns a 409 with current state.
func writeConflict(w http.ResponseWriter, tx *sql.Tx, uid string) {
	var c model.Config
	err := tx.QueryRow(
		`SELECT version, payload, updated_at, updated_by FROM configs WHERE user_id = ?`, uid,
	).Scan(&c.Version, &c.Payload, &c.UpdatedAt, &c.UpdatedBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(model.ConflictResponse{
		Error:            "version_conflict",
		CurrentVersion:   c.Version,
		CurrentPayload:   c.Payload,
		CurrentUpdatedAt: c.UpdatedAt,
		CurrentUpdatedBy: c.UpdatedBy,
	})
}
