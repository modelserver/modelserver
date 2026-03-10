package auth

import (
	"testing"
	"time"
)

func TestJWT_GenerateAndValidate(t *testing.T) {
	j := NewJWTManager("test-secret-at-least-32-characters-long", 15*time.Minute, 168*time.Hour)

	access, refresh, err := j.GenerateTokenPair("user-123", "user@example.com", true)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	claims, err := j.ValidateToken(access)
	if err != nil {
		t.Fatalf("validate access failed: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("UserID = %q, want user-123", claims.UserID)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("Email = %q", claims.Email)
	}
	if claims.TokenType != "access" {
		t.Errorf("TokenType = %q, want access", claims.TokenType)
	}
	if !claims.IsSuperadmin {
		t.Error("expected IsSuperadmin to be true")
	}

	refreshClaims, err := j.ValidateToken(refresh)
	if err != nil {
		t.Fatalf("validate refresh failed: %v", err)
	}
	if refreshClaims.TokenType != "refresh" {
		t.Errorf("TokenType = %q, want refresh", refreshClaims.TokenType)
	}
}

func TestJWT_ExpiredToken(t *testing.T) {
	j := NewJWTManager("test-secret-at-least-32-characters-long", -1*time.Second, 168*time.Hour)

	access, _, err := j.GenerateTokenPair("user-123", "user@example.com", false)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	_, err = j.ValidateToken(access)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestJWT_InvalidSecret(t *testing.T) {
	j1 := NewJWTManager("secret-one-that-is-long-enough-32", 15*time.Minute, 168*time.Hour)
	j2 := NewJWTManager("secret-two-that-is-long-enough-32", 15*time.Minute, 168*time.Hour)

	access, _, _ := j1.GenerateTokenPair("user-123", "user@example.com", false)
	_, err := j2.ValidateToken(access)
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}
