package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func originalTestChange(t *testing.T, c *collaborationHTTPConnection, id, base, dependency, update string) mooring.BatchChange {
	t.Helper()
	op := mooring.BatchOperation{ID: id + "_op", Participant: c.admission.participant, BaseRevision: base, Dependencies: []string{}, Reads: []mooring.BatchReadPrecondition{}}
	if dependency != "" {
		op.Dependencies = []string{dependency}
	}
	payload := collaborationHumanOriginalPayload{Kind: "humancrdt.update", OperationID: op.ID, Origin: c.origin, Update: update, AuthoredFrontier: "AA==", Targets: []string{"p"}, AuthoredCheckpointStateDigest: "sha256:" + strings.Repeat("a", 64)}
	op.Payload, _ = json.Marshal(payload)
	preimage := map[string]any{"domain": "mooring.atomic.v0.4", "scope": map[string]string{"workspace_id": c.document.key.WorkspaceID, "artifact_id": c.document.key.ArtifactID, "application_id": c.document.key.ApplicationID}, "change_id": id, "operations_without_fingerprints": []any{map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}}}
	raw, _ := json.Marshal(preimage)
	hashes, err := mooring.FingerprintAtomicJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	op.Fingerprint = hashes.Operations[op.ID]
	return mooring.BatchChange{ID: id, MembershipHash: hashes.MembershipHash, Operations: []mooring.BatchOperation{op}}
}

// RAM mechanics only: synthetic connection/observations and no authenticated
// capture or application qualification. reserve/finish cannot mint authority.
func originalSlotDataFixture() (*collaborationLiveOriginals, *collaborationHTTPConnection) {
	d := &collaborationHTTPDocument{key: mooring.SessionKey{WorkspaceID: "workspace", ArtifactID: "page", ApplicationID: "wordcraft"}}
	c := &collaborationHTTPConnection{document: d, origin: "17", ref: "connection", admission: collaborationHumanAdmission{participant: "human"}}
	l := newCollaborationLiveOriginals(d)
	d.originals = l
	return l, c
}

func TestMooringOriginalSlotsKeepBasisAndImmutableRetry(t *testing.T) {
	l, c := originalSlotDataFixture()
	zero := collaborationObservedCut{"0", "sha256:" + strings.Repeat("0", 64), "sha256:" + strings.Repeat("1", 64)}
	if err := l.observe(c, zero); err != nil {
		t.Fatal(err)
	}
	a := originalTestChange(t, c, "a", "0", "", "AQ==")
	all, fresh, err := l.reserve(c, []mooring.BatchChange{a})
	if err != nil || len(fresh) != 1 {
		t.Fatal(err)
	}
	l.finishCapture(fresh, nil)
	if err = l.waitCaptured(t.Context(), all); err != nil {
		t.Fatal(err)
	}
	ref, order := all[0].reference, all[0].order
	if err = l.observe(c, collaborationObservedCut{"1", "sha256:" + strings.Repeat("2", 64), ""}); err != nil {
		t.Fatal(err)
	}
	b := originalTestChange(t, c, "b", "0", "a_op", "Ag==")
	both, _, err := l.reserve(c, []mooring.BatchChange{a, b})
	if err != nil || both[0].reference != ref || both[0].order != order || both[1].basis != zero {
		t.Fatal("later observed peer refreshed original basis", err)
	}
	newer := originalTestChange(t, c, "newer", "1", "", "Aw==")
	next, _, err := l.reserve(c, []mooring.BatchChange{newer})
	if err != nil || next[0].basis.revision != "1" {
		t.Fatal("original explicit observed revision lost", err)
	}
	changed := originalTestChange(t, c, "a", "0", "", "BA==")
	if _, _, err = l.reserve(c, []mooring.BatchChange{changed}); err == nil {
		t.Fatal("original payload/digest refreshed")
	}
	a.Operations[0].Payload[0] = 'x'
	if both[0].change.Operations[0].Payload[0] != '{' {
		t.Fatal("caller packet aliases retained original")
	}
	other := &collaborationHTTPConnection{document: c.document, admission: c.admission, origin: c.origin}
	if err = l.observe(other, zero); err != nil {
		t.Fatal(err)
	}
	if _, _, err = l.reserve(other, []mooring.BatchChange{changed}); err == nil {
		t.Fatal("another connection adopted original identity")
	}
	l.retire(c)
	if _, _, err = l.reserve(c, []mooring.BatchChange{b}); err == nil {
		t.Fatal("retired source reused")
	}
	if err = l.observe(c, zero); err == nil {
		t.Fatal("retired source reobserved into life")
	}
}

func TestMooringOriginalSlotsBurnFailedCaptureAndBoundCapacity(t *testing.T) {
	for _, mode := range []string{"records", "bytes", "failed", "retire-before-observe"} {
		t.Run(mode, func(t *testing.T) {
			l, c := originalSlotDataFixture()
			zero := collaborationObservedCut{"0", "sha256:" + strings.Repeat("0", 64), ""}
			if mode == "retire-before-observe" {
				l.retire(c)
				if l.observe(c, zero) == nil {
					t.Fatal("late first observation reopened closed source")
				}
				return
			}
			if err := l.observe(c, zero); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "records":
				for i := 0; i < 256; i++ {
					change := originalTestChange(t, c, fmt.Sprintf("change%d", i), "0", "", "AQ==")
					if _, _, err := l.reserve(c, []mooring.BatchChange{change}); err != nil {
						t.Fatal(i, err)
					}
				}
				if _, _, err := l.reserve(c, []mooring.BatchChange{originalTestChange(t, c, "overflow", "0", "", "AQ==")}); !errors.Is(err, mooring.ErrBatchCapacity) {
					t.Fatal("record budget", err)
				}
			case "bytes":
				update := strings.Repeat("AAAA", 20000)
				if _, _, err := l.reserve(c, []mooring.BatchChange{originalTestChange(t, c, "large", "0", "", update)}); err != nil {
					t.Fatal(err)
				}
				before := l.bytes
				if _, _, err := l.reserve(c, []mooring.BatchChange{originalTestChange(t, c, "overflow", "0", "", update)}); !errors.Is(err, mooring.ErrBatchCapacity) || l.bytes != before {
					t.Fatal("byte budget changed on failed reservation", err)
				}
			case "failed":
				change := originalTestChange(t, c, "uncertain", "0", "", "AQ==")
				all, fresh, err := l.reserve(c, []mooring.BatchChange{change})
				if err != nil {
					t.Fatal(err)
				}
				l.finishCapture(fresh, errors.New("uncertain SQL publication"))
				if l.waitCaptured(t.Context(), all) == nil {
					t.Fatal("uncertain publication became a captured proof")
				}
				if _, _, err = l.reserve(c, []mooring.BatchChange{change}); err == nil {
					t.Fatal("uncertain original recaptured")
				}
			}
		})
	}
	var empty *collaborationLiveOriginals
	if _, err := empty.capture(t.Context(), nil, nil); err == nil {
		t.Fatal("missing authenticated capture accepted")
	}
}
