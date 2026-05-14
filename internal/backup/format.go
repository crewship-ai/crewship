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
const FormatVersion = 1

// MinSupportedFormatVersion is the oldest bundle layout this binary can
// still read. It implements the N-2 policy: MinSupportedFormatVersion =
// max(1, FormatVersion-2).
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
