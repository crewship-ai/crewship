package memdiff

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// lines is the shorthand every diff test uses: normalize, then split.
func lines(t *testing.T, s string) [][]byte {
	t.Helper()
	norm, ls, err := NormalizeLines([]byte(s))
	if err != nil {
		t.Fatalf("NormalizeLines(%q): %v", s, err)
	}
	_ = norm
	return ls
}

// formatOps renders an edit script in a stable one-line-per-op form. It is the
// comparison surface for the table tests and the serialization the determinism
// test hashes.
func formatOps(ops []Op) string {
	var b strings.Builder
	for _, op := range ops {
		fmt.Fprintf(&b, "%s@%d+%d %q\n", op.Kind, op.StartLine, op.LineCount, asStrings(op.Lines))
	}
	return b.String()
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		target string
		want   []string // one "kind@startline+count content" entry per op
		why    string
	}{
		{
			name:   "both empty",
			base:   "",
			target: "",
			want:   nil,
		},
		{
			name:   "empty base is a pure insert anchored before line 1",
			base:   "",
			target: "a\nb\n",
			want:   []string{`insert@0+2 ["a\n" "b\n"]`},
		},
		{
			name:   "empty target deletes the whole base as one span",
			base:   "a\nb\n",
			target: "",
			want:   []string{`delete@1+2 ["a\n" "b\n"]`},
		},
		{
			name:   "identical",
			base:   "a\nb\n",
			target: "a\nb\n",
			want:   []string{`equal@1+2 ["a\n" "b\n"]`},
		},
		{
			// The append path. §8: append expressed as replace carries empty
			// removals, so a pure append must produce no delete op at all.
			name:   "pure append",
			base:   "a\nb\n",
			target: "a\nb\nc\nd\n",
			want: []string{
				`equal@1+2 ["a\n" "b\n"]`,
				`insert@2+2 ["c\n" "d\n"]`,
			},
			why: "insert is anchored after base line 2, the last base line",
		},
		{
			name:   "pure prepend",
			base:   "b\nc\n",
			target: "a\nb\nc\n",
			want: []string{
				`insert@0+1 ["a\n"]`,
				`equal@1+2 ["b\n" "c\n"]`,
			},
			why: "StartLine 0 means before the first base line",
		},
		{
			name:   "delete at the start",
			base:   "a\nb\nc\n",
			target: "b\nc\n",
			want: []string{
				`delete@1+1 ["a\n"]`,
				`equal@2+2 ["b\n" "c\n"]`,
			},
		},
		{
			name:   "delete at the end",
			base:   "a\nb\nc\n",
			target: "a\nb\n",
			want: []string{
				`equal@1+2 ["a\n" "b\n"]`,
				`delete@3+1 ["c\n"]`,
			},
		},
		{
			name:   "delete in the middle",
			base:   "a\nb\nc\n",
			target: "a\nc\n",
			want: []string{
				`equal@1+1 ["a\n"]`,
				`delete@2+1 ["b\n"]`,
				`equal@3+1 ["c\n"]`,
			},
		},
		{
			// §8: "sloučit sousední deletions". Three consecutive removed
			// lines are ONE op, not three.
			name:   "adjacent deletions merge into one op",
			base:   "a\nb\nc\nd\ne\n",
			target: "a\ne\n",
			want: []string{
				`equal@1+1 ["a\n"]`,
				`delete@2+3 ["b\n" "c\n" "d\n"]`,
				`equal@5+1 ["e\n"]`,
			},
		},
		{
			name:   "two separated deletions stay two ops",
			base:   "a\nb\nc\nd\ne\n",
			target: "a\nc\ne\n",
			want: []string{
				`equal@1+1 ["a\n"]`,
				`delete@2+1 ["b\n"]`,
				`equal@3+1 ["c\n"]`,
				`delete@4+1 ["d\n"]`,
				`equal@5+1 ["e\n"]`,
			},
		},
		{
			// §8: "Přepsaný řádek je delete + insert." Never one "change" op,
			// and the delete comes first.
			name:   "a rewritten line is delete then insert",
			base:   "a\nb\nc\n",
			target: "a\nx\nc\n",
			want: []string{
				`equal@1+1 ["a\n"]`,
				`delete@2+1 ["b\n"]`,
				`insert@2+1 ["x\n"]`,
				`equal@3+1 ["c\n"]`,
			},
			why: "the insert anchor is base line 2 even though line 2 is deleted; the anchor is a base position, not a surviving line",
		},
		{
			// The purest tie: a one-line file whose single line is replaced.
			// Both "delete then insert" and "insert then delete" are shortest
			// scripts of length 2. §8 picks delete first.
			name:   "single-line rewrite: delete wins the tie",
			base:   "old\n",
			target: "new\n",
			want: []string{
				`delete@1+1 ["old\n"]`,
				`insert@1+1 ["new\n"]`,
			},
		},
		{
			// A swap: keeping "b" forces deleting "a" first and re-inserting
			// it at the end. Both orders are length 2; delete leads.
			name:   "swap of two lines",
			base:   "a\nb\n",
			target: "b\na\n",
			want: []string{
				`delete@1+1 ["a\n"]`,
				`equal@2+1 ["b\n"]`,
				`insert@2+1 ["a\n"]`,
			},
		},
		{
			// One base line becomes two target lines: delete before both
			// inserts, and the inserts coalesce onto the same anchor.
			name:   "one line becomes two",
			base:   "a\n",
			target: "x\ny\n",
			want: []string{
				`delete@1+1 ["a\n"]`,
				`insert@1+2 ["x\n" "y\n"]`,
			},
		},
		{
			name:   "no common lines at all",
			base:   "a\nb\n",
			target: "x\ny\n",
			want: []string{
				`delete@1+2 ["a\n" "b\n"]`,
				`insert@2+2 ["x\n" "y\n"]`,
			},
			why: "deletions merge and lead; the inserts anchor after the last consumed base line",
		},
		{
			// Trailing-newline change. The last line's bytes differ ("c\n" vs
			// "c"), so it is a delete+insert, not an equal.
			name:   "dropping the trailing newline rewrites the last line",
			base:   "a\nb\nc\n",
			target: "a\nb\nc",
			want: []string{
				`equal@1+2 ["a\n" "b\n"]`,
				`delete@3+1 ["c\n"]`,
				`insert@3+1 ["c"]`,
			},
		},
		{
			name:   "adding a trailing newline rewrites the last line",
			base:   "a\nb\nc",
			target: "a\nb\nc\n",
			want: []string{
				`equal@1+2 ["a\n" "b\n"]`,
				`delete@3+1 ["c"]`,
				`insert@3+1 ["c\n"]`,
			},
		},
		{
			name:   "appending to a base without a trailing newline",
			base:   "a\nb",
			target: "a\nb\nc\n",
			want: []string{
				`equal@1+1 ["a\n"]`,
				`delete@2+1 ["b"]`,
				`insert@2+2 ["b\n" "c\n"]`,
			},
			why: "appending to an unterminated file necessarily rewrites its last line; this is why append must not silently add the LF",
		},
		{
			name:   "blank lines are ordinary lines",
			base:   "a\n\n\nb\n",
			target: "a\n\nb\n",
			want: []string{
				`equal@1+2 ["a\n" "\n"]`,
				`delete@3+1 ["\n"]`,
				`equal@4+1 ["b\n"]`,
			},
			why: "of the two identical blank lines the later one is deleted; the greedy snake keeps the earlier",
		},
		{
			name:   "unicode lines",
			base:   "kůň\n🚀\nüber\n",
			target: "kůň\nüber\n",
			want: []string{
				`equal@1+1 ["kůň\n"]`,
				`delete@2+1 ["🚀\n"]`,
				`equal@3+1 ["über\n"]`,
			},
		},
		{
			name:   "a bare CR inside a line is part of that line's identity",
			base:   "a\rb\nc\n",
			target: "ab\nc\n",
			want: []string{
				`delete@1+1 ["a\rb\n"]`,
				`insert@1+1 ["ab\n"]`,
				`equal@2+1 ["c\n"]`,
			},
			why: "\"a\\rb\\n\" and \"ab\\n\" are different lines, so this is a rewrite of line 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, target := lines(t, tc.base), lines(t, tc.target)
			ops := Diff(base, target)
			got := strings.Split(strings.TrimSuffix(formatOps(ops), "\n"), "\n")
			if len(ops) == 0 {
				got = nil
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Diff produced %d ops, want %d\n got: %s\nwant: %s",
					len(got), len(tc.want), strings.Join(got, " | "), strings.Join(tc.want, " | "))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("op %d:\n got: %s\nwant: %s", i, got[i], tc.want[i])
				}
			}
			if tc.why != "" && t.Failed() {
				t.Logf("why: %s", tc.why)
			}

			// Every script must reproduce the target, and must be shortest.
			assertScript(t, base, target, ops)
		})
	}
}

// assertScript checks the two properties that make an edit script correct: it
// replays to the target, and it is minimal (edit count == n + m - 2*LCS).
func assertScript(t *testing.T, base, target [][]byte, ops []Op) {
	t.Helper()
	got, err := Apply(base, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !equalLines(got, target) {
		t.Fatalf("Apply(base, Diff(base,target)) = %q, want %q", asStrings(got), asStrings(target))
	}
	edits := 0
	for _, op := range ops {
		if op.Kind != OpEqual {
			edits += op.LineCount
		}
	}
	if want := len(base) + len(target) - 2*lcsLen(base, target); edits != want {
		t.Fatalf("edit script has %d edits, shortest is %d", edits, want)
	}
	// Op.Lines must agree with the line numbers it claims.
	for i, op := range ops {
		if op.LineCount != len(op.Lines) {
			t.Fatalf("op %d: LineCount %d but %d Lines", i, op.LineCount, len(op.Lines))
		}
		if op.Kind == OpEqual || op.Kind == OpDelete {
			for j := range op.Lines {
				if !bytes.Equal(op.Lines[j], base[op.StartLine-1+j]) {
					t.Fatalf("op %d line %d: %q is not base line %d (%q)",
						i, j, op.Lines[j], op.StartLine+j, base[op.StartLine-1+j])
				}
			}
		}
	}
}

// lcsLen is a plain O(nm) DP, deliberately unrelated to the Myers code, so a
// bug in Diff cannot hide behind a shared helper.
func lcsLen(a, b [][]byte) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if bytes.Equal(a[i-1], b[j-1]) {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func equalLines(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// TestDiffNeverEmitsInsertBeforeDelete pins §8's "při shodě preferovat delete
// před insert" as a structural property of every script this package emits:
// an insert is never immediately followed by a delete. Where both orders are
// shortest, Myers' tie-break reaches the shared endpoint one column further to
// the right through the delete-first path, so delete-first always wins.
func TestDiffNeverEmitsInsertBeforeDelete(t *testing.T) {
	cases := [][2]string{
		{"old\n", "new\n"},
		{"a\nb\nc\n", "a\nx\nc\n"},
		{"a\n", "x\ny\n"},
		{"x\ny\n", "a\n"},
		{"a\nb\n", "x\ny\n"},
		{"a\nb\nc\nd\n", "w\nx\ny\nz\n"},
		{"a\nb\n", "b\na\n"},
		{"a\nb\nc\n", "c\nb\na\n"},
		{"a\nb\na\nb\na\n", "b\na\nb\n"},
	}
	for _, tc := range cases {
		base, target := lines(t, tc[0]), lines(t, tc[1])
		ops := Diff(base, target)
		for i := 1; i < len(ops); i++ {
			if ops[i-1].Kind == OpInsert && ops[i].Kind == OpDelete {
				t.Errorf("Diff(%q, %q) put an insert before a delete:\n%s", tc[0], tc[1], formatOps(ops))
				break
			}
		}
		assertScript(t, base, target, ops)
	}
}

// TestDiffRepeatedLines is the §8 "Opakované řádky určuje jejich pozice v
// původní revizi" case, pinned in prose here and in
// testdata/repeated_lines_abab.json as a golden file.
//
// base   a b a b a   (lines 1..5)
// target a b a
//
// Three different two-line deletions produce that target: {2,3}, {3,4} and
// {4,5}. All are shortest edit scripts. The answer is {4,5} — delete the LAST
// two lines — and it is deterministic for a structural reason, not by luck:
// Myers' greedy phase extends the diagonal as far as it can before spending an
// edit, so at d=0 it consumes base lines 1,2,3 against the whole of the target
// and the frontier is already at (3,3). From there the only way to reach (5,3)
// is two right (delete) moves, so the deletions are forced to the tail of the
// base and merge into one span.
//
// Read the rule as: among identical candidate lines, the EARLIEST occurrences
// in the base are the ones kept.
func TestDiffRepeatedLines(t *testing.T) {
	base := lines(t, "a\nb\na\nb\na\n")
	target := lines(t, "a\nb\na\n")

	got := formatOps(Diff(base, target))
	want := "equal@1+3 [\"a\\n\" \"b\\n\" \"a\\n\"]\ndelete@4+2 [\"b\\n\" \"a\\n\"]\n"
	if got != want {
		t.Fatalf("repeated-lines diff:\n got:\n%s\nwant:\n%s", got, want)
	}

	rem := Removals(base, target)
	if len(rem) != 1 || rem[0].StartLine != 4 || rem[0].LineCount != 2 {
		t.Fatalf("removals = %+v, want one span {4,2}", rem)
	}

	// The mirror case: dropping the leading occurrences is what the algorithm
	// does NOT do. Assert the alternative explicitly so a future change to the
	// tie-break cannot slip through silently.
	for _, alt := range [][2]int{{2, 2}, {3, 2}} {
		h, err := HashSpan(base, alt[0], alt[1])
		if err != nil {
			t.Fatalf("HashSpan: %v", err)
		}
		alternative := []Removal{{StartLine: alt[0], LineCount: alt[1], OldSHA256: h}}
		// It reproduces the same target, but it is not the reference answer,
		// so VerifyRemovals must reject it.
		if err := VerifyRemovals(base, target, alternative); err == nil {
			t.Fatalf("VerifyRemovals accepted the alternative span %v; the reference answer must be unique", alt)
		}
	}
}

// TestDiffCRLFEquivalence is §8's normalization requirement seen from the diff
// side: CRLF input must produce results identical to its LF equivalent, down
// to the removal hashes.
func TestDiffCRLFEquivalence(t *testing.T) {
	cases := [][2]string{
		{"a\r\nb\r\nc\r\n", "a\r\nc\r\n"},
		{"a\r\nb\r\n", "a\r\nb\r\nc\r\n"},
		{"old\r\n", "new\r\n"},
		{"a\r\nb\r\na\r\nb\r\na\r\n", "a\r\nb\r\na\r\n"},
	}
	for _, tc := range cases {
		crBase, crTarget := lines(t, tc[0]), lines(t, tc[1])
		lfBase := lines(t, strings.ReplaceAll(tc[0], "\r\n", "\n"))
		lfTarget := lines(t, strings.ReplaceAll(tc[1], "\r\n", "\n"))

		if got, want := formatOps(Diff(crBase, crTarget)), formatOps(Diff(lfBase, lfTarget)); got != want {
			t.Errorf("CRLF diff differs from LF diff for %q -> %q:\n got:\n%s\nwant:\n%s", tc[0], tc[1], got, want)
		}
		crRem, lfRem := Removals(crBase, crTarget), Removals(lfBase, lfTarget)
		if fmt.Sprint(crRem) != fmt.Sprint(lfRem) {
			t.Errorf("CRLF removals differ from LF removals for %q -> %q:\n got: %+v\nwant: %+v", tc[0], tc[1], crRem, lfRem)
		}
	}
}
