// Package server wires routing and middleware.
package server

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/handler"
	"github.com/viccom/cfgsync/internal/repo"
	"github.com/viccom/cfgsync/internal/webui"
)

// New builds the top-level HTTP handler.
func New(cfg *config.Config, db *sql.DB, repo *repo.Repo) http.Handler {
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
	mux.Handle("GET /api/v1/admin/apps", adminChain(handler.AdminListApps(db)))
	mux.Handle("POST /api/v1/admin/apps", adminChain(handler.AdminCreateApp(db)))
	mux.Handle("GET /api/v1/admin/apps/{app_id}", adminChain(handler.AdminGetApp(db)))
	mux.Handle("PATCH /api/v1/admin/apps/{app_id}", adminChain(handler.AdminPatchApp(db)))
	mux.Handle("DELETE /api/v1/admin/apps/{app_id}", adminChain(handler.AdminDeleteApp(db)))
	mux.Handle("POST /api/v1/admin/users/{user_id}/promote", adminChain(handler.AdminPromoteUser(db)))
	mux.Handle("GET /api/v1/admin/users", adminChain(handler.AdminListUsers(db)))

	// Developer (UserMW + AdminMW) — release upload / management.
	// Single-developer mode per design decision 1: admin is the only publisher.
	mux.Handle("POST /api/v1/dev/apps/{app_id}/releases", adminChain(handler.UploadRelease(db, cfg, repo)))
	mux.Handle("PUT /api/v1/dev/apps/{app_id}/releases/{version}", adminChain(handler.OverwriteRelease(db, cfg, repo)))
	mux.Handle("GET /api/v1/dev/apps/{app_id}/releases", adminChain(handler.ListDevReleases(db)))
	mux.Handle("DELETE /api/v1/dev/apps/{app_id}/releases/{version}", adminChain(handler.DeleteRelease(db, repo)))

	// App token (AppTokenMW)
	mux.Handle("GET /api/v1/apps/{app_id}/config", auth.AppTokenMW(db, handler.GetConfig(db)))
	mux.Handle("PUT /api/v1/apps/{app_id}/config", auth.AppTokenMW(db, handler.PutConfig(db, cfg)))

	// Catalog (public, no middleware) — read-only app marketplace.
	// visibility=private apps never appear; unlisted apps only via direct URL.
	mux.Handle("GET /api/v1/catalog/apps", handler.ListCatalogApps(db))
	mux.Handle("GET /api/v1/catalog/apps/{app_id}", handler.GetCatalogApp(db))
	mux.Handle("GET /api/v1/catalog/apps/{app_id}/releases", handler.ListCatalogReleases(db))
	mux.Handle("GET /api/v1/catalog/apps/{app_id}/releases/{version}", handler.GetCatalogRelease(db))
	mux.Handle("GET /api/v1/catalog/apps/{app_id}/releases/{version}/docs/{name}", handler.GetCatalogDoc(db))
	mux.Handle("GET /api/v1/catalog/apps/{app_id}/releases/{version}/docs/{name}/rendered", handler.GetCatalogDocRendered(db))
	mux.Handle("GET /api/v1/catalog/apps/{app_id}/releases/{version}/assets/{name...}", handler.GetCatalogAsset(db, repo))
	mux.Handle("GET /api/v1/catalog/apps/{app_id}/releases/{version}/download", handler.DownloadCatalogRelease(db, repo))
	mux.Handle("GET /api/v1/catalog/tags", handler.ListCatalogTags(db))

	// Catch-all: serve the embedded SPA. /api/v1/* is matched by the more specific
	// routes above (Go 1.22's method-aware mux takes the explicit handler first).
	mux.Handle("/", webui.Handler())

	return chain(mux, logMW, recoverMW)
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

// chain wraps h with the given middlewares. chain(h, A, B) returns A(B(h)) —
// the first middleware is the OUTERMOST (runs first on the request path).
// Use this ordering when reasoning about panic recovery vs. logging:
// chain(mux, logMW, recoverMW) = logMW(recoverMW(mux)), so recover catches
// handler panics and writes 500 BEFORE logMW observes the status.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
