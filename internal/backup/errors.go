package backup

import "errors"

// Typed errors for the backup package. Callers should use errors.Is /
// errors.As to match; error strings are informational and may change.
var (
	// ErrFormatTooOld is returned when a bundle's FormatVersion is below
	// MinSupportedFormatVersion. The caller should advise the user to
	// upgrade the bundle via `crewship backup migrate` (V1.5).
	ErrFormatTooOld = errors.New("backup: bundle format version too old for this reader")

	// ErrFormatTooNew is returned when a bundle's FormatVersion is above
	// FormatVersion. The caller should advise the user to upgrade Crewship.
	ErrFormatTooNew = errors.New("backup: bundle format version too new for this reader")

	// ErrInvalidManifest is returned when MANIFEST.json is missing a
	// required field or contains invalid values.
	ErrInvalidManifest = errors.New("backup: invalid manifest")

	// ErrInvalidChecksum is returned when the payload SHA-256 recorded
	// in the manifest does not match the actual bytes on disk.
	ErrInvalidChecksum = errors.New("backup: payload checksum mismatch")

	// ErrLockHeld is returned by AcquireWorkspaceLock when another
	// backup is already in progress for the same workspace.
	ErrLockHeld = errors.New("backup: another backup is already in progress for this workspace")

	// ErrLockExpired is returned when a held lock's TTL has passed and
	// it was reclaimed by another caller.
	ErrLockExpired = errors.New("backup: lock expired before release")

	// ErrDecryption is returned when AGE decryption fails (wrong
	// passphrase or corrupted payload).
	ErrDecryption = errors.New("backup: decryption failed; wrong passphrase or corrupted bundle")

	// ErrInvalidScope is returned when the scope string is neither
	// "crew", "workspace", nor "instance".
	ErrInvalidScope = errors.New("backup: invalid scope")

	// ErrIncompatibleTarget is returned on restore when the bundle's
	// compatible_targets list disallows the current target (e.g. a
	// crew bundle being restored into a different Crewship instance).
	ErrIncompatibleTarget = errors.New("backup: bundle not compatible with this target instance")

	// ErrAdminRequired is returned by RequireAdmin when the caller's
	// workspace role is not OWNER or ADMIN. Callers (CLI, HTTP
	// handlers) check via errors.Is so status-code mapping does not
	// depend on error-message substrings.
	ErrAdminRequired = errors.New("backup: admin role required")

	// ErrAgentRunning is returned by the agent-idle guard when a crew
	// in the target scope has an agent with status=running or busy.
	// The caller typically maps to HTTP 409.
	ErrAgentRunning = errors.New("backup: agent is running")

	// ErrSchemaTooOld is returned on restore when the target database
	// has not applied every migration the bundle was produced with.
	// Maps to HTTP 400 — the operator must upgrade Crewship.
	ErrSchemaTooOld = errors.New("backup: target schema is older than the bundle")

	// ErrNoOpRestore is returned when RestoreBackup completes with
	// rowsInserted == 0 despite a non-empty bundle — every primary
	// key collided with an existing row and INSERT OR IGNORE dropped
	// the lot. Callers map to a non-fatal warning so the admin can
	// re-run with --as-workspace.
	ErrNoOpRestore = errors.New("backup: restore inserted zero rows despite non-empty bundle")
)
