// Bearer-token middleware.
package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey int

const userIDKey ctxKey = 0

// UserID returns the authenticated user ID from the request context.
// Panics if called on a request that did not pass through Middleware.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// Middleware verifies the Bearer token and stores the user ID in the context.
// On failure, it writes a 401 JSON response and does not call the next handler.
func Middleware(secret []byte, next http.Handler) http.Handler {
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
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
