package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims holds JWT token claims.
type Claims struct {
	UserID       string `json:"user_id"`
	Email        string `json:"email"`
	IsSuperadmin bool   `json:"is_superadmin"`
	TokenType    string `json:"token_type"` // "access" or "refresh"
	jwt.RegisteredClaims
}

// JWTManager handles JWT token generation and validation.
type JWTManager struct {
	secret          []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

// NewJWTManager creates a new JWT manager.
func NewJWTManager(secret string, accessTTL, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		secret:          []byte(secret),
		accessTokenTTL:  accessTTL,
		refreshTokenTTL: refreshTTL,
	}
}

// GenerateTokenPair creates an access and refresh token pair.
func (j *JWTManager) GenerateTokenPair(userID, email string, isSuperadmin bool) (access string, refresh string, err error) {
	now := time.Now()

	accessClaims := Claims{
		UserID:       userID,
		Email:        email,
		IsSuperadmin: isSuperadmin,
		TokenType:    "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(j.accessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID,
		},
	}
	access, err = jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(j.secret)
	if err != nil {
		return "", "", fmt.Errorf("sign access token: %w", err)
	}

	refreshClaims := Claims{
		UserID:       userID,
		Email:        email,
		IsSuperadmin: isSuperadmin,
		TokenType:    "refresh",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(j.refreshTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID,
		},
	}
	refresh, err = jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(j.secret)
	if err != nil {
		return "", "", fmt.Errorf("sign refresh token: %w", err)
	}

	return access, refresh, nil
}

// ValidateToken validates a JWT token and returns its claims.
func (j *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}
