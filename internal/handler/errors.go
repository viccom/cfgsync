// Package handler implements HTTP handlers.
package handler

import (
	"encoding/json"
	"log"
	"net/http"
)

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a standard error response.
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

// logReq logs request method, path, and status after the handler runs.
func logReq(r *http.Request, status int) {
	log.Printf("%s %s -> %d", r.Method, r.URL.Path, status)
}
