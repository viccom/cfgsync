// Authentication middlewares.
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

type ctxKey int

const (
	userIDKey ctxKey = iota
	isAdminKey
	appTokenKey
)

// AppTokenCtx carries the (user_id, app_id) extracted from an app token.
type AppTokenCtx struct {
	UserID string
	AppID  string
}

// UserID returns the authenticated user ID from the request context.
// Empty if the request did not pass through UserMW.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// IsAdmin reports whether the authenticated user is an admin.
func IsAdmin(ctx context.Context) bool {
	v, _ := ctx.Value(isAdminKey).(bool)
	return v
}

// AppToken returns the app token context (user_id + app_id) or nil.
func AppToken(ctx context.Context) *AppTokenCtx {
	v, _ := ctx.Value(appTokenKey).(*AppTokenCtx)
	return v
}

// UserMW verifies a Bearer JWT and stores uid + is_admin in context.
func UserMW(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		tokenStr := strings.TrimPrefix(h, "Bearer ")
		claims, err := ParseAccess(secret, tokenStr)
		if err != nil {
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
		ctx = context.WithValue(ctx, isAdminKey, claims.IsAdmin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminMW requires that the request came through UserMW and the user is an admin.
func AdminMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAdmin(r.Context()) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AppTokenMW verifies an opaque Bearer app token, looks up the corresponding row,
// verifies it matches the {app_id} path parameter, and stores (uid, app_id) in context.
// Also updates last_used_at (best-effort, errors are ignored).
func AppTokenMW(d *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		tokenStr := strings.TrimPrefix(h, "Bearer ")

		sum := sha256.Sum256([]byte(tokenStr))
		tokenHash := hex.EncodeToString(sum[:])

		pathAppID := r.PathValue("app_id")

		var (
			uid   string
			appID string
		)
		err := d.QueryRowContext(r.Context(),
			`SELECT user_id, app_id FROM app_tokens WHERE token_hash = ?`,
			tokenHash,
		).Scan(&uid, &appID)
		if err == sql.ErrNoRows {
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		if pathAppID != "" && appID != pathAppID {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}

		_, _ = d.ExecContext(r.Context(),
			`UPDATE app_tokens SET last_used_at = ? WHERE token_hash = ?`,
			time.Now().Unix(), tokenHash,
		)

		ctx := context.WithValue(r.Context(), appTokenKey, &AppTokenCtx{UserID: uid, AppID: appID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
