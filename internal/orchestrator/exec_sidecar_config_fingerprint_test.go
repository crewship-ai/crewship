package orchestrator

import "testing"

func TestSidecarConfigFingerprintTracksCredentialMaterial(t *testing.T) {
	t.Parallel()

	key := "internal-master-for-test"
	base := []Credential{{
		ID: "cred-1", Provider: "OPENAI_COMPAT", PlainValue: "secret-one",
		Priority: 2, BaseURL: "https://gateway.example/v1",
		Headers: map[string]string{"X-Org": "org-secret"},
	}}

	got := sidecarConfigFingerprint(key, base)
	if len(got) != 24 {
		t.Fatalf("fingerprint length = %d, want 24", len(got))
	}
	rotated := append([]Credential(nil), base...)
	rotated[0].PlainValue = "secret-two"
	if got == sidecarConfigFingerprint(key, rotated) {
		t.Fatal("credential rotation did not change config fingerprint")
	}

	headerRotated := append([]Credential(nil), base...)
	headerRotated[0].Headers = map[string]string{"X-Org": "new-org-secret"}
	if got == sidecarConfigFingerprint(key, headerRotated) {
		t.Fatal("header rotation did not change config fingerprint")
	}

	endpointMoved := append([]Credential(nil), base...)
	endpointMoved[0].BaseURL = "https://other-gateway.example/v1"
	if got == sidecarConfigFingerprint(key, endpointMoved) {
		t.Fatal("endpoint change did not change config fingerprint")
	}

	if fp := sidecarConfigFingerprint("", base); fp != "" {
		t.Fatalf("unkeyed fingerprint = %q, want empty rather than a credential oracle", fp)
	}
}

func TestSidecarConfigFingerprintIsOrderIndependent(t *testing.T) {
	t.Parallel()

	key := "internal-master-for-test"
	a := Credential{ID: "a", EnvVarName: "OPENAI_API_KEY", PlainValue: "one", Priority: 1}
	b := Credential{ID: "b", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "two", Priority: 2}

	left := sidecarConfigFingerprint(key, []Credential{a, b})
	right := sidecarConfigFingerprint(key, []Credential{b, a})
	if left != right {
		t.Fatalf("delivery order changed fingerprint: %q != %q", left, right)
	}
}

func TestSidecarNeedsRestartOnConfigFingerprintDrift(t *testing.T) {
	t.Parallel()

	health := &sidecarHealth{
		NetworkMode:       "free",
		ConfigFingerprint: "running",
	}
	if sidecarNeedsRestart(health, "free", nil, "running") {
		t.Fatal("matching config fingerprint requested a restart")
	}
	if !sidecarNeedsRestart(health, "free", nil, "rotated") {
		t.Fatal("rotated config fingerprint reused stale sidecar")
	}
	health.ConfigFingerprint = ""
	if !sidecarNeedsRestart(health, "free", nil, "desired") {
		t.Fatal("pre-fingerprint sidecar reused despite a keyed desired config")
	}
}
