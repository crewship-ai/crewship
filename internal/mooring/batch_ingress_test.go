package mooring

import (
	"errors"
	"fmt"
	"testing"
)

func TestSessionIngressRejectsBeyondNegotiatedOperationLimit(t *testing.T) {
	key := SessionKey{WorkspaceID: "w", ArtifactID: "a", ApplicationID: "wordcraft"}
	changes := make([]BatchChange, 257)
	for i := range changes {
		changes[i] = testBatchChange(t, key, fmt.Sprintf("change_%d", i), "", testBatchOp(fmt.Sprintf("op_%d", i)))
	}
	if _, err := NormalizeSessionIngress(key, changes); !errors.Is(err, ErrInvalid) {
		t.Fatalf("257 operations admitted: %v", err)
	}
}

func TestSessionIngressPreservesCompleteMembershipWithoutAliases(t *testing.T) {
	key := SessionKey{WorkspaceID: "w", ArtifactID: "a", ApplicationID: "wordcraft"}
	structure := "0"
	a := testBatchOp("a")
	a.Payload = []byte(`{"value":"9007199254740993"}`)
	a.Reads = []BatchReadPrecondition{{TargetID: "p", Revision: "0", StructureRevision: &structure}}
	b := testBatchOp("b", "a")
	change := testBatchChange(t, key, "change", "task", a, b)
	normalized, err := NormalizeSessionIngress(key, []BatchChange{change})
	if err != nil {
		t.Fatal(err)
	}
	change.Operations[0].Payload[2] = 'X'
	change.Operations[0].Reads[0].TargetID = "other"
	structure = "9"
	change.Operations[1].Dependencies[0] = "other"
	got := normalized[0]
	if string(got.Operations[0].Payload) != `{"value":"9007199254740993"}` || got.Operations[0].Reads[0].TargetID != "p" || *got.Operations[0].Reads[0].StructureRevision != "0" || got.Operations[1].Dependencies[0] != "a" {
		t.Fatal("normalization retained caller aliases")
	}
	got.Operations = got.Operations[:1]
	if _, err = NormalizeSessionIngress(key, []BatchChange{got}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("partial atomic membership admitted: %v", err)
	}
	got = normalized[0]
	got.Operations[0].Participant = "forged"
	if _, err = NormalizeSessionIngress(key, []BatchChange{got}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("altered original author admitted: %v", err)
	}
}
