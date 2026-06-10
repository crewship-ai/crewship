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
