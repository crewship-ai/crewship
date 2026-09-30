// Package convert keeps old backup bundles recoverable.
//
// The promise it implements (docs/guides/backup.mdx, "Compatibility"):
// every supported backup remains recoverable through the current recovery
// tooling, directly or through maintained converters.
//
// A converter step takes a bundle at FormatVersion N and describes the same
// data at N+1. Convert chains the steps registered in Default() from the
// bundle's version up to backup.FormatVersion and writes a NEW bundle —
// the original is only ever read — together with a Report of what each
// step changed and what the older layout could never have carried. Steps
// are honest: they set the manifest fields a newer reader relies on from
// what the payload actually contains, and they report, never invent, the
// data an older version did not collect.
//
// Encryption: converting needs the key that opens the bundle (an age
// identity or the passphrase). The converted bundle is sealed again to
// the recipients recorded in the source manifest, or to the passphrase
// that opened it, unless the caller names new recipients or a new
// passphrase. Plaintext never touches disk: the payload is decrypted
// twice as a stream (index, then rewrite) instead of being spooled.
//
// Rule for anyone changing the on-disk format (see internal/backup/format.go):
// bumping backup.FormatVersion requires, in the same change,
//
//  1. a Step whose From is the previous FormatVersion, registered in
//     steps.go, and
//  2. a fixture bundle for the new version in internal/backup/testdata/compat
//     (`go test ./internal/backup/convert -run TestCompatFixtures
//     -update-compat-fixtures` writes the missing ones; existing fixtures
//     are never regenerated).
//
// TestConverterChainCoversEveryVersion and TestCompatFixtures fail until
// both exist, and TestCompatFixtures restores every fixture — converting
// it first when needed — into a fresh database on every CI run.
package convert
