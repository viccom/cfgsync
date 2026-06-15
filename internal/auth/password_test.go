package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("hunter2-correct-horse")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword(hash, "hunter2-correct-horse"); err != nil {
		t.Errorf("verify correct: %v", err)
	}
	if err := VerifyPassword(hash, "wrong"); err == nil {
		t.Errorf("verify wrong should fail")
	}
}
