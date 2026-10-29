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

// An independent positive-path reachability oracle checks component identity
// across every directed four-vertex graph, including self-edges and disjoint
// cycles. It does not reuse Tarjan's traversal or stack bookkeeping.
func TestBatchCycleComponentsExhaustiveFourVertices(t *testing.T) {
	const n = 4
	for mask := 0; mask < 1<<(n*n); mask++ {
		edges := make([][]int, n)
		var reachable [n][n]bool
		for from := 0; from < n; from++ {
			for to := 0; to < n; to++ {
				if mask&(1<<(from*n+to)) != 0 {
					edges[from] = append(edges[from], to)
					reachable[from][to] = true
				}
			}
		}
		for via := 0; via < n; via++ {
			for from := 0; from < n; from++ {
				for to := 0; to < n; to++ {
					reachable[from][to] = reachable[from][to] || (reachable[from][via] && reachable[via][to])
				}
			}
		}
		components := batchCycleComponents(edges)
		for from := 0; from < n; from++ {
			component, cyclic := components[from]
			if cyclic != reachable[from][from] {
				t.Fatalf("graph%d vertex%d cycle=%v want%v", mask, from, cyclic, reachable[from][from])
			}
			if !cyclic {
				continue
			}
			for to := 0; to < n; to++ {
				other, otherCyclic := components[to]
				same := otherCyclic && component == other
				want := reachable[from][to] && reachable[to][from]
				if same != want {
					t.Fatalf("graph%d SCC(%d,%d)=%v want%v", mask, from, to, same, want)
				}
			}
		}
	}
}
