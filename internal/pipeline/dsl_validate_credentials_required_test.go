package pipeline

// dsl_validate_credentials_required_test.go — pins the enforcement half of
// #1418: `credentials_required` was declared-only. A routine declaring a
// required credential must FAIL validation when that credential is not
// resolvable in the run scope, and PASS when it is.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidateRequiredCredentials(t *testing.T) {
	ctx := context.Background()

	// probe: "stripe" resolves, everything else does not.
	probe := func(_ context.Context, credType string) (bool, error) {
		return strings.EqualFold(credType, "stripe"), nil
	}

	t.Run("passes when every declared credential resolves", func(t *testing.T) {
		dsl := &DSL{Name: "pays", CredsRequired: []CredReq{{Type: "stripe"}}}
		if err := ValidateRequiredCredentials(ctx, dsl, probe); err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
	})

	t.Run("fails when a declared credential is unresolvable", func(t *testing.T) {
		dsl := &DSL{Name: "pays", CredsRequired: []CredReq{{Type: "stripe"}, {Type: "twilio"}}}
		err := ValidateRequiredCredentials(ctx, dsl, probe)
		if err == nil {
			t.Fatal("expected failure for unresolvable credential")
		}
		if !strings.Contains(err.Error(), "twilio") {
			t.Errorf("error should name the missing credential, got %v", err)
		}
	})

	t.Run("no declared credentials is a no-op pass", func(t *testing.T) {
		if err := ValidateRequiredCredentials(ctx, &DSL{Name: "x"}, probe); err != nil {
			t.Fatalf("empty credentials_required should pass, got %v", err)
		}
	})

	t.Run("empty type entry is rejected", func(t *testing.T) {
		dsl := &DSL{Name: "x", CredsRequired: []CredReq{{Type: "   "}}}
		if err := ValidateRequiredCredentials(ctx, dsl, probe); err == nil {
			t.Fatal("expected failure for empty credential type")
		}
	})

	t.Run("nil probe fails closed", func(t *testing.T) {
		dsl := &DSL{Name: "x", CredsRequired: []CredReq{{Type: "stripe"}}}
		if err := ValidateRequiredCredentials(ctx, dsl, nil); err == nil {
			t.Fatal("expected failure when no probe is available to confirm resolvability")
		}
	})

	t.Run("probe error surfaces (not silently allowed)", func(t *testing.T) {
		boom := func(_ context.Context, _ string) (bool, error) { return false, errors.New("db down") }
		dsl := &DSL{Name: "x", CredsRequired: []CredReq{{Type: "stripe"}}}
		if err := ValidateRequiredCredentials(ctx, dsl, boom); err == nil {
			t.Fatal("expected probe error to fail validation")
		}
	})
}

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
