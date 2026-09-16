package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// Rotation grace in the boot payload (#1882): the previous value rides beside
// the current one, and only there.

func TestBuildSidecarCreds_CarriesRotationGrace(t *testing.T) {
	t.Parallel()
	sc := buildSidecarCreds([]Credential{
		{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", Type: "API_KEY", Provider: "ANTHROPIC", PlainValue: "sk-new",
			GraceToken: "sk-old", GraceExpiresAt: "2026-01-02T00:00:00Z", GraceRotationID: "rot_1"},
		{ID: "c2", EnvVarName: "OPENAI_API_KEY", Type: "API_KEY", Provider: "OPENAI", PlainValue: "sk-oa"},
	}, nil)
	if len(sc) != 2 {
		t.Fatalf("built %d creds, want 2", len(sc))
	}
	if sc[0].GraceToken != "sk-old" || sc[0].GraceExpiresAt != "2026-01-02T00:00:00Z" || sc[0].GraceRotationID != "rot_1" {
		t.Errorf("grace not carried: %+v", sc[0])
	}
	if sc[1].GraceToken != "" || sc[1].GraceExpiresAt != "" || sc[1].GraceRotationID != "" {
		t.Errorf("grace invented for a credential with no rotation: %+v", sc[1])
	}

	// The wire shape: present for c1, absent (not empty) for c2, so a
	// credential with no open rotation serialises exactly as before #1882.
	blob, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"grace_token":"sk-old"`) {
		t.Errorf("payload lacks the grace token: %s", blob)
	}
	if strings.Count(string(blob), `"grace_token"`) != 1 {
		t.Errorf("grace_token key emitted for a credential without one: %s", blob)
	}
}

// The fingerprint is the configuration identity the orchestrator restarts a
// shared sidecar on. A rotation already moves it (the token changed); the
// grace window opening, changing or closing must not, or every sidecar shared
// by concurrent runs restarts once more when the window expires.
func TestSidecarConfigFingerprint_IgnoresRotationGrace(t *testing.T) {
	t.Parallel()
	key := "internal-master-for-test"
	base := []Credential{{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", Type: "API_KEY", Provider: "ANTHROPIC", PlainValue: "sk-new"}}
	withGrace := []Credential{{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", Type: "API_KEY", Provider: "ANTHROPIC", PlainValue: "sk-new",
		GraceToken: "sk-old", GraceExpiresAt: "2026-01-02T00:00:00Z", GraceRotationID: "rot_1"}}

	if sidecarConfigFingerprint(key, base) != sidecarConfigFingerprint(key, withGrace) {
		t.Fatal("an open grace window changed the config fingerprint")
	}
	rotated := []Credential{{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", Type: "API_KEY", Provider: "ANTHROPIC", PlainValue: "sk-newer",
		GraceToken: "sk-new", GraceExpiresAt: "2026-01-03T00:00:00Z", GraceRotationID: "rot_2"}}
	if sidecarConfigFingerprint(key, base) == sidecarConfigFingerprint(key, rotated) {
		t.Fatal("a rotation (token change) did not change the config fingerprint")
	}
}
