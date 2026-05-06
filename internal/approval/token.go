package approval

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ApprovalClaims represents the claims in a JWT approval token
type ApprovalClaims struct {
	jwt.RegisteredClaims
	Namespace string `json:"ns"`
	RecName   string `json:"rec"`
	Owner     string `json:"owner"`
}

// TokenManager handles generation and validation of JWT approval tokens
type TokenManager struct {
	signingKey []byte
}

// NewTokenManager creates a new TokenManager with the given signing key
func NewTokenManager(signingKey []byte) *TokenManager {
	return &TokenManager{
		signingKey: signingKey,
	}
}

// GenerateToken creates a new JWT approval token for the given resource
func (tm *TokenManager) GenerateToken(namespace, recName, owner string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := ApprovalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   owner,
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "ovro",
		},
		Namespace: namespace,
		RecName:   recName,
		Owner:     owner,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(tm.signingKey)
}

// ValidateToken validates a JWT approval token and returns its claims
func (tm *TokenManager) ValidateToken(tokenString string) (*ApprovalClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &ApprovalClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method is HMAC
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return tm.signingKey, nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*ApprovalClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}
