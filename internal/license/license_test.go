package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func generateTestKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func signClaims(t *testing.T, priv ed25519.PrivateKey, claims Claims) []byte {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, payload)

	signed := signedLicense{
		Payload:   string(payload),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}

	data, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNewReturnsDefaults(t *testing.T) {
	l := New()
	if l.Edition() != EditionCommunity {
		t.Errorf("expected community edition, got %s", l.Edition())
	}
	if l.MaxCrews() != 15 {
		t.Errorf("expected 15 max crews, got %d", l.MaxCrews())
	}
	if l.MaxAgentsPerCrew() != 10 {
		t.Errorf("expected 10 max agents, got %d", l.MaxAgentsPerCrew())
	}
	if l.MaxMembers() != 5 {
		t.Errorf("expected 5 max members, got %d", l.MaxMembers())
	}
	if !l.IsCommunity() {
		t.Error("expected IsCommunity() = true")
	}
}

func TestLoadValidLicense(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	setPublicKey(base64.StdEncoding.EncodeToString(pub))
	defer setPublicKey("")

	claims := Claims{
		LicenseID:    "test-001",
		LicenseeName: "Test User",
		LicenseeOrg:  "Test Corp",
		Edition:      EditionEnterprise,
		MaxCrews:     100,
		MaxAgents:    50,
		MaxMembers:   200,
		Features:     []string{"sso", "audit_export"},
		IssuedAt:     time.Now().Unix(),
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Unix(),
	}

	data := signClaims(t, priv, claims)

	l := New()
	if err := l.LoadFromBytes(data); err != nil {
		t.Fatalf("LoadFromBytes: %v", err)
	}

	if l.Edition() != EditionEnterprise {
		t.Errorf("expected enterprise, got %s", l.Edition())
	}
	if l.MaxCrews() != 100 {
		t.Errorf("expected 100, got %d", l.MaxCrews())
	}
	if l.MaxAgentsPerCrew() != 50 {
		t.Errorf("expected 50, got %d", l.MaxAgentsPerCrew())
	}
	if l.MaxMembers() != 200 {
		t.Errorf("expected 200, got %d", l.MaxMembers())
	}
	if !l.IsEnterprise() {
		t.Error("expected IsEnterprise() = true")
	}
	if !l.HasFeature("sso") {
		t.Error("expected HasFeature('sso') = true")
	}
	if l.HasFeature("nonexistent") {
		t.Error("expected HasFeature('nonexistent') = false")
	}
}

// TestClaimsDeepCopiesFeatures verifies a caller mutating the Features
// slice on a returned Claims doesn't corrupt the live license state.
// Without the deep-copy, the value-copy of Claims still shared its
// underlying array — a downstream caller flipping Features[0] = "X"
// silently rewrote what HasFeature() returned.
func TestClaimsDeepCopiesFeatures(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	setPublicKey(base64.StdEncoding.EncodeToString(pub))
	defer setPublicKey("")

	claims := Claims{
		LicenseID:  "deep-copy-test",
		Edition:    EditionEnterprise,
		MaxCrews:   1,
		MaxAgents:  1,
		MaxMembers: 1,
		Features:   []string{"sso", "audit_export"},
		IssuedAt:   time.Now().Unix(),
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
	}
	data := signClaims(t, priv, claims)
	l := New()
	if err := l.LoadFromBytes(data); err != nil {
		t.Fatalf("LoadFromBytes: %v", err)
	}

	c := l.Claims()
	if len(c.Features) != 2 {
		t.Fatalf("expected 2 features, got %d", len(c.Features))
	}
	c.Features[0] = "POISONED"

	// HasFeature must still see the original value — a caller's mutation
	// of the returned slice is not allowed to mutate live state.
	if !l.HasFeature("sso") {
		t.Errorf("Claims().Features mutation poisoned live state — expected 'sso' to still be present")
	}
	if l.HasFeature("POISONED") {
		t.Errorf("'POISONED' leaked into live state via shared slice backing")
	}
}

func TestLoadExpiredLicense(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	setPublicKey(base64.StdEncoding.EncodeToString(pub))
	defer setPublicKey("")

	claims := Claims{
		LicenseID:    "expired-001",
		LicenseeName: "Expired User",
		Edition:      EditionTeam,
		MaxCrews:     50,
		MaxAgents:    25,
		MaxMembers:   25,
		IssuedAt:     time.Now().Add(-2 * 365 * 24 * time.Hour).Unix(),
		ExpiresAt:    time.Now().Add(-24 * time.Hour).Unix(), // expired yesterday
	}

	data := signClaims(t, priv, claims)

	l := New()
	err := l.LoadFromBytes(data)
	if err == nil {
		t.Fatal("expected error for expired license")
	}

	// Should still have community defaults
	if l.Edition() != EditionCommunity {
		t.Errorf("expected community after expired, got %s", l.Edition())
	}
}

func TestLoadInvalidSignature(t *testing.T) {
	pub, _ := generateTestKeypair(t)
	_, otherPriv := generateTestKeypair(t) // different key
	setPublicKey(base64.StdEncoding.EncodeToString(pub))
	defer setPublicKey("")

	claims := Claims{
		LicenseID: "tampered",
		Edition:   EditionEnterprise,
		MaxCrews:  999,
		IssuedAt:  time.Now().Unix(),
		ExpiresAt: time.Now().Add(365 * 24 * time.Hour).Unix(),
	}

	data := signClaims(t, otherPriv, claims) // signed with wrong key

	l := New()
	err := l.LoadFromBytes(data)
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestLoadFromFile(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	setPublicKey(base64.StdEncoding.EncodeToString(pub))
	defer setPublicKey("")

	claims := Claims{
		LicenseID:    "file-001",
		LicenseeName: "File Test",
		Edition:      EditionTeam,
		MaxCrews:     30,
		MaxAgents:    20,
		MaxMembers:   15,
		IssuedAt:     time.Now().Unix(),
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Unix(),
	}

	data := signClaims(t, priv, claims)

	dir := t.TempDir()
	path := filepath.Join(dir, "license.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	l := New()
	if err := l.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if l.Edition() != EditionTeam {
		t.Errorf("expected team, got %s", l.Edition())
	}
}

func TestLoadFromFileNotFound(t *testing.T) {
	l := New()
	err := l.LoadFromFile("/nonexistent/path/license.json")
	if err != nil {
		t.Fatalf("expected nil for non-existent file, got %v", err)
	}
	if l.Edition() != EditionCommunity {
		t.Errorf("expected community defaults, got %s", l.Edition())
	}
}

func TestLoadFromFileEmpty(t *testing.T) {
	l := New()
	err := l.LoadFromFile("")
	if err != nil {
		t.Fatalf("expected nil for empty path, got %v", err)
	}
}

func TestNoPublicKey(t *testing.T) {
	setPublicKey("")
	defer setPublicKey("")

	data := []byte(`{"payload":"{}","signature":"AAAA"}`)

	l := New()
	err := l.LoadFromBytes(data)
	if err == nil {
		t.Fatal("expected error when no public key embedded")
	}
}

func TestLimitError(t *testing.T) {
	err := &LimitError{
		Resource: "crews",
		Current:  15,
		Maximum:  15,
		Edition:  EditionCommunity,
	}

	if !IsLimitError(err) {
		t.Error("expected IsLimitError = true")
	}

	msg := err.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
}
