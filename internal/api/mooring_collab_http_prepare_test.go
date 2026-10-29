package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// DATA-only cursor mechanics. Actual qualification/lifetime comes exclusively
// from the active same-writer PreparedCandidates accessor at the call site.
func TestMooringPreparedPrefixAdvancesOnlyThisWriterCursor(t *testing.T) {
	_, c := originalSlotDataFixture()
	a := originalTestChange(t, c, "a", "0", "", "AQ==")
	b := originalTestChange(t, c, "b", "0", "", "Ag==")
	ap, _ := c.parseOriginalHumanChange(a)
	bp, _ := c.parseOriginalHumanChange(b)
	prior := collaborationOriginalView{source: c.ref, order: 1, change: a, payload: ap}
	current := collaborationOriginalView{source: c.ref, order: 2, change: b, payload: bp}
	views := map[string]collaborationOriginalView{a.ID: prior, b.ID: current}
	candidate := mooring.SessionPreparedCandidate{Change: a, Revision: "1", NativeDelta: protocol.SessionNativeDelta{WriterOrigin: "17", AllocationOrigins: []protocol.PositiveCounter{"17"}, Update: "AQ==", Targets: []protocol.Identifier{"p"}}}
	if next, err := collaborationNextUnsettled(current, views, nil); err != nil || next != 1 {
		t.Fatal("unprepared predecessor was skipped", next, err)
	}
	if next, err := collaborationNextUnsettled(current, views, []mooring.SessionPreparedCandidate{candidate}); err != nil || next != 2 {
		t.Fatal("exact same-writer prefix did not advance local cursor", next, err)
	}
	if views[a.ID].saved != nil {
		t.Fatal("provisional candidate labelled Saved")
	}
	if next, err := collaborationNextUnsettled(current, views, nil); err != nil || next != 1 {
		t.Fatal("local cursor escaped into another writer", next, err)
	}
	foreign := candidate
	foreign.NativeDelta.Update = "Aw=="
	if _, err := collaborationNextUnsettled(current, views, []mooring.SessionPreparedCandidate{foreign}); err == nil {
		t.Fatal("different raw prefix adopted")
	}
	foreign = candidate
	foreign.Change = b
	if _, err := collaborationNextUnsettled(current, views, []mooring.SessionPreparedCandidate{foreign}); err == nil {
		t.Fatal("current original pretended to be earlier prefix")
	}
}
