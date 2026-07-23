package internaltoken

import (
	"strings"
	"testing"
)

func TestDeriveAndValidate_RoundTrip(t *testing.T) {
	t.Parallel()
	tok := DeriveWorkspaceToken("master-secret", "ws_123")
	if tok == "" {
		t.Fatal("expected non-empty token")
	}
	if !IsWorkspaceToken(tok) {
		t.Fatalf("derived token not recognized as workspace token: %q", tok)
	}
	ws, ok := ValidateWorkspaceToken("master-secret", tok)
	if !ok {
		t.Fatal("derived token failed validation against same master")
	}
	if ws != "ws_123" {
		t.Fatalf("bound workspace = %q, want ws_123", ws)
	}
}

func TestDerive_NeverEqualsMaster(t *testing.T) {
	t.Parallel()
	// The whole point of PR-F24: the secret handed to a sidecar must
	// not be the master. If this fails, containers hold the global
	// secret again.
	master := "master-secret"
	tok := DeriveWorkspaceToken(master, "ws_a")
	if tok == master {
		t.Fatal("derived token equals master secret")
	}
	if strings.Contains(tok, master) {
		t.Fatal("derived token leaks master secret")
	}
}

func TestDerive_EmptyInputsRefuseToIssue(t *testing.T) {
	t.Parallel()
	if got := DeriveWorkspaceToken("", "ws_a"); got != "" {
		t.Errorf("empty master should not issue a token; got %q", got)
	}
	if got := DeriveWorkspaceToken("master", ""); got != "" {
		t.Errorf("empty workspace should not issue a token; got %q", got)
	}
}

func TestValidate_RejectsTamperedWorkspace(t *testing.T) {
	t.Parallel()
	tok := DeriveWorkspaceToken("master-secret", "ws_a")
	forged := strings.Replace(tok, "ws_a", "ws_b", 1)
	if _, ok := ValidateWorkspaceToken("master-secret", forged); ok {
		t.Fatal("token with swapped workspace segment must NOT validate — this is the cross-tenant forgery")
	}
}

func TestValidate_RejectsTamperedMAC(t *testing.T) {
	t.Parallel()
	tok := DeriveWorkspaceToken("master-secret", "ws_a")
	// Flip the last hex char.
	last := tok[len(tok)-1]
	flip := byte('0')
	if last == '0' {
		flip = '1'
	}
	forged := tok[:len(tok)-1] + string(flip)
	if _, ok := ValidateWorkspaceToken("master-secret", forged); ok {
		t.Fatal("token with tampered MAC must not validate")
	}
}

func TestValidate_RejectsWrongMaster(t *testing.T) {
	t.Parallel()
	tok := DeriveWorkspaceToken("master-A", "ws_a")
	if _, ok := ValidateWorkspaceToken("master-B", tok); ok {
		t.Fatal("token derived from a different master must not validate")
	}
}

func TestValidate_RejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"wsv1",
		"wsv1.",
		"wsv1..deadbeef",     // empty workspace segment
		"wsv1.ws_a",          // missing MAC
		"wsv2.ws_a.deadbeef", // unknown version prefix
		"not-a-token-at-all", // master-shaped opaque string
		"wsv1.ws_a.deadbeef", // wrong MAC
		"wsv1.ws_a.",         // empty MAC
	}
	for _, c := range cases {
		if ws, ok := ValidateWorkspaceToken("master", c); ok {
			t.Errorf("ValidateWorkspaceToken(%q) = (%q, true), want reject", c, ws)
		}
	}
}

func TestValidate_EmptyMasterFailsClosed(t *testing.T) {
	t.Parallel()
	// With an empty master, even a token that was (somehow) derived
	// from an empty master must not validate — empty-key HMACs are
	// computable by anyone.
	if _, ok := ValidateWorkspaceToken("", "wsv1.ws_a.deadbeef"); ok {
		t.Fatal("empty master must reject every token")
	}
}

func TestValidate_WorkspaceIDWithDots(t *testing.T) {
	t.Parallel()
	// Workspace IDs are opaque to this package; a "." inside one must
	// still round-trip because the MAC segment (hex) can't contain a
	// dot and parsing splits on the LAST separator.
	tok := DeriveWorkspaceToken("master", "ws.with.dots")
	ws, ok := ValidateWorkspaceToken("master", tok)
	if !ok || ws != "ws.with.dots" {
		t.Fatalf("got (%q, %v), want (ws.with.dots, true)", ws, ok)
	}
}

func TestIsWorkspaceToken(t *testing.T) {
	t.Parallel()
	if IsWorkspaceToken("opaque-master-token") {
		t.Error("master-shaped token misclassified as workspace token")
	}
	if !IsWorkspaceToken("wsv1.ws_a.deadbeef") {
		t.Error("workspace-shaped token not recognized")
	}
	// Bare prefix without separator is NOT a workspace token — a
	// master that happens to start with "wsv1" but lacks the dot must
	// keep using the master compare path.
	if IsWorkspaceToken("wsv1deadbeef") {
		t.Error("prefix without separator misclassified")
	}
}

func TestSignCaller_RoundTripAndBinding(t *testing.T) {
	t.Parallel()
	tok := DeriveWorkspaceToken("master-secret", "ws_123")
	sig := SignCaller(tok, "ws_123", "user_abc")
	if sig == "" {
		t.Fatal("expected non-empty signature")
	}
	if !VerifyCaller(tok, "ws_123", "user_abc", sig) {
		t.Error("valid signature did not verify")
	}
	// Wrong caller id, wrong workspace, wrong key, and tampered sig all fail.
	if VerifyCaller(tok, "ws_123", "user_other", sig) {
		t.Error("signature verified for a different caller id")
	}
	if VerifyCaller(tok, "ws_other", "user_abc", sig) {
		t.Error("signature verified for a different workspace")
	}
	if VerifyCaller("other-token", "ws_123", "user_abc", sig) {
		t.Error("signature verified under a different key")
	}
	if VerifyCaller(tok, "ws_123", "user_abc", sig+"00") {
		t.Error("tampered signature verified")
	}
}

func TestSignCaller_EmptyInputsFailClosed(t *testing.T) {
	t.Parallel()
	if SignCaller("", "ws", "u") != "" || SignCaller("tok", "", "u") != "" || SignCaller("tok", "ws", "") != "" {
		t.Error("SignCaller must return empty on any empty input")
	}
	// Empty signature, empty token, or empty ids never verify.
	if VerifyCaller("tok", "ws", "u", "") {
		t.Error("empty signature must not verify")
	}
	if VerifyCaller("", "ws", "u", "deadbeef") {
		t.Error("empty token must not verify")
	}
}

func TestSignCaller_DomainSeparatedFromWorkspaceMAC(t *testing.T) {
	t.Parallel()
	// The caller-identity HMAC must not collide with the workspace-
	// binding HMAC even over the same key material + workspace id.
	tok := "shared-key"
	if SignCaller(tok, "ws_x", "ws_x") == mac(tok, "ws_x") {
		t.Error("caller-identity MAC collides with workspace-binding MAC (missing domain separation)")
	}
}

func TestDeriveAgentToken_Properties(t *testing.T) {
	t.Parallel()
	master := "master-secret"
	a := DeriveAgentToken(master, "ws_1", "agent_a")
	b := DeriveAgentToken(master, "ws_1", "agent_b")
	if a == "" || b == "" {
		t.Fatal("expected non-empty per-agent tokens")
	}
	if a == b {
		t.Fatal("distinct agents must get distinct tokens")
	}
	// Deterministic: same inputs → same token (the sidecar re-derives the
	// roster it matches against).
	if a != DeriveAgentToken(master, "ws_1", "agent_a") {
		t.Fatal("derivation is not deterministic")
	}
	// Never leaks the master.
	if strings.Contains(a, master) {
		t.Fatal("per-agent token leaks the master secret")
	}
	// Same agent id in a different workspace must not collide (cross-tenant).
	if a == DeriveAgentToken(master, "ws_2", "agent_a") {
		t.Fatal("token must be bound to the workspace, not just the agent id")
	}
	// A different master yields a different token (unforgeable from inside a
	// container that never sees the master).
	if a == DeriveAgentToken("other-master", "ws_1", "agent_a") {
		t.Fatal("token must depend on the master secret")
	}
}

func TestDeriveAgentToken_EmptyInputsFailClosed(t *testing.T) {
	t.Parallel()
	if DeriveAgentToken("", "ws", "a") != "" {
		t.Error("empty master must not issue a token")
	}
	if DeriveAgentToken("m", "", "a") != "" {
		t.Error("empty workspace must not issue a token")
	}
	if DeriveAgentToken("m", "ws", "") != "" {
		t.Error("empty agent id must not issue a token")
	}
}

func TestDeriveAgentToken_DomainSeparated(t *testing.T) {
	t.Parallel()
	// The per-agent MAC must not collide with the workspace-binding MAC even
	// over the same key material + workspace id (an agent token must never
	// validate as a workspace token or vice versa).
	master := "shared-key"
	agentMac := DeriveAgentToken(master, "ws_x", "ws_x")
	if strings.HasSuffix(agentMac, mac(master, "ws_x")) {
		t.Error("per-agent MAC collides with workspace-binding MAC (missing domain separation)")
	}
}

// --- #1159: crew-bound internal token -------------------------------------

const crewTestMaster = "master-secret-0123456789abcdef"

func TestDeriveCrewToken_RoundTrip(t *testing.T) {
	t.Parallel()
	const master = crewTestMaster
	tok := DeriveCrewToken(master, "ws_a", "crew_1")
	if !IsCrewToken(tok) {
		t.Fatalf("IsCrewToken(%q) = false, want true", tok)
	}
	// A crew token must NOT be mistaken for a workspace token (distinct prefix).
	if IsWorkspaceToken(tok) {
		t.Errorf("crew token %q also matched IsWorkspaceToken — prefixes must not collide", tok)
	}
	ws, crew, ok := ValidateCrewToken(master, tok)
	if !ok || ws != "ws_a" || crew != "crew_1" {
		t.Fatalf("ValidateCrewToken = (%q,%q,%v), want (ws_a,crew_1,true)", ws, crew, ok)
	}
}

func TestDeriveCrewToken_EmptyInputsFailClosed(t *testing.T) {
	t.Parallel()
	if DeriveCrewToken("", "ws", "c") != "" {
		t.Error("empty master must not issue a crew token")
	}
	if DeriveCrewToken("m", "", "c") != "" {
		t.Error("empty workspace must not issue a crew token")
	}
	if DeriveCrewToken("m", "ws", "") != "" {
		t.Error("empty crew must not issue a crew token")
	}
}

func TestValidateCrewToken_RejectsForgeries(t *testing.T) {
	t.Parallel()
	const master = crewTestMaster
	valid := DeriveCrewToken(master, "ws_a", "crew_1")

	cases := []struct {
		name  string
		token string
	}{
		// Rebind the crew segment to a sibling crew but keep the crew_1 MAC.
		{"swapped_crew_segment", CrewPrefix + ".ws_a.crew_victim." + valid[len(CrewPrefix+".ws_a.crew_1."):]},
		// Rebind the workspace segment (cross-tenant).
		{"swapped_workspace_segment", CrewPrefix + ".ws_victim.crew_1." + valid[len(CrewPrefix+".ws_a.crew_1."):]},
		{"tampered_mac", valid[:len(valid)-1] + "0"},
		{"wrong_master", DeriveCrewToken("other-master", "ws_a", "crew_1")},
		{"empty_crew_segment", CrewPrefix + ".ws_a..deadbeef"},
		{"empty_ws_segment", CrewPrefix + "..crew_1.deadbeef"},
		{"missing_segment", CrewPrefix + ".ws_a.deadbeef"},
		{"workspace_token_not_a_crew_token", DeriveWorkspaceToken(master, "ws_a")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := ValidateCrewToken(master, tc.token); ok {
				t.Errorf("ValidateCrewToken accepted a forgery: %q", tc.token)
			}
		})
	}
}

func TestValidateCrewToken_EmptyMasterFailsClosed(t *testing.T) {
	t.Parallel()
	tok := DeriveCrewToken(crewTestMaster, "ws_a", "crew_1")
	if _, _, ok := ValidateCrewToken("", tok); ok {
		t.Error("empty master must fail closed")
	}
}

func TestDeriveCrewToken_DomainSeparated(t *testing.T) {
	t.Parallel()
	// The crew MAC must not collide with the workspace-binding or per-agent
	// MAC even over the same key material — a crew token must never validate
	// as a workspace token, and vice versa.
	master := "shared-key"
	crewTok := DeriveCrewToken(master, "ws_x", "ws_x")
	if strings.HasSuffix(crewTok, mac(master, "ws_x")) {
		t.Error("crew MAC collides with workspace-binding MAC (missing domain separation)")
	}
	if strings.HasSuffix(crewTok, agentMAC(master, "ws_x", "ws_x")) {
		t.Error("crew MAC collides with per-agent MAC (missing domain separation)")
	}
}

func TestFingerprint_DeterministicAndDistinct(t *testing.T) {
	t.Parallel()

	// Empty token -> empty fingerprint (nothing for the server to compare).
	if fp := Fingerprint(""); fp != "" {
		t.Fatalf("Fingerprint(\"\") = %q, want empty", fp)
	}

	master := "master-secret"
	tokA := DeriveCrewToken(master, "ws_1", "crew_a")
	tokB := DeriveCrewToken(master, "ws_1", "crew_b")

	fpA1 := Fingerprint(tokA)
	fpA2 := Fingerprint(tokA)
	if fpA1 == "" {
		t.Fatal("expected non-empty fingerprint for a real token")
	}
	// Deterministic: the same token always fingerprints identically, so a
	// sidecar and the server independently derive the same value.
	if fpA1 != fpA2 {
		t.Fatalf("Fingerprint not deterministic: %q != %q", fpA1, fpA2)
	}
	// 12 hex chars, matching the other /health digests.
	if len(fpA1) != 12 {
		t.Fatalf("Fingerprint length = %d, want 12 (%q)", len(fpA1), fpA1)
	}
	// Distinct tokens fingerprint differently (this is what lets the server
	// notice a container holding a stale token).
	if fpA1 == Fingerprint(tokB) {
		t.Fatalf("distinct tokens shared a fingerprint: %q", fpA1)
	}

	// A token minted under a ROTATED master fingerprints differently — the
	// #1385 orphan-detection signal.
	tokRotated := DeriveCrewToken("different-master", "ws_1", "crew_a")
	if Fingerprint(tokRotated) == fpA1 {
		t.Fatal("token from a rotated master shares the old fingerprint — orphan detection would miss it")
	}

	// The fingerprint is not a substring of the token (one-way; nothing
	// leaked to an agent reading /health).
	if strings.Contains(tokA, fpA1) {
		t.Fatalf("fingerprint %q appears inside the token %q", fpA1, tokA)
	}
}
