package appengine

import "testing"

// The rebase window is read and parsed before the writer transaction; inside
// it only revisions committed since then are scanned, so a stale edit over
// large commands does not hold the database write lock while parsing them.
func TestStaleEditScansHistoryOutsideTheWriterTransaction(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	for i := 0; i < 30; i++ {
		if _, err := s.Execute(t.Context(), k, bigEdit(i, "v"), a, e); err != nil {
			t.Fatal(err)
		}
	}
	var inWriter, outside int64
	s.windowScanned = func(writer bool, rows int64) {
		if writer {
			inWriter += rows
		} else {
			outside += rows
		}
	}
	late := Request{MutationID: "s2:1", MandateID: "human-import", SessionID: "s2", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "Z9", Input: "1"}}
	if r, err := s.Execute(t.Context(), k, late, a, e); err != nil || r.Rev != 32 {
		t.Fatalf("rebase: %+v %v", r, err)
	}
	if outside != 30 || inWriter != 0 {
		t.Fatalf("scanned outside=%d in writer=%d", outside, inWriter)
	}
}
