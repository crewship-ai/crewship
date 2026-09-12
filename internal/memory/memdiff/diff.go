package memdiff

import (
	"bytes"
	"fmt"
)

// OpKind discriminates the three edit-script operations. §8 has no "change"
// operation: "Přepsaný řádek je delete + insert."
type OpKind int

const (
	// OpEqual is a run of base lines carried into the target unchanged.
	OpEqual OpKind = iota
	// OpDelete is a run of base lines that the target does not contain.
	OpDelete
	// OpInsert is a run of target lines that the base does not contain.
	OpInsert
)

// String renders the kind for golden fixtures and error messages.
func (k OpKind) String() string {
	switch k {
	case OpEqual:
		return "equal"
	case OpDelete:
		return "delete"
	case OpInsert:
		return "insert"
	default:
		return fmt.Sprintf("OpKind(%d)", int(k))
	}
}

// Op is one run of the edit script.
//
// StartLine is always expressed in BASE coordinates, 1-based, because that is
// the coordinate system §8 fixes for removals.
//
//   - OpEqual, OpDelete: StartLine is the first base line of the run.
//   - OpInsert: StartLine is the base line the run is inserted AFTER, so 0
//     means "before the first base line". The anchor is a position in the base,
//     not a claim that the anchored line survives — a rewritten line yields
//     OpDelete{StartLine: n} followed by OpInsert{StartLine: n}, and base line
//     n is gone.
//
// Lines alias the base (equal, delete) or target (insert) slices passed to
// Diff. Callers must not mutate them.
type Op struct {
	Kind      OpKind
	StartLine int
	LineCount int
	Lines     [][]byte
}

// unset marks a diagonal that is not reachable at the current edit distance.
const unset = -1

// Diff returns the deterministic line-based shortest edit script that turns
// base into target, per §8: "Diff je deterministický line-based shortest edit
// script (Myers; při shodě preferovat delete před insert, sloučit sousední
// deletions)."
//
// Both inputs must already be Normalize'd and SplitLines'd.
//
// The implementation is Myers' O(ND) greedy algorithm with the classic
// tie-break, which is what produces §8's two required properties:
//
//   - Delete before insert. When a shortest script can be written either
//     delete-then-insert or insert-then-delete at the same position, the
//     forward pass reaches the shared endpoint one column further to the right
//     via the delete-first path, so the delete-first path wins. A rewritten
//     line therefore always comes out as OpDelete followed by OpInsert.
//   - Position in the base decides among repeated identical lines. The greedy
//     phase extends each snake as far as it can before spending an edit, so
//     the earliest matching occurrences in the base are the ones kept and the
//     deletions land as late in the base as a shortest script allows. See
//     TestDiffRepeatedLines and testdata/repeated_lines_abab.json.
//
// Runs of the same kind are coalesced, so adjacent deletions are one op.
//
// Diff never returns an error: any two line slices have a shortest edit script.
func Diff(base, target [][]byte) []Op {
	n, m := len(base), len(target)
	switch {
	case n == 0 && m == 0:
		return nil
	case n == 0:
		return []Op{{Kind: OpInsert, StartLine: 0, LineCount: m, Lines: target}}
	case m == 0:
		return []Op{{Kind: OpDelete, StartLine: 1, LineCount: n, Lines: base}}
	}

	maxD := n + m
	frontiers := make([]frontier, 0, 16)
	prev := frontier{d: -1}

	for d := 0; d <= maxD; d++ {
		cur := newFrontier(d)
		for k := -d; k <= d; k += 2 {
			// k = x-y, so only diagonals that can hold a point of the grid
			// are worth visiting.
			if k < -m || k > n {
				continue
			}
			x, _, ok := chooseStep(prev, k, d, n, m)
			if !ok {
				// Diagonal k is walled off at this distance. It stays unset,
				// which matters: a diagonal reachable at d-2 can be blocked at
				// d by the grid edges, and a stale value would be read as a
				// live frontier.
				continue
			}
			y := x - k
			for x < n && y < m && bytes.Equal(base[x], target[y]) {
				x++
				y++
			}
			cur.set(k, x)
			if x == n && y == m {
				frontiers = append(frontiers, cur)
				return backtrack(frontiers, base, target, d)
			}
		}
		frontiers = append(frontiers, cur)
		prev = cur
	}
	// Unreachable: deleting every base line and inserting every target line is
	// a script of length n+m, so the search always terminates by d == maxD.
	panic("memdiff: Myers search failed to reach the endpoint")
}

// frontier is Myers' V array at one edit distance, holding only the diagonals
// that distance can actually reach (k = -d, -d+2, ..., d). Keeping the whole
// 2*(n+m)+1 array per distance would make the recorded trace cost
// O(D*(n+m)) ints; this makes it O(D^2/2), which for a 4000-line wholesale
// rewrite is the difference between ~1 GiB and ~250 MiB. It is still
// quadratic — see the note on Diff's cost in the package docs.
type frontier struct {
	d int
	x []int
}

func newFrontier(d int) frontier {
	f := frontier{d: d, x: make([]int, d+1)}
	for i := range f.x {
		f.x[i] = unset
	}
	return f
}

// get returns the furthest x on diagonal k, or unset when k is out of this
// frontier's reach or was not reachable at this distance.
func (f frontier) get(k int) int {
	if f.d < 0 || k < -f.d || k > f.d || (k+f.d)%2 != 0 {
		return unset
	}
	return f.x[(k+f.d)/2]
}

func (f frontier) set(k, x int) { f.x[(k+f.d)/2] = x }

// chooseStep returns the furthest x on diagonal k reachable in exactly d moves
// (before extending the snake), whether the last move was a "down" move
// (insert) rather than a "right" move (delete), and whether diagonal k is
// reachable at all.
//
// prev is the frontier at d-1. A candidate move is only considered when it
// stays inside the grid: a down move must have a target line left to insert
// (y+1 <= m) and a right move a base line left to delete (x+1 <= n). The
// textbook formulation skips these checks and can park the frontier outside
// the grid, where "x >= n && y >= m" then reports a bogus endpoint.
//
// The tie-break is the classic one, and it is what §8's "při shodě preferovat
// delete před insert" buys: when both predecessors reach the same x, the right
// (delete) edge lands one column further and wins.
func chooseStep(prev frontier, k, d, n, m int) (x int, down bool, ok bool) {
	if d == 0 {
		return 0, false, true
	}
	downX, downOK := unset, false
	if p := prev.get(k + 1); p != unset && p-k <= m {
		downX, downOK = p, true
	}
	rightX, rightOK := unset, false
	if p := prev.get(k - 1); p != unset && p+1 <= n {
		rightX, rightOK = p+1, true
	}

	switch {
	case downOK && rightOK:
		if prev.get(k-1) < prev.get(k+1) {
			return downX, true, true
		}
		return rightX, false, true
	case downOK:
		return downX, true, true
	case rightOK:
		return rightX, false, true
	default:
		return unset, false, false
	}
}

// rawOp is a single-line move recovered from the Myers path, before coalescing.
type rawOp struct {
	kind OpKind
	x    int // base index: the line for equal/delete, the anchor for insert
	y    int // target index; only meaningful for insert
}

// backtrack walks the recorded frontiers from (n,m) back to (0,0), re-deriving
// each choice with chooseStep so it reconstructs exactly the path the forward
// pass took.
func backtrack(frontiers []frontier, base, target [][]byte, d int) []Op {
	n, m := len(base), len(target)
	x, y := n, m
	var rev []rawOp

	for ; d > 0; d-- {
		prev := frontiers[d-1]
		k := x - y
		_, down, ok := chooseStep(prev, k, d, n, m)
		if !ok {
			panic("memdiff: backtrack lost the Myers path")
		}
		prevK := k - 1
		if down {
			prevK = k + 1
		}
		prevX := prev.get(prevK)
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, rawOp{kind: OpEqual, x: x})
		}
		if down {
			rev = append(rev, rawOp{kind: OpInsert, x: prevX, y: prevY})
		} else {
			rev = append(rev, rawOp{kind: OpDelete, x: prevX})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, rawOp{kind: OpEqual, x: x})
	}

	return coalesce(rev, base, target)
}

// coalesce reverses the backtracked moves into forward order and merges runs
// of the same kind. Adjacent deletions become one op, which §8 requires
// ("sloučit sousední deletions") and which is what makes Removals emit one
// interval per contiguous deleted span.
func coalesce(rev []rawOp, base, target [][]byte) []Op {
	if len(rev) == 0 {
		return nil
	}
	var ops []Op
	var (
		kind   OpKind
		xStart int
		xEnd   int
		yStart int
		yEnd   int
		open   bool
	)
	flush := func() {
		if !open {
			return
		}
		switch kind {
		case OpEqual, OpDelete:
			ops = append(ops, Op{
				Kind:      kind,
				StartLine: xStart + 1,
				LineCount: xEnd - xStart + 1,
				Lines:     base[xStart : xEnd+1],
			})
		case OpInsert:
			ops = append(ops, Op{
				Kind:      kind,
				StartLine: xStart,
				LineCount: yEnd - yStart + 1,
				Lines:     target[yStart : yEnd+1],
			})
		}
		open = false
	}

	for i := len(rev) - 1; i >= 0; i-- {
		op := rev[i]
		mergeable := open && kind == op.kind
		if mergeable {
			switch kind {
			case OpEqual, OpDelete:
				mergeable = op.x == xEnd+1
			case OpInsert:
				mergeable = op.x == xStart && op.y == yEnd+1
			}
		}
		if mergeable {
			xEnd = op.x
			yEnd = op.y
			continue
		}
		flush()
		kind, xStart, xEnd, yStart, yEnd, open = op.kind, op.x, op.x, op.y, op.y, true
	}
	flush()
	return ops
}

// Apply replays an edit script against base and returns the reconstructed
// target. Equal runs are taken from base by line number rather than from
// Op.Lines, so a script whose StartLine is wrong cannot reproduce the target
// by accident — that is what makes Apply a real check in the property test.
func Apply(base [][]byte, ops []Op) ([][]byte, error) {
	out := make([][]byte, 0, len(base))
	for i, op := range ops {
		switch op.Kind {
		case OpEqual, OpDelete:
			if op.StartLine < 1 || op.LineCount < 1 || op.StartLine-1+op.LineCount > len(base) {
				return nil, fmt.Errorf("memdiff: op %d (%s) spans lines %d..%d outside a %d-line base",
					i, op.Kind, op.StartLine, op.StartLine+op.LineCount-1, len(base))
			}
			if op.Kind == OpEqual {
				out = append(out, base[op.StartLine-1:op.StartLine-1+op.LineCount]...)
			}
		case OpInsert:
			if op.StartLine < 0 || op.StartLine > len(base) {
				return nil, fmt.Errorf("memdiff: op %d (insert) anchored at base line %d outside a %d-line base",
					i, op.StartLine, len(base))
			}
			if op.LineCount != len(op.Lines) {
				return nil, fmt.Errorf("memdiff: op %d (insert) declares %d lines but carries %d",
					i, op.LineCount, len(op.Lines))
			}
			out = append(out, op.Lines...)
		default:
			return nil, fmt.Errorf("memdiff: op %d has unknown kind %d", i, int(op.Kind))
		}
	}
	return out, nil
}
