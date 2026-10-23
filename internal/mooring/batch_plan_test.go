package mooring

import "testing"

func TestBatchCycleMembershipSCCNotDFSBackedge(t *testing.T) {
	edges := [][]int{{1, 2}, {0}, {1}, {2}, {}, {5}}
	actual := batchCycles(edges)
	for index, want := range []bool{true, true, true, false, false, true} {
		if actual[index] != want {
			t.Fatalf("vertex%d cycle=%v want%v", index, actual[index], want)
		}
	}
}
