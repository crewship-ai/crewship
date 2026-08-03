// Package backup provides the foundational primitives for Crewship's
// backup & restore system: manifest format, streaming tar.zst bundle,
// AGE encryption, SHA-256 integrity checks, and a per-workspace advisory
// lock in the main database.
//
// This file defines the on-disk format version and the N-2 compatibility
// policy.
//
// Forward-compatibility contract:
//
//   - FormatVersion is monotonically increasing. It is bumped only for
//     incompatible on-disk layout changes. Additive JSON fields do not
//     bump the version.
//   - A reader on Crewship version V must accept bundles written by
//     any Crewship version whose FormatVersion is within [V-2, V].
//     Older bundles surface ErrFormatTooOld with a pointer to the
//     `crewship backup migrate` tool (delivered in V1.5).
//   - A bundle with FormatVersion greater than the current reader
//     surfaces ErrFormatTooNew with a pointer to upgrade Crewship.
//
// The matrix is enforced by IsCompatible and is covered by tests.
package backup

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
const FormatVersion = 3

// FormatVersionCrewMemory is the first format version whose
// memory_included flag means what it says: observed, and about the real
// memory tree.
const FormatVersionCrewMemory = 3

// MinSupportedFormatVersion is the oldest bundle layout this binary can
// still read. It implements the N-2 policy: MinSupportedFormatVersion =
// max(1, FormatVersion-2). v1 bundles (encrypted AGE-passphrase, 10-
// table dump) remain restorable.
const MinSupportedFormatVersion = 1

// IsCompatible reports whether a bundle written with `written` can be
// read by this binary (current reader at FormatVersion).
//
// The policy is N-2: accept [MinSupportedFormatVersion, FormatVersion].
// Bundles outside this range return false and the caller should surface
// ErrFormatTooOld or ErrFormatTooNew accordingly.
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
