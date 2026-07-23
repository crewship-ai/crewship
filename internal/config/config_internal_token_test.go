package config

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

const testEncKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // gitleaks:allow

// loadEnv sets the env a bare Load("") needs to run in a unit test (skip the
// sidecar autodetect) plus the given encryption key + explicit internal token.
func loadEnv(t *testing.T, encKey, internalToken string) {
	t.Helper()
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	t.Setenv("ENCRYPTION_KEY", encKey)
	t.Setenv("CREWSHIP_INTERNAL_TOKEN", internalToken)
}

// TestInternalTokenStableAcrossReloads is the #1385 regression: with no
// explicit internal token but a persisted ENCRYPTION_KEY, two independent
// Loads (simulating a server restart) derive the SAME master. Before the fix
// each Load produced a fresh crypto/rand value, so a restart invalidated every
// crew-bound token held by surviving containers.
func TestInternalTokenStableAcrossReloads(t *testing.T) {
	loadEnv(t, testEncKey, "")

	c1, err := Load("")
	if err != nil {
		t.Fatalf("load 1: %v", err)
	}
	c2, err := Load("")
	if err != nil {
		t.Fatalf("load 2: %v", err)
	}
	if c1.Auth.InternalToken == "" {
		t.Fatal("internal token is empty")
	}
	if c1.Auth.InternalToken != c2.Auth.InternalToken {
		t.Errorf("internal token changed across reloads: %q != %q — a restart would invalidate live container tokens", c1.Auth.InternalToken, c2.Auth.InternalToken)
	}
	if want := deriveInternalTokenMaster(testEncKey); c1.Auth.InternalToken != want {
		t.Errorf("internal token = %q, want deterministic derivation %q", c1.Auth.InternalToken, want)
	}
}

// TestCrewTokenSurvivesRestart proves the end-to-end property the bug is about:
// a crew-bound token minted in "boot 1" still validates in "boot 2" once the
// master is derived from the stable ENCRYPTION_KEY.
func TestCrewTokenSurvivesRestart(t *testing.T) {
	loadEnv(t, testEncKey, "")

	boot1, err := Load("")
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	// A surviving container holds this token from before the restart.
	token := internaltoken.DeriveCrewToken(boot1.Auth.InternalToken, "ws_1", "crew_1")
	if token == "" {
		t.Fatal("failed to mint crew token")
	}

	boot2, err := Load("")
	if err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	ws, crew, ok := internaltoken.ValidateCrewToken(boot2.Auth.InternalToken, token)
	if !ok {
		t.Fatal("crew token minted in boot 1 no longer validates in boot 2 — restart still breaks container auth")
	}
	if ws != "ws_1" || crew != "crew_1" {
		t.Errorf("validated scope = (%q,%q), want (ws_1,crew_1)", ws, crew)
	}
}

// TestInternalTokenExplicitOverrideWins confirms an operator-supplied token is
// never clobbered by the derivation — the derivation only fills the gap.
func TestInternalTokenExplicitOverrideWins(t *testing.T) {
	const custom = "operator-chosen-internal-token-value"
	loadEnv(t, testEncKey, custom)

	c, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Auth.InternalToken != custom {
		t.Errorf("internal token = %q, want the explicit override %q", c.Auth.InternalToken, custom)
	}
}

// TestInternalTokenRandomFallbackWithoutEncryptionKey keeps the old behavior
// for the plaintext dev mode: with no encryption key to anchor to, the master
// falls back to a per-boot random (non-empty, and different each Load).
func TestInternalTokenRandomFallbackWithoutEncryptionKey(t *testing.T) {
	loadEnv(t, "", "")

	c1, err := Load("")
	if err != nil {
		t.Fatalf("load 1: %v", err)
	}
	c2, err := Load("")
	if err != nil {
		t.Fatalf("load 2: %v", err)
	}
	if c1.Auth.InternalToken == "" || c2.Auth.InternalToken == "" {
		t.Fatal("fallback internal token is empty")
	}
	if c1.Auth.InternalToken == c2.Auth.InternalToken {
		t.Error("random fallback produced identical tokens across loads — expected per-boot randomness")
	}
}

// TestDeriveInternalTokenMaster_Deterministic pins the derivation primitive:
// stable per seed, distinct across seeds, 256-bit hex output.
func TestDeriveInternalTokenMaster_Deterministic(t *testing.T) {
	a := deriveInternalTokenMaster(testEncKey)
	if a != deriveInternalTokenMaster(testEncKey) {
		t.Error("derivation is not deterministic for the same seed")
	}
	if a == deriveInternalTokenMaster(testEncKey+"x") {
		t.Error("derivation collided across different seeds")
	}
	if len(a) != 64 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Errorf("derived master = %q, want 64 lowercase-hex chars", a)
	}
}
