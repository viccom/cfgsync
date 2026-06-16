package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/model"
)

// appIDRegex enforces reverse-domain style: two or more dot-separated segments,
// each starting with [a-z0-9] and containing [a-z0-9-]. Examples:
//   com.1remote.desktop
//   io.github.someuser.my-tool
//   local.dev.proj
var appIDRegex = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{1,30}\.)+[a-z0-9][a-z0-9-]{1,30}$`)

// AdminCreateApp registers a new app_id. Requires admin (enforced by middleware chain).
func AdminCreateApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminUID := auth.UserID(r.Context())
		var req model.CreateAppRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		if !appIDRegex.MatchString(req.AppID) || len(req.AppID) > 64 {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		if req.DisplayName == "" {
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
