package approval

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndValidateToken(t *testing.T) {
	tm := NewTokenManager([]byte("test-secret-key"))

	namespace := "default"
	recName := "test-resource"
	owner := "test-owner"
	ttl := 1 * time.Hour

	token, err := tm.GenerateToken(namespace, recName, owner, ttl)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := tm.ValidateToken(token)
	require.NoError(t, err)
	require.NotNil(t, claims)

	assert.Equal(t, namespace, claims.Namespace)
	assert.Equal(t, recName, claims.RecName)
	assert.Equal(t, owner, claims.Owner)
	assert.Equal(t, owner, claims.Subject)
	assert.Equal(t, "ovro", claims.Issuer)
	assert.NotNil(t, claims.IssuedAt)
	assert.NotNil(t, claims.ExpiresAt)
}

func TestExpiredToken(t *testing.T) {
	tm := NewTokenManager([]byte("test-secret-key"))

	// Generate token with negative TTL (already expired)
	token, err := tm.GenerateToken("default", "test-resource", "test-owner", -1*time.Hour)
	require.NoError(t, err)

	// Validation should fail due to expiration
	_, err = tm.ValidateToken(token)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestInvalidSignature(t *testing.T) {
	tm1 := NewTokenManager([]byte("secret-key-1"))
	tm2 := NewTokenManager([]byte("secret-key-2"))

	// Generate token with first manager
	token, err := tm1.GenerateToken("default", "test-resource", "test-owner", 1*time.Hour)
	require.NoError(t, err)

	// Validate with second manager (wrong key)
	_, err = tm2.ValidateToken(token)
	assert.Error(t, err)
}

func TestTamperedToken(t *testing.T) {
	tm := NewTokenManager([]byte("test-secret-key"))

	token, err := tm.GenerateToken("default", "test-resource", "test-owner", 1*time.Hour)
	require.NoError(t, err)

	// Tamper with the token by modifying a character in the middle
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	// Modify the payload
	if len(parts[1]) > 0 {
		runes := []rune(parts[1])
		if runes[0] == 'a' {
			runes[0] = 'b'
		} else {
			runes[0] = 'a'
		}
		parts[1] = string(runes)
	}
	tamperedToken := strings.Join(parts, ".")

	// Validation should fail
	_, err = tm.ValidateToken(tamperedToken)
	assert.Error(t, err)
}

func TestValidTokenRoundTrip(t *testing.T) {
	tm := NewTokenManager([]byte("test-secret-key"))

	expectedNamespace := "test-namespace"
	expectedRecName := "test-recommendation"
	expectedOwner := "test-user"
	ttl := 2 * time.Hour

	// Generate token
	token, err := tm.GenerateToken(expectedNamespace, expectedRecName, expectedOwner, ttl)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	// Validate token
	claims, err := tm.ValidateToken(token)
	require.NoError(t, err)
	require.NotNil(t, claims)

	// Verify all fields
	assert.Equal(t, expectedNamespace, claims.Namespace)
	assert.Equal(t, expectedRecName, claims.RecName)
	assert.Equal(t, expectedOwner, claims.Owner)
	assert.Equal(t, expectedOwner, claims.Subject)
	assert.Equal(t, "ovro", claims.Issuer)

	// Verify timing
	now := time.Now()
	assert.True(t, claims.IssuedAt.Time.Before(now))
	assert.True(t, claims.ExpiresAt.Time.After(now))

	// Verify TTL is approximately correct (within 1 second tolerance)
	expectedExpiry := claims.IssuedAt.Time.Add(ttl)
	assert.WithinDuration(t, expectedExpiry, claims.ExpiresAt.Time, 1*time.Second)
}
