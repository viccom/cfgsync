// Package server wires routing and middleware.
package server

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/handler"
)

// New builds the top-level HTTP handler.
func New(cfg *config.Config, db *sql.DB) http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /api/v1/health", handler.Health(db))

	// Credential (body auth, no middleware)
	mux.HandleFunc("POST /api/v1/auth/register", handler.Register(db, cfg))
	mux.HandleFunc("POST /api/v1/auth/login", handler.Login(db, cfg))
	mux.HandleFunc("POST /api/v1/auth/refresh", handler.Refresh(db, cfg))

	// User token (UserMW)
	mux.Handle("POST /api/v1/auth/logout", auth.UserMW(cfg.JWTSecret, handler.Logout(db)))
	mux.Handle("GET /api/v1/apps", auth.UserMW(cfg.JWTSecret, handler.ListApps(db)))
	mux.Handle("GET /api/v1/apps/{app_id}", auth.UserMW(cfg.JWTSecret, handler.GetApp(db)))
	mux.Handle("POST /api/v1/me/apps/{app_id}/token", auth.UserMW(cfg.JWTSecret, handler.CreateAppToken(db, cfg)))
	mux.Handle("GET /api/v1/me/tokens", auth.UserMW(cfg.JWTSecret, handler.ListMyTokens(db)))
	mux.Handle("DELETE /api/v1/me/tokens/{token_prefix}", auth.UserMW(cfg.JWTSecret, handler.DeleteAppToken(db)))
	mux.Handle("DELETE /api/v1/me/apps/{app_id}/data", auth.UserMW(cfg.JWTSecret, handler.DeleteAppData(db)))
	mux.Handle("GET /api/v1/me/quota", auth.UserMW(cfg.JWTSecret, handler.GetQuota(db, cfg)))

	// Admin (UserMW + AdminMW)
	adminChain := func(h http.Handler) http.Handler {
		return auth.UserMW(cfg.JWTSecret, auth.AdminMW(h))
	}
	mux.Handle("POST /api/v1/admin/apps", adminChain(handler.AdminCreateApp(db)))

	// App token (AppTokenMW)
	mux.Handle("GET /api/v1/apps/{app_id}/config", auth.AppTokenMW(db, handler.GetConfig(db)))
	mux.Handle("PUT /api/v1/apps/{app_id}/config", auth.AppTokenMW(db, handler.PutConfig(db, cfg)))

	return chain(mux, recoverMW, logMW)
}

// --- middleware ---

type wrappedWriter struct {
	http.ResponseWriter
	status int
}

func (w *wrappedWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func logMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &wrappedWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)
		log.Printf("%s %s %d %dus", r.Method, r.URL.Path, ww.status, time.Since(start).Microseconds())
	})
}

func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("PANIC %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
