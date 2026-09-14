package memdiff

import (
	"errors"
	"testing"
	"unicode/utf8"
)

// FuzzNormalize checks the two hard rules on arbitrary bytes: invalid UTF-8 is
// rejected, and valid UTF-8 survives with its trailing newline exactly as it
// arrived.
func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"", "a", "a\n", "a\r\n", "a\r\r\n", "a\rb", "kůň\r\n🚀", "\x80", "\xed\xa0\x80"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out, err := Normalize(in)
		if !utf8.Valid(in) {
			if !errors.Is(err, ErrInvalidUTF8) {
				t.Fatalf("Normalize(%q) on invalid UTF-8 = %v, want ErrInvalidUTF8", in, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Normalize(%q): %v", in, err)
		}
		if !utf8.Valid(out) {
			t.Fatalf("Normalize turned valid UTF-8 %q into invalid %q", in, out)
		}
		endsLF := func(b []byte) bool { return len(b) > 0 && b[len(b)-1] == '\n' }
		if endsLF(in) != endsLF(out) {
			t.Fatalf("Normalize changed whether %q ends in a newline: %q", in, out)
		}
		if len(in) == 0 && len(out) != 0 {
			t.Fatalf("Normalize(empty) = %q", out)
		}
		// Splitting is lossless.
		if got := joinLines(SplitLines(out)); got != string(out) {
			t.Fatalf("SplitLines is lossy for %q: %q", out, got)
		}
	})
}

// FuzzDiff runs the whole contract on arbitrary line-shaped input: the script
// replays to the target, it is minimal, and the removals it implies verify.
func FuzzDiff(f *testing.F) {
	seeds := [][2]string{
		{"", ""},
		{"a\n", ""},
		{"", "a\n"},
		{"a\nb\nc\n", "a\nc\n"},
		{"a\nb\na\nb\na\n", "a\nb\na\n"},
		{"old\n", "new\n"},
		{"a\nb", "a\nb\nc\n"},
		{"a\r\nb\r\n", "a\r\n"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, baseText, targetText string) {
		// Not a skip: rejecting an uninteresting input inside f.Fuzz is a
		// plain return. Skipping here would spend the repo's skip budget
		// (scripts/skip-budget.txt) on something that is not a guarded test.
		if len(baseText) > 4096 || len(targetText) > 4096 {
			return
		}
		normBase, base, err := NormalizeLines([]byte(baseText))
		if err != nil {
			return // invalid UTF-8 base: Normalize's own table test covers this
		}
		_, target, err := NormalizeLines([]byte(targetText))
		if err != nil {
			return // invalid UTF-8 target
		}
		_ = normBase

		ops := Diff(base, target)
		applied, err := Apply(base, ops)
		if err != nil {
			t.Fatalf("Apply: %v (base=%q target=%q)", err, baseText, targetText)
		}
		if !equalLines(applied, target) {
			t.Fatalf("replay mismatch: base=%q target=%q got=%q\nops:\n%s",
				baseText, targetText, asStrings(applied), formatOps(ops))
		}
		edits := 0
		for _, op := range ops {
			if op.Kind != OpEqual {
				edits += op.LineCount
			}
		}
		if want := len(base) + len(target) - 2*lcsLen(base, target); edits != want {
			t.Fatalf("script has %d edits, shortest is %d (base=%q target=%q)", edits, want, baseText, targetText)
		}
		for i := 1; i < len(ops); i++ {
			if ops[i-1].Kind == OpInsert && ops[i].Kind == OpDelete {
				t.Fatalf("insert precedes delete: base=%q target=%q\nops:\n%s", baseText, targetText, formatOps(ops))
			}
		}
		rem := Removals(base, target)
		assertRemovalInvariants(t, base, rem)
		if err := VerifyRemovals(base, target, rem); err != nil {
			t.Fatalf("VerifyRemovals rejected its own derivation: %v (base=%q target=%q)", err, baseText, targetText)
		}
	})
}
