// Package memdiff is the reference line diff behind Crewship's memory write
// contract (PRD "Crewship 1.0: implementační a akceptační kontrakt", §8).
//
// The server uses it to check the `removals` a client declares on a memory
// `replace`. A replace that removes a line the client did not declare is
// rejected with `undeclared_removal`; that decision has to be reproducible
// byte-for-byte on both sides, so the algorithm here is pinned by golden
// fixtures in testdata/ rather than delegated to a library.
//
// # Why not go-difflib
//
// The repo already depends on github.com/pmezard/go-difflib, but only for
// human-facing unified diffs (internal/api/consolidate_proposed_diff_handler.go,
// internal/api/pipelines_diff.go). It implements Ratcliff/Obershelp, not
// Myers, and has none of §8's tie-break rules, so it cannot be the normative
// implementation. This package is self-contained and depends only on the
// standard library.
//
// # The pipeline
//
//	raw bytes ──Normalize──> normalized bytes ──SplitLines──> [][]byte lines
//	                                                              │
//	                                        Diff / Removals / VerifyRemovals
//
// Every function below that takes [][]byte expects lines that came out of
// SplitLines applied to Normalize'd content. Feeding it CRLF content or
// lines without their trailing LF produces well-defined but meaningless
// hashes, because the removal hash covers the exact bytes including the LF.
//
// # What §8 fixes, in order
//
//   - CRLF is normalized to LF before versioning; the trailing newline is
//     preserved exactly as given; invalid UTF-8 is rejected.
//   - Line numbers are 1-based in the original (base) revision.
//   - A removal hash is SHA-256 over the exact removed bytes including their
//     LFs; the last line of a file may have no LF, and then it is hashed
//     without one.
//   - The diff is a deterministic line-based shortest edit script (Myers).
//     On a tie, delete is preferred before insert, and adjacent deletions are
//     merged into one op.
//   - Which of several identical lines is deleted is decided by position in
//     the base revision. See the repeated-lines golden fixture.
//   - A rewritten line is a delete plus an insert, never a single "change".
//
// # Cost, and the guard this package does NOT have
//
// Myers is O(ND) in time and this implementation records one compact frontier
// per edit distance, so it is O(D^2/2) ints in memory, where D is the number of
// edited lines. That is cheap for the shape memory writes actually have — a
// 5000-line file with a ten-line edit diffs in ~0.1 ms and ~0.4 MiB — and
// quadratic for the shape an attacker would pick. Measured on the reference dev
// box, two files of n lines with NOTHING in common:
//
//	n=1000  (D=2000)   17 ms     16 MiB
//	n=2000  (D=4000)   61 ms     65 MiB
//	n=4000  (D=8000)  239 ms    265 MiB
//
// Nothing here caps that. §8 sets a 1 MiB webhook body limit but says nothing
// about memory content size, and Diff's signature has no error to return. The
// caller wiring this into the write path is responsible for bounding the input
// — a line-count or byte cap checked under the mutation lock, before Diff — and
// for deciding what a too-large replace returns. Do not reach this package from
// an unbounded request body.
package memdiff
