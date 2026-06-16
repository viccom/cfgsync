package auth

import (
	"testing"
	"time"
)

func TestIssueAndParseAccess(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, err := IssueAccess(secret, "uid-1", "a@b.c", false, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := ParseAccess(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != "uid-1" || claims.Email != "a@b.c" {
		t.Errorf("claims mismatch: %+v", claims)
	}
	if claims.IsAdmin {
		t.Errorf("expected IsAdmin=false")
	}
}

func TestIssueAndParseAccess_Admin(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, err := IssueAccess(secret, "uid-1", "a@b.c", true, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := ParseAccess(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !claims.IsAdmin {
		t.Errorf("expected IsAdmin=true")
	}
}

func TestParseAccess_RejectsWrongSecret(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, _ := IssueAccess(secret, "uid-1", "a@b.c", false, time.Hour)
	if _, err := ParseAccess([]byte("different-secret-different-secret"), tok); err == nil {
		t.Errorf("expected parse error with wrong secret")
	}
}

func TestParseAccess_RejectsExpired(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, _ := IssueAccess(secret, "uid-1", "a@b.c", false, -time.Minute)
	if _, err := ParseAccess(secret, tok); err == nil {
		t.Errorf("expected parse error on expired token")
	}
}
