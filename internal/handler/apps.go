package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/viccom/cfgsync/internal/model"
)

// ListApps returns all registered apps with optional pagination (?limit=20&offset=0).
// Public-facing metadata only (no created_by email).
func ListApps(db *sql.DB) http.HandlerFunc {
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
			`SELECT app_id, display_name, description, created_at FROM apps
			  ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer rows.Close()

		apps := make([]model.App, 0, limit)
		for rows.Next() {
			var a model.App
			if err := rows.Scan(&a.AppID, &a.DisplayName, &a.Description, &a.CreatedAt); err != nil {
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

// GetApp returns a single app by app_id.
func GetApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		if appID == "" {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		var a model.App
		err := db.QueryRowContext(r.Context(),
			`SELECT app_id, display_name, description, created_at FROM apps WHERE app_id = ?`, appID,
		).Scan(&a.AppID, &a.DisplayName, &a.Description, &a.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, a)
	}
}
