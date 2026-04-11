package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Edition represents the license tier (community, team, or enterprise).
type Edition string

const (
	EditionCommunity  Edition = "community"
	EditionTeam       Edition = "team"
	EditionEnterprise Edition = "enterprise"
)

// Claims holds the verified payload of a signed license, including resource
// limits, edition, and optional feature flags.
type Claims struct {
	LicenseID    string   `json:"license_id"`
	LicenseeName string   `json:"licensee_name"`
	LicenseeOrg  string   `json:"licensee_org"`
	Edition      Edition  `json:"edition"`
	MaxCrews     int      `json:"max_crews"`
	MaxAgents    int      `json:"max_agents_per_crew"`
	MaxMembers   int      `json:"max_members"`
	Features     []string `json:"features,omitempty"`
	IssuedAt     int64    `json:"issued_at"`
	ExpiresAt    int64    `json:"expires_at"`
}

var communityDefaults = Claims{
	LicenseID:    "community",
	LicenseeName: "Community User",
	LicenseeOrg:  "",
	Edition:      EditionCommunity,
	MaxCrews:     15,
	MaxAgents:    10,
	MaxMembers:   5,
	Features:     nil,
}

type signedLicense struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// License holds the verified license state.
type License struct {
	mu     sync.RWMutex
	claims Claims
	valid  bool
}

// publicKey is the embedded Ed25519 public key for license verification.
// This is set at build time via ldflags or replaced during release builds.
// Format: base64-encoded 32-byte Ed25519 public key.
var publicKey = ""

// New creates a License initialized with community edition defaults.
func New() *License {
	return &License{
		claims: communityDefaults,
		valid:  true,
	}
}

// LoadFromFile loads and verifies a signed license file.
// If the file doesn't exist or verification fails, community defaults remain.
func (l *License) LoadFromFile(path string) error {
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read license file: %w", err)
	}

	return l.LoadFromBytes(data)
}

// LoadFromBytes parses and verifies a signed license from raw bytes.
func (l *License) LoadFromBytes(data []byte) error {
	var signed signedLicense
	if err := json.Unmarshal(data, &signed); err != nil {
		return fmt.Errorf("parse license: %w", err)
	}

	if publicKey == "" {
		return fmt.Errorf("no public key embedded in binary")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: %d", len(pubKeyBytes))
	}

	sigBytes, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	payloadBytes := []byte(signed.Payload)
	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), payloadBytes, sigBytes) {
		return fmt.Errorf("invalid license signature")
	}

	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return fmt.Errorf("parse license claims: %w", err)
	}

	if claims.ExpiresAt > 0 && time.Now().Unix() > claims.ExpiresAt {
		return fmt.Errorf("license expired on %s", time.Unix(claims.ExpiresAt, 0).Format(time.RFC3339))
	}

	l.mu.Lock()
	l.claims = claims
	l.valid = true
	l.mu.Unlock()

	return nil
}

// Claims returns a copy of the current license claims.
func (l *License) Claims() Claims {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claims
}

// Edition returns the current license edition (community, team, or enterprise).
func (l *License) Edition() Edition {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claims.Edition
}

// MaxCrews returns the maximum number of crews allowed per workspace.
func (l *License) MaxCrews() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claims.MaxCrews
}

// MaxAgentsPerCrew returns the maximum number of agents allowed in a single crew.
func (l *License) MaxAgentsPerCrew() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claims.MaxAgents
}

// MaxMembers returns the maximum number of members allowed per workspace.
func (l *License) MaxMembers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claims.MaxMembers
}

// HasFeature reports whether the license includes the named feature flag.
func (l *License) HasFeature(feature string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, f := range l.claims.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// IsEnterprise reports whether the license is enterprise edition.
func (l *License) IsEnterprise() bool {
	return l.Edition() == EditionEnterprise
}

// IsCommunity reports whether the license is community (free) edition.
func (l *License) IsCommunity() bool {
	return l.Edition() == EditionCommunity
}

// CommunityDefaults returns the default community license claims.
func CommunityDefaults() Claims {
	return communityDefaults
}

// setPublicKey allows overriding the embedded public key (for testing only).
// Unexported to prevent external code from bypassing license verification.
func setPublicKey(key string) {
	publicKey = key
}
