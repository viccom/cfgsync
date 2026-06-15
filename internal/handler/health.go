package handler

import (
	"database/sql"
	"net/http"
)

// Health returns 200 with {"status":"ok","db":"ok"} if the DB is reachable.
func Health(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(); err != nil {
			writeError(w, http.StatusServiceUnavailable, "db unreachable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "ok"})
	}
}
