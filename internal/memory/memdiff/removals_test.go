package memdiff

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// shaOf is sha under the name the golden tests use.
func shaOf(s string) string { return sha(s) }

func TestHashSpan(t *testing.T) {
	base := lines(t, "alpha\nbeta\ngamma\n")

	tests := []struct {
		name      string
		startLine int
		lineCount int
		want      string
		wantErr   error
	}{
		{name: "first line", startLine: 1, lineCount: 1, want: sha("alpha\n")},
		{name: "middle line", startLine: 2, lineCount: 1, want: sha("beta\n")},
		{name: "last line", startLine: 3, lineCount: 1, want: sha("gamma\n")},
		{
			// The hash covers the concatenated bytes, LFs included — not a
			// hash of hashes and not a per-line join with a separator.
			name: "multi-line span", startLine: 1, lineCount: 3, want: sha("alpha\nbeta\ngamma\n"),
		},
		{name: "start_line 0 is not 1-based", startLine: 0, lineCount: 1, wantErr: ErrInvalidSpan},
		{name: "negative start_line", startLine: -1, lineCount: 1, wantErr: ErrInvalidSpan},
		{name: "zero line_count", startLine: 1, lineCount: 0, wantErr: ErrInvalidSpan},
		{name: "negative line_count", startLine: 1, lineCount: -2, wantErr: ErrInvalidSpan},
		{name: "past the end", startLine: 3, lineCount: 2, wantErr: ErrSpanOutOfRange},
		{name: "entirely past the end", startLine: 9, lineCount: 1, wantErr: ErrSpanOutOfRange},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HashSpan(base, tc.startLine, tc.lineCount)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("HashSpan(%d,%d) error = %v, want %v", tc.startLine, tc.lineCount, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("HashSpan(%d,%d): %v", tc.startLine, tc.lineCount, err)
			}
			if got != tc.want {
				t.Fatalf("HashSpan(%d,%d) = %s, want %s", tc.startLine, tc.lineCount, got, tc.want)
			}
		})
	}
}

// TestHashSpanTrailingNewline pins §8's "poslední řádek LF nemít může": the
// same visible last line hashes differently depending on whether the base file
// ends in a newline. Both values are pinned as literals so a change to the
// hashing rule shows up as a diff in this file.
func TestHashSpanTrailingNewline(t *testing.T) {
	terminated := lines(t, "a\nb\n")
	unterminated := lines(t, "a\nb")

	withLF, err := HashSpan(terminated, 2, 1)
	if err != nil {
		t.Fatalf("HashSpan: %v", err)
	}
	withoutLF, err := HashSpan(unterminated, 2, 1)
	if err != nil {
		t.Fatalf("HashSpan: %v", err)
	}

	const (
		// sha256("b\n")
		wantWithLF = "0263829989b6fd954f72baaf2fc64bc2e2f01d692d4de72986ea808f6e99813f"
		// sha256("b")
		wantWithoutLF = "3e23e8160039594a33894f6564e1b1348bbd7a0088d42c4acb73eeaed59c009d"
	)
	if withLF != wantWithLF {
		t.Errorf("hash of terminated last line = %s, want %s", withLF, wantWithLF)
	}
	if withoutLF != wantWithoutLF {
		t.Errorf("hash of unterminated last line = %s, want %s", withoutLF, wantWithoutLF)
	}
	if withLF == withoutLF {
		t.Fatal("terminated and unterminated last lines must not hash the same")
	}

	// And the whole-file span, for the same reason.
	allTerm, _ := HashSpan(terminated, 1, 2)
	allUnterm, _ := HashSpan(unterminated, 1, 2)
	if allTerm != sha("a\nb\n") || allUnterm != sha("a\nb") {
		t.Fatalf("whole-file spans: %s / %s", allTerm, allUnterm)
	}
}

func TestRemovals(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		target string
		want   []Removal
	}{
		{
			// The append case. §8 requires empty removals; nil is how that is
			// spelled in Go and how it marshals as [] once the API wraps it.
			name: "pure append removes nothing", base: "a\nb\n", target: "a\nb\nc\n", want: nil,
		},
		{name: "identical removes nothing", base: "a\n", target: "a\n", want: nil},
		{name: "empty base removes nothing", base: "", target: "a\n", want: nil},
		{
			name: "empty target removes everything as one span",
			base: "a\nb\nc\n", target: "",
			want: []Removal{{StartLine: 1, LineCount: 3, OldSHA256: sha("a\nb\nc\n")}},
		},
		{
			name: "delete at the start", base: "a\nb\nc\n", target: "b\nc\n",
			want: []Removal{{StartLine: 1, LineCount: 1, OldSHA256: sha("a\n")}},
		},
		{
			name: "delete at the end", base: "a\nb\nc\n", target: "a\nb\n",
			want: []Removal{{StartLine: 3, LineCount: 1, OldSHA256: sha("c\n")}},
		},
		{
			name: "delete in the middle", base: "a\nb\nc\n", target: "a\nc\n",
			want: []Removal{{StartLine: 2, LineCount: 1, OldSHA256: sha("b\n")}},
		},
		{
			name: "adjacent deletions are one removal", base: "a\nb\nc\nd\ne\n", target: "a\ne\n",
			want: []Removal{{StartLine: 2, LineCount: 3, OldSHA256: sha("b\nc\nd\n")}},
		},
		{
			name: "separated deletions are two removals", base: "a\nb\nc\nd\ne\n", target: "a\nc\ne\n",
			want: []Removal{
				{StartLine: 2, LineCount: 1, OldSHA256: sha("b\n")},
				{StartLine: 4, LineCount: 1, OldSHA256: sha("d\n")},
			},
		},
		{
			name: "a rewrite removes the old line", base: "a\nb\nc\n", target: "a\nx\nc\n",
			want: []Removal{{StartLine: 2, LineCount: 1, OldSHA256: sha("b\n")}},
		},
		{
			name: "repeated lines: the trailing occurrences go", base: "a\nb\na\nb\na\n", target: "a\nb\na\n",
			want: []Removal{{StartLine: 4, LineCount: 2, OldSHA256: sha("b\na\n")}},
		},
		{
			name: "unterminated last line hashes without its LF", base: "a\nb", target: "a\n",
			want: []Removal{{StartLine: 2, LineCount: 1, OldSHA256: sha("b")}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, target := lines(t, tc.base), lines(t, tc.target)
			got := Removals(base, target)
			if len(got) != len(tc.want) {
				t.Fatalf("Removals = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("removal %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
			// What we derive must always verify.
			if err := VerifyRemovals(base, target, got); err != nil {
				t.Fatalf("VerifyRemovals rejected our own derivation: %v", err)
			}
			assertRemovalInvariants(t, base, got)
		})
	}
}

// assertRemovalInvariants checks the shape §8 mandates: 1-based, inside the
// base, ascending, non-intersecting — and, because §8 requires adjacent
// deletions to be merged, never merely touching either.
func assertRemovalInvariants(t *testing.T, base [][]byte, spans []Removal) {
	t.Helper()
	for i, s := range spans {
		if s.StartLine < 1 {
			t.Fatalf("removal %d: start_line %d is not 1-based", i, s.StartLine)
		}
		if s.LineCount < 1 {
			t.Fatalf("removal %d: line_count %d", i, s.LineCount)
		}
		if s.EndLine() > len(base) {
			t.Fatalf("removal %d ends at %d past a %d-line base", i, s.EndLine(), len(base))
		}
		if i > 0 && s.StartLine <= spans[i-1].EndLine()+1 {
			t.Fatalf("removal %d (%d..%d) is not strictly after removal %d (%d..%d); adjacent spans must be merged",
				i, s.StartLine, s.EndLine(), i-1, spans[i-1].StartLine, spans[i-1].EndLine())
		}
	}
}

func TestVerifyRemovals(t *testing.T) {
	// base: a b c d e ; target keeps a, c, e -> the diff removes b (2) and d (4).
	const baseText = "a\nb\nc\nd\ne\n"
	const targetText = "a\nc\ne\n"
	base, target := lines(t, baseText), lines(t, targetText)

	hb, db := sha("b\n"), sha("d\n")

	tests := []struct {
		name     string
		declared []Removal
		wantErr  error
		wantCode string
	}{
		{
			name: "exact match passes",
			declared: []Removal{
				{StartLine: 2, LineCount: 1, OldSHA256: hb},
				{StartLine: 4, LineCount: 1, OldSHA256: db},
			},
		},
		{
			name: "uppercase hex is accepted",
			declared: []Removal{
				{StartLine: 2, LineCount: 1, OldSHA256: strings.ToUpper(hb)},
				{StartLine: 4, LineCount: 1, OldSHA256: db},
			},
		},
		{
			name: "declared out of order still passes",
			declared: []Removal{
				{StartLine: 4, LineCount: 1, OldSHA256: db},
				{StartLine: 2, LineCount: 1, OldSHA256: hb},
			},
		},
		{
			name:     "missing declaration is an undeclared removal",
			declared: []Removal{{StartLine: 2, LineCount: 1, OldSHA256: hb}},
			wantErr:  ErrUndeclaredRemoval,
			wantCode: "undeclared_removal",
		},
		{
			name:     "no declarations at all is an undeclared removal",
			declared: nil,
			wantErr:  ErrUndeclaredRemoval,
			wantCode: "undeclared_removal",
		},
		{
			name: "extra declaration for a line the diff keeps",
			declared: []Removal{
				{StartLine: 2, LineCount: 1, OldSHA256: hb},
				{StartLine: 3, LineCount: 1, OldSHA256: sha("c\n")},
				{StartLine: 4, LineCount: 1, OldSHA256: db},
			},
			wantErr:  ErrSpuriousRemoval,
			wantCode: "spurious_removal",
		},
		{
			name: "wrong hash is distinguishable from a wrong span",
			declared: []Removal{
				{StartLine: 2, LineCount: 1, OldSHA256: sha("not the line\n")},
				{StartLine: 4, LineCount: 1, OldSHA256: db},
			},
			wantErr:  ErrHashMismatch,
			wantCode: "hash_mismatch",
		},
		{
			name: "empty hash is a mismatch, not a skip",
			declared: []Removal{
				{StartLine: 2, LineCount: 1},
				{StartLine: 4, LineCount: 1, OldSHA256: db},
			},
			wantErr:  ErrHashMismatch,
			wantCode: "hash_mismatch",
		},
		{
			name:     "start_line 0 breaks 1-based numbering",
			declared: []Removal{{StartLine: 0, LineCount: 2, OldSHA256: hb}},
			wantErr:  ErrInvalidSpan,
			wantCode: "invalid_span",
		},
		{
			name:     "zero line_count declares nothing",
			declared: []Removal{{StartLine: 2, LineCount: 0, OldSHA256: hb}},
			wantErr:  ErrInvalidSpan,
			wantCode: "invalid_span",
		},
		{
			name:     "span runs past the end of the base",
			declared: []Removal{{StartLine: 5, LineCount: 3, OldSHA256: hb}},
			wantErr:  ErrSpanOutOfRange,
			wantCode: "span_out_of_range",
		},
		{
			name: "overlapping spans",
			declared: []Removal{
				{StartLine: 2, LineCount: 2, OldSHA256: sha("b\nc\n")},
				{StartLine: 3, LineCount: 2, OldSHA256: sha("c\nd\n")},
			},
			wantErr:  ErrOverlappingSpans,
			wantCode: "overlapping_spans",
		},
		{
			name: "overlap is detected regardless of declared order",
			declared: []Removal{
				{StartLine: 3, LineCount: 2, OldSHA256: sha("c\nd\n")},
				{StartLine: 2, LineCount: 2, OldSHA256: sha("b\nc\n")},
			},
			wantErr:  ErrOverlappingSpans,
			wantCode: "overlapping_spans",
		},
		{
			name: "a duplicated declaration overlaps itself",
			declared: []Removal{
				{StartLine: 2, LineCount: 1, OldSHA256: hb},
				{StartLine: 2, LineCount: 1, OldSHA256: hb},
				{StartLine: 4, LineCount: 1, OldSHA256: db},
			},
			wantErr:  ErrOverlappingSpans,
			wantCode: "overlapping_spans",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyRemovals(base, target, tc.declared)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("VerifyRemovals = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifyRemovals = %v, want %v", err, tc.wantErr)
			}
			var re *RemovalError
			if !errors.As(err, &re) {
				t.Fatalf("error %v is not a *RemovalError", err)
			}
			if re.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", re.Code, tc.wantCode)
			}
			if re.Detail == "" {
				t.Error("RemovalError.Detail is empty; the client gets no explanation")
			}
		})
	}
}

// TestVerifyRemovalsAcceptsDifferentChunking documents a deliberate reading of
// §8. The merge rule ("sloučit sousední deletions") constrains the reference
// diff, not the client's declaration, so a client that splits one contiguous
// removal into two touching spans has declared exactly the same set of removed
// lines and is accepted. What it may never do is remove a line it did not
// declare, or declare a line the diff keeps.
func TestVerifyRemovalsAcceptsDifferentChunking(t *testing.T) {
	base, target := lines(t, "a\nb\nc\nd\ne\n"), lines(t, "a\ne\n")

	merged := Removals(base, target)
	if len(merged) != 1 || merged[0].LineCount != 3 {
		t.Fatalf("expected one merged span of 3, got %+v", merged)
	}

	split := []Removal{
		{StartLine: 2, LineCount: 1, OldSHA256: sha("b\n")},
		{StartLine: 3, LineCount: 2, OldSHA256: sha("c\nd\n")},
	}
	if err := VerifyRemovals(base, target, split); err != nil {
		t.Fatalf("VerifyRemovals rejected an equivalent split declaration: %v", err)
	}
}

// TestVerifyRemovalsOnEmptyInputs covers the degenerate shapes the API can
// receive: nothing to remove, and nothing to remove from.
func TestVerifyRemovalsOnEmptyInputs(t *testing.T) {
	empty := lines(t, "")

	if err := VerifyRemovals(empty, empty, nil); err != nil {
		t.Fatalf("empty->empty with no removals: %v", err)
	}
	if err := VerifyRemovals(empty, lines(t, "a\n"), nil); err != nil {
		t.Fatalf("creating content from an empty base removes nothing: %v", err)
	}
	// Any declaration against an empty base is out of range: there is no line 1.
	err := VerifyRemovals(empty, lines(t, "a\n"), []Removal{{StartLine: 1, LineCount: 1, OldSHA256: sha("")}})
	if !errors.Is(err, ErrSpanOutOfRange) {
		t.Fatalf("declaration against an empty base = %v, want ErrSpanOutOfRange", err)
	}
	// Wiping a file must be declared.
	base := lines(t, "a\n")
	if err := VerifyRemovals(base, empty, nil); !errors.Is(err, ErrUndeclaredRemoval) {
		t.Fatalf("undeclared wipe = %v, want ErrUndeclaredRemoval", err)
	}
	if err := VerifyRemovals(base, empty, []Removal{{StartLine: 1, LineCount: 1, OldSHA256: sha("a\n")}}); err != nil {
		t.Fatalf("declared wipe: %v", err)
	}
}

// TestVerifyRemovalsCatchesTheWrongBase is the drift case §8 calls
// memory_conflict at the API level: the client diffed against a different
// revision, so its hashes do not match the base the server holds.
func TestVerifyRemovalsCatchesTheWrongBase(t *testing.T) {
	serverBase := lines(t, "a\nb\nc\n")
	clientBase := lines(t, "a\nB\nc\n")
	target := lines(t, "a\nc\n")

	declared := Removals(clientBase, target)
	if len(declared) != 1 {
		t.Fatalf("expected one removal, got %+v", declared)
	}
	err := VerifyRemovals(serverBase, target, declared)
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("stale-base declaration = %v, want ErrHashMismatch", err)
	}
}

// TestRemovalErrorIsInspectable checks the typed result carries enough for a
// caller to build an API error body without re-parsing a string.
func TestRemovalErrorIsInspectable(t *testing.T) {
	base, target := lines(t, "a\nb\n"), lines(t, "a\n")
	err := VerifyRemovals(base, target, nil)

	var re *RemovalError
	if !errors.As(err, &re) {
		t.Fatalf("err %v is not a *RemovalError", err)
	}
	if re.Code != "undeclared_removal" {
		t.Fatalf("code = %q", re.Code)
	}
	if re.Span.StartLine != 2 || re.Span.LineCount != 1 {
		t.Fatalf("span = %+v, want the derived {2,1}", re.Span)
	}
	if re.Index != -1 {
		t.Fatalf("index = %d, want -1: an undeclared removal is not attributable to a declaration", re.Index)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("Error() = %q, want it to name the offending line", err.Error())
	}
}
