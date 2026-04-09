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

// Claims represents the decoded payload of a NextAuth v5 JWE session token.
type Claims struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
	Jti   string `json:"jti"`
}

// JWTValidator decrypts and validates NextAuth v5 JWE session tokens using
// an HKDF-derived encryption key.
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

	if claims.Exp > 0 && time.Now().Unix() > claims.Exp+5 {
		return nil, ErrTokenExpired
	}

	if claims.ID == "" {
		return nil, fmt.Errorf("%w: missing user id", ErrInvalidToken)
	}

	return &claims, nil
}

// CreateToken creates a NextAuth-compatible JWE token from claims.
func (v *JWTValidator) CreateToken(claims *Claims) (string, error) {
	if claims.Iat == 0 {
		claims.Iat = time.Now().Unix()
	}
	if claims.Exp == 0 {
		claims.Exp = time.Now().Add(30 * 24 * time.Hour).Unix()
	}
	if claims.Jti == "" {
		claims.Jti = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	encrypter, err := jose.NewEncrypter(
		jose.A256CBC_HS512,
		jose.Recipient{Algorithm: jose.DIRECT, Key: v.encryptionKey},
		(&jose.EncrypterOptions{}).WithContentType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("create encrypter: %w", err)
	}

	jwe, err := encrypter.Encrypt(payload)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}

	return jwe.CompactSerialize()
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
