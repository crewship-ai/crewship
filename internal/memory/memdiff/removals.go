package memdiff

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// Removal is one declared deleted span, exactly as §8 defines it: "Removals
// jsou seznam {start_line, line_count, old_sha256} v původním normalizovaném
// podkladu, číslování od 1, neprotínající se intervaly. Hash je SHA256 přes
// přesné bajty odstraněných řádků včetně jejich LF; poslední řádek LF nemít
// může."
type Removal struct {
	StartLine int    `json:"start_line"` // 1-based, in the base revision
	LineCount int    `json:"line_count"` // >= 1
	OldSHA256 string `json:"old_sha256"` // lowercase hex, over the exact removed bytes including their LFs
}

// EndLine is the last base line the removal covers, 1-based and inclusive.
func (r Removal) EndLine() int { return r.StartLine + r.LineCount - 1 }

// Sentinel errors. Every error VerifyRemovals and HashSpan return wraps
// exactly one of these, so a caller can map a failure onto the API error
// vocabulary in §8 with errors.Is.
var (
	// ErrInvalidSpan is a structurally impossible span: StartLine < 1 (the
	// numbering is 1-based) or LineCount < 1 (an empty removal declares
	// nothing).
	ErrInvalidSpan = errors.New("memdiff: invalid removal span")

	// ErrSpanOutOfRange is a well-formed span that runs past the end of the
	// base revision.
	ErrSpanOutOfRange = errors.New("memdiff: removal span outside the base")

	// ErrOverlappingSpans is two declared spans that intersect. §8 requires
	// non-intersecting intervals.
	ErrOverlappingSpans = errors.New("memdiff: overlapping removal spans")

	// ErrHashMismatch is a span whose old_sha256 does not match the bytes it
	// covers in the base. The client diffed against a different revision.
	ErrHashMismatch = errors.New("memdiff: removal hash does not match the base")

	// ErrUndeclaredRemoval is the §8 `undeclared_removal` case: the real diff
	// removes a base line that no declared span covers.
	ErrUndeclaredRemoval = errors.New("memdiff: base line removed but not declared")

	// ErrSpuriousRemoval is the mirror image: a declared span covers a base
	// line that the real diff keeps. §8 folds this into `undeclared_removal`
	// ("undeclared_removal pro neshodu diffu"), but the two are different
	// client bugs and worth telling apart in logs.
	ErrSpuriousRemoval = errors.New("memdiff: line declared removed but kept by the diff")
)

// RemovalError carries which declaration failed and why. It wraps one of the
// sentinels above; Code is a stable, log-safe discriminator.
type RemovalError struct {
	Code   string  // "invalid_span", "span_out_of_range", "overlapping_spans", "hash_mismatch", "undeclared_removal", "spurious_removal"
	Index  int     // index into the declared slice, or -1 when the failure is not attributable to one declaration
	Span   Removal // the offending declaration, or the offending derived span for undeclared_removal
	Detail string
	err    error
}

func (e *RemovalError) Error() string {
	return fmt.Sprintf("%s (declaration %d, lines %d..%d): %s",
		e.err.Error(), e.Index, e.Span.StartLine, e.Span.EndLine(), e.Detail)
}

// Unwrap exposes the sentinel so errors.Is works.
func (e *RemovalError) Unwrap() error { return e.err }

func removalErr(code string, sentinel error, index int, span Removal, format string, args ...any) *RemovalError {
	return &RemovalError{
		Code:   code,
		Index:  index,
		Span:   span,
		Detail: fmt.Sprintf(format, args...),
		err:    sentinel,
	}
}

// HashSpan computes the old_sha256 of base lines [startLine, startLine+lineCount)
// — SHA-256 over the exact bytes of those lines, each including its trailing
// LF. The last line of a file may have no LF; then it is hashed without one,
// which is why the hash of the final line differs between a base that ends in
// a newline and one that does not.
//
// It exists so callers, the server and the tests all agree on one definition
// instead of three re-derivations of the same loop.
func HashSpan(base [][]byte, startLine, lineCount int) (string, error) {
	span := Removal{StartLine: startLine, LineCount: lineCount}
	if err := checkSpan(base, span, -1); err != nil {
		return "", err
	}
	h := sha256.New()
	for _, line := range base[startLine-1 : startLine-1+lineCount] {
		h.Write(line)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checkSpan(base [][]byte, span Removal, index int) error {
	if span.StartLine < 1 {
		return removalErr("invalid_span", ErrInvalidSpan, index, span,
			"start_line is %d; line numbering is 1-based", span.StartLine)
	}
	if span.LineCount < 1 {
		return removalErr("invalid_span", ErrInvalidSpan, index, span,
			"line_count is %d; a removal must cover at least one line", span.LineCount)
	}
	if span.EndLine() > len(base) {
		return removalErr("span_out_of_range", ErrSpanOutOfRange, index, span,
			"span ends at line %d but the base has %d lines", span.EndLine(), len(base))
	}
	return nil
}

// Removals derives the removal declaration that the base->target edit implies:
// one entry per contiguous run of deleted base lines, in ascending line order,
// each hashed with HashSpan.
//
// A pure append removes nothing and yields an empty (nil) slice — which is what
// makes "append expressed as replace" carry `removals: []` per §8 ("I append
// přes replace musí nést expected_revision a prázdné removals").
//
// Both inputs must already be Normalize'd and SplitLines'd.
func Removals(base, target [][]byte) []Removal {
	var spans []Removal
	for _, op := range Diff(base, target) {
		if op.Kind != OpDelete {
			continue
		}
		// Diff already merges deletions that are adjacent in the script, and
		// with the current tie-break an insert can never separate two
		// base-contiguous deletes (TestDiffNeverEmitsInsertBeforeDelete, and
		// the fuzz/property runs). This second merge is therefore currently
		// unreachable. It is kept deliberately: it is the only thing standing
		// between a future change to the tie-break and a removals list with
		// two touching intervals where §8 wants one, and it costs one
		// comparison per delete op.
		if n := len(spans); n > 0 && spans[n-1].EndLine()+1 == op.StartLine {
			spans[n-1].LineCount += op.LineCount
			continue
		}
		spans = append(spans, Removal{StartLine: op.StartLine, LineCount: op.LineCount})
	}
	for i := range spans {
		// checkSpan cannot fail here: the spans come from Diff over this base.
		h, err := HashSpan(base, spans[i].StartLine, spans[i].LineCount)
		if err != nil {
			panic("memdiff: derived removal outside its own base: " + err.Error())
		}
		spans[i].OldSHA256 = h
	}
	return spans
}

// VerifyRemovals reports whether declared matches exactly what base->target
// actually removes. This is the server-side guard §8 asks for: "server ji
// používá pro kontrolu removals".
//
// It returns nil only when every check passes. Checks run in this order, and
// the first failure is returned, so the error a client sees is the most
// specific one available:
//
//  1. each span is structurally valid (1-based, LineCount >= 1) — ErrInvalidSpan
//  2. each span lies inside the base                            — ErrSpanOutOfRange
//  3. no two spans intersect                                    — ErrOverlappingSpans
//  4. each old_sha256 matches the base bytes it covers          — ErrHashMismatch
//  5. every line the diff removes is declared                   — ErrUndeclaredRemoval
//  6. every line declared is actually removed                   — ErrSpuriousRemoval
//
// Steps 5 and 6 compare the SET of covered base lines, not the tuples. A client
// that declares {1,1} and {2,1} where the reference diff merges them into
// {1,2} is declaring the same removal and is accepted; §8's merge rule
// constrains the diff algorithm, not the client's chunking. What it may not do
// is remove a line it did not declare, or declare a line it did not remove.
//
// Both line slices must already be Normalize'd and SplitLines'd.
func VerifyRemovals(base, target [][]byte, declared []Removal) error {
	for i, span := range declared {
		if err := checkSpan(base, span, i); err != nil {
			return err
		}
	}

	order := make([]int, len(declared))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return declared[order[a]].StartLine < declared[order[b]].StartLine
	})
	for i := 1; i < len(order); i++ {
		prev, cur := declared[order[i-1]], declared[order[i]]
		if cur.StartLine <= prev.EndLine() {
			return removalErr("overlapping_spans", ErrOverlappingSpans, order[i], cur,
				"overlaps declaration %d covering lines %d..%d", order[i-1], prev.StartLine, prev.EndLine())
		}
	}

	for i, span := range declared {
		want, err := HashSpan(base, span.StartLine, span.LineCount)
		if err != nil {
			return err
		}
		if !equalHash(span.OldSHA256, want) {
			return removalErr("hash_mismatch", ErrHashMismatch, i, span,
				"old_sha256 is %q but lines %d..%d of the base hash to %q",
				span.OldSHA256, span.StartLine, span.EndLine(), want)
		}
	}

	declaredLine := make([]bool, len(base)+1)
	declaredBy := make([]int, len(base)+1)
	for i, span := range declared {
		for ln := span.StartLine; ln <= span.EndLine(); ln++ {
			declaredLine[ln] = true
			declaredBy[ln] = i
		}
	}

	actual := Removals(base, target)
	actualLine := make([]bool, len(base)+1)
	for _, span := range actual {
		for ln := span.StartLine; ln <= span.EndLine(); ln++ {
			actualLine[ln] = true
		}
	}

	for _, span := range actual {
		for ln := span.StartLine; ln <= span.EndLine(); ln++ {
			if !declaredLine[ln] {
				return removalErr("undeclared_removal", ErrUndeclaredRemoval, -1, span,
					"base line %d is removed by the diff but no declaration covers it", ln)
			}
		}
	}
	for ln := 1; ln <= len(base); ln++ {
		if declaredLine[ln] && !actualLine[ln] {
			i := declaredBy[ln]
			return removalErr("spurious_removal", ErrSpuriousRemoval, i, declared[i],
				"base line %d is declared removed but the diff keeps it", ln)
		}
	}
	return nil
}

// equalHash compares hex digests case-insensitively without allocating.
func equalHash(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lowerHex(a[i]) != lowerHex(b[i]) {
			return false
		}
	}
	return true
}

func lowerHex(c byte) byte {
	if c >= 'A' && c <= 'F' {
		return c + ('a' - 'A')
	}
	return c
}
