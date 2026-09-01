package backup

// Completeness checking (#2009).
//
// Verify (runner.go) streams payload.age through HashingReader and compares
// the digest against manifest.Checksums.PayloadSHA256. That answers "are
// these the bytes we wrote" — integrity — and it is a good answer, but it is
// not "does this bundle contain what it claims to contain" — completeness.
// A bundle that dumped zero rows for a table because a scoping filter
// picked a nullable column that happened to be NULL everywhere reports
// ✓ VALID, because nothing about the checksum path even looks at row
// counts.
//
// This file gives Verify (and RestoreBackup's dump-extraction step, which
// gets it for free once the payload is decrypted) something to compare
// against: Manifest.Contents.TableRowCounts, recorded at create time. See
// that field's doc comment for exactly what this guarantees and — just as
// important — what it does not: a create-time scoping bug that undercounts
// still undercounts identically in both the payload and the recorded
// count, so this is "the bundle is intact", not "the bundle is everything".

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"strings"
)

// TableRowCountMismatch is one table whose payload row count disagrees with
// what the manifest recorded at create time.
type TableRowCountMismatch struct {
	Table    string `json:"table"`
	Recorded int    `json:"recorded"`
	Actual   int    `json:"actual"`
}

// compareRowCounts reports every table in recorded whose count in actual
// differs. Only tables the manifest actually recorded are compared — a
// table present in actual but absent from recorded is not a completeness
// gap by this definition, since there is nothing to check it against
// (typically an older/partial manifest, or a bundle section this function's
// caller was not asked to reconcile).
//
// Deterministic order (sorted table name) so two comparisons of the same
// pair of maps produce an identical report — the same reasoning
// tallyDroppedColumns documents for its own ordering.
func compareRowCounts(recorded, actual map[string]int) []TableRowCountMismatch {
	if len(recorded) == 0 {
		return nil
	}
	tables := make([]string, 0, len(recorded))
	for t := range recorded {
		tables = append(tables, t)
	}
	sortStrings(tables)
	var out []TableRowCountMismatch
	for _, t := range tables {
		want := recorded[t]
		got := actual[t] // 0 when the table is absent from actual entirely
		if got != want {
			out = append(out, TableRowCountMismatch{Table: t, Recorded: want, Actual: got})
		}
	}
	return out
}

// scanDumpFromTarEntry reads r as a zstd-compressed tar (the shape of an
// unencrypted payload) looking only for db/dump.json, ignoring every other
// entry. Returns (nil, nil) when the archive has no db/dump.json entry at
// all — an instance-scope-only bundle, or a files-only resume payload, both
// of which legitimately carry no DB section.
//
// Deliberately lighter than ExtractPayload: that helper writes every
// section (workspace/, volumes/, memory/, system/) to temp files because
// restore needs to hand them to Docker afterward. Verify needs none of
// that — only the few KB of db/dump.json — so this reads the tar directly
// without touching disk.
//
// r must be read to completion by the CALLER after this returns, even on
// the (nil, nil) "not found" path: this function stops as soon as it has
// what it needs (or hits real EOF), and does not itself guarantee every
// byte of r was consumed — see Verify's drain step for why that matters
// when r sits underneath a running checksum.
func scanDumpFromTarEntry(r io.Reader) (*DBDump, error) {
	tr, err := NewTarZstReader(r)
	if err != nil {
		// Not a readable zstd stream at all — most likely an encrypted
		// payload handed in by a caller that should have checked
		// Encryption.Enabled first. Not found, not a hard error: callers
		// that want a hard failure here already control when this is
		// invoked.
		return nil, nil //nolint:nilerr // caller decides whether "not found" matters
	}
	defer func() { _ = tr.Close() }()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("backup: scan payload for completeness: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name != "db/dump.json" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxBackupDBDumpBytes))
		if err != nil {
			return nil, fmt.Errorf("backup: read db/dump.json from payload: %w", err)
		}
		return UnmarshalDump(data)
	}
}

// warnRowCountMismatches emits the operator-facing note for a non-empty
// []TableRowCountMismatch, shared by every caller of compareRowCounts
// (restore's payload check and its post-insert shortfall check) so the same
// shape of report reads the same way everywhere it appears. what names
// which comparison this is ("payload row counts", "rows inserted") since
// the two mean different things — see PayloadRowCountMismatches's and
// RowsInsertedShortfalls's doc comments on RestoreResult.
func warnRowCountMismatches(logger func(string), what string, mismatches []TableRowCountMismatch) {
	if len(mismatches) == 0 || logger == nil {
		return
	}
	details := make([]string, 0, len(mismatches))
	for _, m := range mismatches {
		details = append(details, fmt.Sprintf("%s (recorded %d, actual %d)", m.Table, m.Recorded, m.Actual))
	}
	logger(fmt.Sprintf(
		"WARNING: %s do not match what the manifest recorded for %d table(s): %s. "+
			"This bundle is not what its own manifest claims it is — treat it as suspect before relying on it for disaster recovery.",
		what, len(mismatches), strings.Join(details, "; ")))
}

// completenessSkipReason explains why Verify could not check completeness,
// given what it found (or didn't). Exactly one of encrypted / dumpFound /
// hasCounts should drive the branch taken; see Verify's call site for how
// they're derived.
func completenessSkipReason(encrypted bool, dumpFound bool, hasRecordedCounts bool) string {
	switch {
	case encrypted:
		return "bundle is encrypted; verify does not decrypt the payload, so row counts cannot be inspected — run `crewship backup restore <file> --dry-run` with the passphrase for a completeness check"
	case !dumpFound:
		return "bundle payload carries no db/dump.json section to check (or its db section could not be parsed)"
	case !hasRecordedCounts:
		return "bundle predates row-count recording (#2009); no baseline to compare the payload against"
	default:
		return ""
	}
}
