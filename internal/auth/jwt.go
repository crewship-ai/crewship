package auth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/crypto/hkdf"
	"io"
)

var (
	ErrTokenExpired = errors.New("token expired")
	ErrInvalidToken = errors.New("invalid token")
)

type Claims struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
	Jti   string `json:"jti"`
}

type JWTValidator struct {
	encryptionKey []byte
}

// NewJWTValidator creates a validator that can decrypt NextAuth v5 JWE tokens.
// secret is the NEXTAUTH_SECRET, salt is the cookie name (e.g. "authjs.session-token").
func NewJWTValidator(secret, salt string) (*JWTValidator, error) {
	if secret == "" {
		return nil, errors.New("jwt secret is required")
	}
	if salt == "" {
		salt = "authjs.session-token"
	}

	key, err := deriveEncryptionKey(secret, salt, 64)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	return &JWTValidator{encryptionKey: key}, nil
}

// Validate decrypts a NextAuth JWE token and returns the claims.
func (v *JWTValidator) Validate(tokenStr string) (*Claims, error) {
	jwe, err := jose.ParseEncrypted(tokenStr, []jose.KeyAlgorithm{jose.DIRECT}, []jose.ContentEncryption{jose.A256CBC_HS512})
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidToken, err)
	}

	plaintext, err := jwe.Decrypt(v.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt: %v", ErrInvalidToken, err)
	}

	var claims Claims
	if err := json.Unmarshal(plaintext, &claims); err != nil {
		return nil, fmt.Errorf("%w: unmarshal: %v", ErrInvalidToken, err)
	}

	if claims.Exp > 0 && time.Now().Unix() > claims.Exp+15 {
		return nil, ErrTokenExpired
	}

	if claims.ID == "" {
		return nil, fmt.Errorf("%w: missing user id", ErrInvalidToken)
	}

	return &claims, nil
}

// deriveEncryptionKey replicates NextAuth's getDerivedEncryptionKey.
// HKDF-SHA256 with info="Auth.js Generated Encryption Key ({salt})"
func deriveEncryptionKey(secret, salt string, length int) ([]byte, error) {
	info := fmt.Sprintf("Auth.js Generated Encryption Key (%s)", salt)
	hkdfReader := hkdf.New(sha256.New, []byte(secret), []byte(salt), []byte(info))

	key := make([]byte, length)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, err
	}
	return key, nil
}
