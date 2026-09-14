package memdiff

import (
	"bytes"
	"errors"
	"unicode/utf8"
)

// ErrInvalidUTF8 is returned by Normalize when the input is not valid UTF-8.
// §8: "UTF-8 vstup normalizovat CRLF na LF před verzováním, zachovat koncový
// newline; odmítnout nevalidní UTF-8."
var ErrInvalidUTF8 = errors.New("memdiff: content is not valid UTF-8")

var (
	crlf = []byte("\r\n")
	lf   = []byte("\n")
)

// Normalize validates that b is UTF-8 and rewrites every CRLF as a bare LF.
//
// It does NOT add or strip a trailing newline. §8 says the trailing newline is
// preserved, and it has to be: whether the last line ends in LF changes the
// SHA-256 of any removal that covers it, so inventing or dropping one would
// silently invalidate a client's declared removals.
//
// A lone CR that is not part of a CRLF is content, not a line terminator, and
// survives untouched — old-Mac line endings are not a supported input form and
// treating them as terminators would split lines the client did not split.
//
// This is a single left-to-right pass, so Normalize is NOT idempotent for the
// one input shape where a literal CR sits immediately before a CRLF: "a\r\r\n"
// becomes "a\r\n" (content "a\r", terminator LF), and normalizing that again
// would eat the content CR. No normalizer can be both CR-preserving and
// idempotent, because "a\n" and "a\r\n" have to collapse onto the same
// canonical form. Apply Normalize exactly once, at ingest; §8 already forbids
// re-normalizing stored content on read. See TestNormalizeIsNotIdempotent.
//
// The returned slice never aliases b; the caller may keep either.
func Normalize(b []byte) ([]byte, error) {
	if !utf8.Valid(b) {
		return nil, ErrInvalidUTF8
	}
	if !bytes.Contains(b, crlf) {
		out := make([]byte, len(b))
		copy(out, b)
		return out, nil
	}
	return bytes.ReplaceAll(b, crlf, lf), nil
}

// SplitLines splits normalized content into lines, each including its trailing
// LF. A final line that has no LF is returned without one.
//
// The rule is "cut immediately after every LF", which makes the split lossless:
// bytes.Join(SplitLines(b), nil) always equals b. That is the property the
// removal hash depends on, and it is why an empty input has zero lines rather
// than one empty line.
//
// Precisely:
//
//	""       -> []            (0 lines)
//	"\n"     -> ["\n"]        (1 line: an empty line that is terminated)
//	"a"      -> ["a"]         (1 line: unterminated)
//	"a\n"    -> ["a\n"]       (1 line: terminated — NOT followed by an empty one)
//	"a\n\n"  -> ["a\n","\n"]  (2 lines: the second is an empty terminated line)
//
// The returned lines alias b. Callers must not mutate them.
func SplitLines(b []byte) [][]byte {
	if len(b) == 0 {
		return nil
	}
	lines := make([][]byte, 0, bytes.Count(b, lf)+1)
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			lines = append(lines, b[start:i+1])
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
}

// NormalizeLines is the two-step pipeline Normalize+SplitLines, for callers
// that only ever need the lines. It returns the normalized bytes too, because
// the canonical content is what gets written to disk and hashed as
// content_sha256.
func NormalizeLines(b []byte) ([]byte, [][]byte, error) {
	norm, err := Normalize(b)
	if err != nil {
		return nil, nil, err
	}
	return norm, SplitLines(norm), nil
}
