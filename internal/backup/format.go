// Package backup provides the foundational primitives for Crewship's
// backup & restore system: manifest format, streaming tar.zst bundle,
// AGE encryption, SHA-256 integrity checks, and a per-workspace advisory
// lock in the main database.
//
// This file defines the on-disk format version, the direct-read window
// and the long-term compatibility promise.
//
// The promise: every supported backup remains recoverable through the
// current recovery tooling, directly or through maintained converters.
//
//   - FormatVersion is monotonically increasing. It is bumped only for
//     incompatible on-disk layout or meaning changes. Additive JSON
//     fields do not bump the version.
//   - Direct read: a reader at FormatVersion V restores, verifies and
//     inspects bundles whose FormatVersion is within
//     [MinSupportedFormatVersion, V] (at most V-2 back). IsCompatible is
//     that gate and nothing else.
//   - Conversion: every bundle from OldestRecoverableFormatVersion up is
//     recoverable through `crewship backup convert`, which chains the
//     converters in internal/backup/convert (one step per version) to
//     write a NEW bundle at FormatVersion, plus a report of what each
//     step changed and what cannot be recovered. The original bundle is
//     never modified. A bundle older than the direct-read window gets a
//     ConvertRequiredError naming the exact command — never a dead end.
//   - A bundle with FormatVersion greater than the current reader
//     surfaces ErrFormatTooNew with a pointer to upgrade Crewship.
//
// Rule for bumping FormatVersion (enforced by tests in
// internal/backup/convert, so a bump cannot merge without both):
//
//  1. Register a converter step from the previous version to the new one
//     in internal/backup/convert (steps.go). The step must be honest: it
//     sets whatever manifest fields the newer reader relies on, and it
//     reports — never invents — anything the older layout could not have
//     carried.
//  2. Commit a fixture bundle for the new version under
//     internal/backup/testdata/compat/ (`go test ./internal/backup/convert
//     -run TestCompatFixtures -update-compat-fixtures` writes the missing
//     ones). Existing fixtures are never regenerated: they stand for
//     bundles already in the field, and CI restores every one of them on
//     every run.
//
// OldestRecoverableFormatVersion is never raised; dropping a converter
// breaks the promise for every bundle that still needs it.
package backup

import (
	"errors"
	"fmt"
	"strings"
)

// FormatVersion is the on-disk layout version written into every
// MANIFEST.json produced by this binary.
//
// v1 → v2 (2026-05-25): no on-disk layout change. The bump reflects
// restore-side semantics: --replace mode lands and the dump now
// carries the expanded table set discovered via FK walk (50+ tables
// vs the historical 10). A v1 reader on a future version can still
// restore v2 bundles correctly because INSERT OR IGNORE silently
// drops unknown tables — but the bump pins the boundary so admins
// reading manifest.format_version can tell whether their bundle
// supports the post-rewrite contract.
// v2 → v3 (2026-08-03, #1713): the payload gains a `crew/<slug>/`
// section carrying /crew — the agent and crew-shared memory trees,
// which no earlier bundle contained at any scope level. Existing
// sections are unchanged, so a v3 reader restores v1 and v2 bundles
// exactly as before; what the bump buys is the ability to tell an
// operator the truth about one. Up to v2, contents.crews[].memory_included
// was set unconditionally and pointed at /output, so a pre-v3 bundle
// asserting memory_included: true is asserting something that was never
// checked. FormatVersionCrewMemory is the boundary every reader of that
// flag must consult — see CrewSummary.HasCrewMemory.
//
// Not a bump (2026-09-30): the attachment-blobs/ payload section and the
// manifest's contents.attachments_included / attachments_missing /
// incomplete fields. Both are additive — ExtractPayload discards unknown
// top-level entries and ReadManifest ignores unknown fields — so a v3
// reader without them restores a bundle carrying them exactly as before,
// and no existing field changed meaning. A reader tells a bundle that
// predates the section by attachments_included being absent, not by the
// format version.
const FormatVersion = 4

// Quota service images require readers that restore their persistent data.
const FormatVersionServiceSnapshots = 4

// FormatVersionCrewMemory is the first format version whose
// memory_included flag means what it says: observed, and about the real
// memory tree.
const FormatVersionCrewMemory = 3

// MinSupportedFormatVersion is the oldest bundle layout this binary can
// still read directly. It implements the N-2 policy:
// MinSupportedFormatVersion = max(1, FormatVersion-2). v1 bundles
// (10-table dump) are are recovered through the maintained converter chain.
const MinSupportedFormatVersion = 2

// OldestRecoverableFormatVersion is the oldest bundle layout the current
// recovery tooling can bring back, directly or through converters. It
// is 1 — the first format ever written — and it stays 1: the converter
// chain in internal/backup/convert must cover every step from here to
// FormatVersion (TestConverterChainCoversEveryVersion).
const OldestRecoverableFormatVersion = 1

// IsCompatible reports whether a bundle written with `written` can be
// read directly by this binary (current reader at FormatVersion).
//
// The policy is N-2: accept [MinSupportedFormatVersion, FormatVersion].
// Bundles outside this range return false and the caller should surface
// ErrFormatTooOld (as a ConvertRequiredError when it knows the path) or
// ErrFormatTooNew accordingly. IsCompatible is the direct-read gate
// only; a false answer for an old bundle does not mean unrecoverable.
func IsCompatible(written int) bool {
	return written >= MinSupportedFormatVersion && written <= FormatVersion
}

// CompatibilityReason returns a typed error explaining why a given
// written format version is not compatible with the current reader,
// or nil if IsCompatible(written) is true.
func CompatibilityReason(written int) error {
	if written > FormatVersion {
		return ErrFormatTooNew
	}
	if written < MinSupportedFormatVersion {
		return ErrFormatTooOld
	}
	return nil
}

// ConvertCommand returns the exact CLI invocation that converts the
// bundle at bundlePath to the current FormatVersion. The output path is
// a sibling of the input (ConvertedBundlePath), so the original is never
// overwritten. Identity / passphrase flags are left for the operator to
// add — only they know which key opens the bundle.
func ConvertCommand(bundlePath string) string {
	return "crewship backup convert --bundle " + shellQuote(bundlePath) +
		" --out " + shellQuote(ConvertedBundlePath(bundlePath))
}

// ConvertedBundlePath is the default output path ConvertCommand suggests:
// "x.tar.zst" becomes "x.v3.tar.zst" (for FormatVersion 3).
func ConvertedBundlePath(bundlePath string) string {
	infix := fmt.Sprintf(".v%d", FormatVersion)
	for _, ext := range []string{".tar.zst", ".crewship"} {
		if strings.HasSuffix(bundlePath, ext) {
			return strings.TrimSuffix(bundlePath, ext) + infix + ext
		}
	}
	return bundlePath + infix + ".tar.zst"
}

// shellQuote single-quotes s when it carries anything a POSIX shell
// would interpret, so the printed command can be pasted as is.
func shellQuote(s string) string {
	safe := s != "" && strings.IndexFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case strings.ContainsRune("-_./:@%+=,", r):
			return false
		}
		return true
	}) < 0
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// ConvertRequiredError is what restore, verify and inspect return for a
// bundle older than the direct-read window: it names the version and the
// exact command that makes the bundle readable again. It unwraps to
// ErrFormatTooOld so existing errors.Is checks (HTTP 400, metrics) hold.
type ConvertRequiredError struct {
	Path    string
	Written int
}

func (e *ConvertRequiredError) Error() string {
	return fmt.Sprintf(
		"backup: %s is format v%d; this reader reads v%d–v%d directly. Convert it first (the original is left untouched): %s",
		e.Path, e.Written, MinSupportedFormatVersion, FormatVersion, ConvertCommand(e.Path))
}

// Unwrap keeps errors.Is(err, ErrFormatTooOld) true.
func (e *ConvertRequiredError) Unwrap() error { return ErrFormatTooOld }

// WithConvertHint upgrades a bare ErrFormatTooOld from ReadBundle /
// ReadBundleStream into a ConvertRequiredError carrying the bundle path,
// when the bundle is still within reach of the converter chain. Any
// other error (or nil) passes through unchanged.
func WithConvertHint(path string, m *Manifest, err error) error {
	if err == nil || m == nil || !errors.Is(err, ErrFormatTooOld) {
		return err
	}
	var already *ConvertRequiredError
	if errors.As(err, &already) {
		return err
	}
	if m.FormatVersion < OldestRecoverableFormatVersion {
		return err
	}
	return &ConvertRequiredError{Path: path, Written: m.FormatVersion}
}
