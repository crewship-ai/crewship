package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// RAM/DATA mechanics only. These fixtures deliberately construct no admission,
// native application proof, SQL COMMIT or current recipient authority.
func originalCommittedDataFixture(t *testing.T) (*collaborationLiveOriginals, *collaborationOriginalGroup, []mooring.BatchChange, protocol.SessionCommitResult, []mooring.SessionSavedCut, []mooring.SessionCommittedNativeDelta) {
	t.Helper()
	l, c := originalSlotDataFixture()
	if err := l.observe(c, collaborationObservedCut{"0", "sha256:" + strings.Repeat("0", 64), ""}); err != nil {
		t.Fatal(err)
	}
	change := originalTestChange(t, c, "a", "0", "", "AQ==")
	slots, fresh, err := l.reserve(c, []mooring.BatchChange{change})
	if err != nil {
		t.Fatal(err)
	}
	l.finishCapture(fresh, nil)
	change = slots[0].change
	group := &collaborationOriginalGroup{source: slots[0].source, requestID: "original-public", outerID: "original-outer", changes: []string{change.ID}, captured: true, sent: true}
	slots[0].group = group
	l.groups[c.ref+"\x00"+string(group.requestID)] = group
	revision := protocol.Counter("1")
	op, _ := json.Marshal(protocol.OperationResultVariant1{OpID: protocol.Identifier(change.Operations[0].ID), Status: "saved", Revision: revision, Issuer: "crewship", Durability: "node"})
	receipt := protocol.CommitResult{Type: "commit_result", RequestID: "private-actor", Scope: protocol.Scope{WorkspaceID: "workspace", ArtifactID: "page", ApplicationID: "wordcraft"}, Generation: "1", Revision: revision, Issuer: "crewship", Durability: "node", Changes: []protocol.ChangeResult{{ChangeID: protocol.Identifier(change.ID), Status: "saved", FirstRevision: &revision, LastRevision: &revision, Operations: []protocol.OperationResult{op}}}}
	public, err := l.preparePublicReceipt(group, receipt)
	if err != nil {
		t.Fatal(err)
	}
	cut := mooring.SessionSavedCut{ChangeID: change.ID, MembershipHash: change.MembershipHash, Revision: "1", StateDigest: "sha256:" + strings.Repeat("b", 64)}
	delta := protocol.SessionNativeDelta{WriterOrigin: "17", AllocationOrigins: []protocol.PositiveCounter{"17"}, Update: "AQ==", Targets: []protocol.Identifier{"p"}}
	actual := []mooring.SessionCommittedNativeDelta{{ChangeID: change.ID, MembershipHash: change.MembershipHash, Revision: "1", StateDigest: cut.StateDigest, NativeDelta: delta}}
	wire := []protocol.SessionCommittedNativeDelta{{ChangeID: protocol.Identifier(change.ID), MembershipHash: protocol.Digest(change.MembershipHash), Revision: "1", CheckpointStateDigest: protocol.Digest(cut.StateDigest), NativeDelta: delta}}
	return l, group, []mooring.BatchChange{change}, protocol.SessionCommitResult{Receipt: receipt, PublicReceipt: public, NativeDeltas: &wire}, []mooring.SessionSavedCut{cut}, actual
}

func TestMooringOriginalCommittedPublicationIsSeparateFromPending(t *testing.T) {
	l, g, changes, result, cuts, deltas := originalCommittedDataFixture(t)
	if g.receipt != nil || g.privateReceipt != nil || l.changes[changes[0].ID].saved != nil {
		t.Fatal("output preparation published Saved")
	}
	if result.PublicReceipt.RequestID != "original-public" || result.Receipt.RequestID != "private-actor" {
		t.Fatal("receipt request relabelled")
	}
	if err := l.publishCommitted(g, changes, result, cuts, deltas); err != nil {
		t.Fatal(err)
	}
	slot := l.changes[changes[0].ID]
	if slot.saved == nil || slot.saved.cut != cuts[0] || g.receipt.RequestID != "original-public" || g.privateReceipt.RequestID != "private-actor" {
		t.Fatal("actual publication lost immutable correlation")
	}
	deltas[0].NativeDelta.Targets[0] = "caller-mutated"
	if slot.saved.delta.Targets[0] != "p" {
		t.Fatal("retained history aliases caller delta")
	}
}

func TestMooringOriginalCommittedUncertainOrForeignDataPublishesNothing(t *testing.T) {
	for _, mode := range []string{"missing-native", "foreign-membership", "foreign-cut", "legacy-saved-gap"} {
		t.Run(mode, func(t *testing.T) {
			l, g, changes, result, cuts, deltas := originalCommittedDataFixture(t)
			switch mode {
			case "missing-native":
				result.NativeDeltas = nil
			case "foreign-membership":
				deltas[0].MembershipHash = "sha256:" + strings.Repeat("c", 64)
			case "foreign-cut":
				cuts[0].StateDigest = "sha256:" + strings.Repeat("d", 64)
			case "legacy-saved-gap":
				empty := []protocol.SessionCommittedNativeDelta{}
				result.NativeDeltas = &empty
				deltas = nil
			}
			if err := l.publishCommitted(g, changes, result, cuts, deltas); err == nil {
				t.Fatal("unqualified history accepted")
			}
			if g.receipt != nil || g.privateReceipt != nil || l.changes[changes[0].ID].saved != nil {
				t.Fatal("failure published partial Saved")
			}
		})
	}
}

func TestMooringOriginalGroupCannotSplitOrAdoptAnotherRequest(t *testing.T) {
	l, g, changes, _, _, _ := originalCommittedDataFixture(t)
	if _, err := l.boundPublicGroup(changes); err != nil {
		t.Fatal(err)
	}
	foreign := changes[0]
	foreign.Operations = append([]mooring.BatchOperation{}, foreign.Operations...)
	foreign.Operations[0].BaseRevision = "1"
	if _, err := l.boundPublicGroup([]mooring.BatchChange{foreign}); err == nil {
		t.Fatal("rewritten original adopted")
	}
	g.changes = append(g.changes, "not-in-original-emission")
	if _, err := l.boundPublicGroup(changes); err == nil {
		t.Fatal("split group accepted")
	}
}

func TestMooringPeerOutputNamespaceIncludesExactRecipient(t *testing.T) {
	first, err := collaborationPeerOutputID("17", "connection", "participant", "gate")
	if err != nil || first != "actor-update:17:sha256:8c316647f8d15ca2bc8b5d43e99eb75c8b4be0b0d72408ad3c66af7977d16da2" {
		t.Fatal("recipient tuple digest", first, err)
	}
	for _, parts := range [][3]string{{"other", "participant", "gate"}, {"connection", "other", "gate"}, {"connection", "participant", "other"}} {
		id, err := collaborationPeerOutputID("17", parts[0], parts[1], parts[2])
		if err != nil || id == first {
			t.Fatal("private recipient namespace collided", err)
		}
	}
	for _, revision := range []string{"0", "01", "-1", "18446744073709551616"} {
		if _, err := collaborationPeerOutputID(revision, "connection", "participant", "gate"); err == nil {
			t.Fatal("invalid cut accepted")
		}
	}
}
