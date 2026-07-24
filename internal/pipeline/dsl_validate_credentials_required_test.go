package pipeline

// dsl_validate_credentials_required_test.go — pins the enforcement half of
// #1418: `credentials_required` was declared-only. A routine declaring a
// required credential must FAIL validation when that credential is not
// resolvable in the run scope, and PASS when it is.

import (
	"context"
	"testing"
)

func TestRequiredCredentialTypes(t *testing.T) {
	dsl := &DSL{CredsRequired: []CredReq{
		{Type: "  Stripe "}, {Type: "stripe"}, {Type: ""}, {Type: "twilio"},
	}}
	got := RequiredCredentialTypes(dsl)
	// trimmed, lowercased, de-duplicated, empties dropped
	want := map[string]bool{"stripe": true, "twilio": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want the 2 distinct types", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected type %q", g)
		}
	}
}

// TestNewVaultCredentialProbe wires the probe against a seeded vault to
// prove it reports real resolvability (ACTIVE + scope) without ever
// returning the value.
func TestNewVaultCredentialProbe(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCredential(t, db, "cred_ws", "ws_test", "", "STRIPE", "ACTIVE", "sk_live_x", "2026-01-01T00:00:00Z")
	seedCredential(t, db, "cred_pending", "ws_test", "", "TWILIO", "PENDING", "placeholder", "2026-01-02T00:00:00Z")

	probe := NewVaultCredentialProbe(db)
	scope := RunScope{WorkspaceID: "ws_test"}

	ok, err := probe(context.Background(), scope, "stripe")
	if err != nil || !ok {
		t.Errorf("stripe should resolve: ok=%v err=%v", ok, err)
	}
	ok, err = probe(context.Background(), scope, "twilio")
	if err != nil || ok {
		t.Errorf("PENDING twilio must NOT resolve: ok=%v err=%v", ok, err)
	}
	ok, err = probe(context.Background(), scope, "unknown")
	if err != nil || ok {
		t.Errorf("unknown type must not resolve: ok=%v err=%v", ok, err)
	}
}
