package api

import (
	"context"
	"testing"
)

func TestCraftNativeAgentStoppedManagementProofNeverBecomesDocumentAuthority(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	if w := call(`{"operation":"acquire"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	e := m.runs[f.a.RunID]
	nativeAgentRetirementRuntime(m, e, false)
	proof, err := m.stoppedProof(human, e)
	if err != nil {
		t.Fatal("positive original-exec stopped proof", err)
	}
	tx, err := m.writer(human)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	proof.tx = tx
	ctx := context.WithValue(human, craftNativeAgentStoppedKey{}, proof)
	for _, intent := range []string{"read", "write"} {
		if _, err := m.human.authorizeNative(ctx, tx, e.grant.Scope, intent); err == nil {
			t.Fatalf("real stopped management proof acquired native %s authority", intent)
		}
	}
}
