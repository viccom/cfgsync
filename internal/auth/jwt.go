// JWT helpers (HS256).
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the JWT payload.
type Claims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// IssueAccess signs an access token for the given user with the given TTL.
func IssueAccess(secret []byte, uid, email string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: uid, Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "1remote-cloud",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(secret)
}

// ParseAccess verifies the token signature, algorithm, and expiry.
// Returns the parsed claims on success.
func ParseAccess(secret []byte, tokenStr string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
