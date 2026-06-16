package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/model"
)

// appIDRegex enforces reverse-domain style: two or more dot-separated segments,
// each starting with [a-z0-9] and containing [a-z0-9-]. Examples:
//   com.1remote.desktop
//   io.github.someuser.my-tool
//   local.dev.proj
var appIDRegex = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{1,30}\.)+[a-z0-9][a-z0-9-]{1,30}$`)

// Field length caps for app metadata. Defends against pathological inputs
// (e.g. 1 MB display_name) that would otherwise be persisted verbatim.
const (
	maxAppIDLen      = 64
	maxDisplayNameLen = 256
	maxDescriptionLen = 1024
)

// AdminCreateApp registers a new app_id. Requires admin (enforced by middleware chain).
func AdminCreateApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminUID := auth.UserID(r.Context())
		var req model.CreateAppRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		if !appIDRegex.MatchString(req.AppID) || len(req.AppID) > maxAppIDLen {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		if req.DisplayName == "" || len(req.DisplayName) > maxDisplayNameLen {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		if len(req.Description) > maxDescriptionLen {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}

		now := time.Now().Unix()
		_, err := db.ExecContext(r.Context(),
			`INSERT INTO apps (app_id, display_name, description, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			req.AppID, req.DisplayName, req.Description, now, adminUID)
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "app_id_exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		writeJSON(w, http.StatusOK, model.App{
			AppID:       req.AppID,
			DisplayName: req.DisplayName,
			Description: req.Description,
			CreatedAt:   now,
			CreatedBy:   adminUID,
		})
	}
}

// AdminListApps returns all registered apps with admin-only fields (including created_by user_id).
// Paginated via ?limit (max 100, default 20) and ?offset.
func AdminListApps(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 20
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}

		rows, err := db.QueryContext(r.Context(),
			`SELECT app_id, display_name, description, created_at, created_by
			   FROM apps
			  ORDER BY created_at DESC
			  LIMIT ? OFFSET ?`, limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer rows.Close()

		apps := make([]model.App, 0, limit)
		for rows.Next() {
			var a model.App
			if err := rows.Scan(&a.AppID, &a.DisplayName, &a.Description, &a.CreatedAt, &a.CreatedBy); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			apps = append(apps, a)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"apps":   apps,
			"limit":  limit,
			"offset": offset,
		})
	}
}

// AdminGetApp returns a single app's full record, including the email of the admin who created it.
func AdminGetApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		if appID == "" {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		var (
			a           model.App
			createdBy   string
		)
		err := db.QueryRowContext(r.Context(),
			`SELECT a.app_id, a.display_name, a.description, a.created_at, a.created_by, u.email
			   FROM apps a
			   LEFT JOIN users u ON u.id = a.created_by
			  WHERE a.app_id = ?`, appID,
		).Scan(&a.AppID, &a.DisplayName, &a.Description, &a.CreatedAt, &createdBy, &a.CreatedByEmail)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		a.CreatedBy = createdBy
		writeJSON(w, http.StatusOK, a)
	}
}

// AdminPatchApp partially updates an app's display_name and/or description.
// Omitted fields are left untouched. Empty-body or all-omitted is a 400.
func AdminPatchApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		if appID == "" {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		var req model.PatchAppRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		if req.DisplayName == nil && req.Description == nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		if req.DisplayName != nil && (*req.DisplayName == "" || len(*req.DisplayName) > maxDisplayNameLen) {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		if req.Description != nil && len(*req.Description) > maxDescriptionLen {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}

		// Build the UPDATE incrementally so omitted fields stay untouched.
		sets := make([]string, 0, 2)
		args := make([]interface{}, 0, 4)
		if req.DisplayName != nil {
			sets = append(sets, "display_name = ?")
			args = append(args, *req.DisplayName)
		}
		if req.Description != nil {
			sets = append(sets, "description = ?")
			args = append(args, *req.Description)
		}
		args = append(args, appID)

		// Ensure row exists; surface 404 cleanly rather than 0-rows-affected silence.
		var exists int
		if err := db.QueryRowContext(r.Context(),
			`SELECT 1 FROM apps WHERE app_id = ?`, appID,
		).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		q := "UPDATE apps SET " + strings.Join(sets, ", ") + " WHERE app_id = ?"
		if _, err := db.ExecContext(r.Context(), q, args...); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		// Return the updated row (re-read so all fields are fresh).
		var a model.App
		if err := db.QueryRowContext(r.Context(),
			`SELECT app_id, display_name, description, created_at, created_by FROM apps WHERE app_id = ?`,
			appID,
		).Scan(&a.AppID, &a.DisplayName, &a.Description, &a.CreatedAt, &a.CreatedBy); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, a)
	}
}

// AdminDeleteApp removes an app_id. The schema's ON DELETE CASCADE on
// configs/config_history/app_tokens (app_id REFERENCES apps(app_id)) wipes all
// per-user data for this app atomically — no manual tx needed.
func AdminDeleteApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		if appID == "" {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		res, err := db.ExecContext(r.Context(),
			`DELETE FROM apps WHERE app_id = ?`, appID)
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

// AdminPromoteUser grants is_admin=1 to another user. Idempotent: promoting an
// existing admin still succeeds (UPDATE matches the row, just sets the same value).
// Unknown user_id -> 404 via RowsAffected==0 on the UPDATE itself.
func AdminPromoteUser(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetUID := r.PathValue("user_id")
		if targetUID == "" {
			writeError(w, http.StatusBadRequest, "invalid_user_id")
			return
		}
		res, err := db.ExecContext(r.Context(),
			`UPDATE users SET is_admin = 1, updated_at = ? WHERE id = ?`,
			time.Now().Unix(), targetUID,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"user_id":  targetUID,
			"is_admin": true,
		})
	}
}
