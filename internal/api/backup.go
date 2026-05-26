package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

// BackupHandler serves the /api/v1/admin/backups endpoints. All routes
// require workspace role OWNER or ADMIN; the router wires authed() +
// wsCtx() in front, but each handler double-checks via canRole to
// avoid accidental downgrades when a caller supplies a stale token.
//
// The handler depends on backup.DockerOps (an abstraction implemented
// by backup.MobyDockerOps) rather than the concrete *docker.Client,
// keeping the HTTP layer honest about the provider pattern and
// trivially mockable from unit tests.

type BackupHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	dockerOps backup.DockerOps // nil-safe: Create/Restore fall back to pure-DB mode
	// crewshipVersion is stamped into created manifests so restores on
	// future binaries can report what produced each bundle. Injected by
	// the router from main's build-info; empty string when unknown.
	crewshipVersion string
	// crewContainerName maps a crew slug to its Docker container name.
	// Injected by the router from the active ContainerProvider so the
	// per-instance prefix (e.g. "crewship-3-team-" on instance 3) is
	// honored — the previous hardcoded "crewship-team-" prefix broke
	// every non-default instance. Falls back to the hardcoded prefix
	// when nil so unit tests + early-init code still build.
	crewContainerName func(slug string) string
	// journal is the workspace event emitter. WriteAuditLog dual-emits
	// audit.entity_* entries when this is non-nil so backup admin
	// actions (create / delete / unlock / rotate / download) surface in
	// the unified Crew Journal alongside operational events. nil maps
	// to noopEmitter via the SetJournal setter below.
	journal journal.Emitter
}

// NewBackupHandler constructs a BackupHandler. dockerOps may be nil
// in test setups; Create/Restore then run in pure-DB mode (useful for
// restoring a workspace that has no crews with containers).

func NewBackupHandler(db *sql.DB, logger *slog.Logger, dockerOps backup.DockerOps, crewshipVersion string) *BackupHandler {
	return &BackupHandler{db: db, logger: logger, dockerOps: dockerOps, crewshipVersion: crewshipVersion, journal: noopEmitter{}}
}

// SetJournal wires a journal emitter so backup admin actions land in
// the unified Crew Journal in addition to the audit_logs table. Pass
// nil (or skip the call) to keep the existing audit_logs-only path.
func (h *BackupHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// SetCrewContainerName injects the slug→container-name mapping from the
// active ContainerProvider. Called by the router after the provider is
// known. Without this, multi-instance setups (crewship_1, _2, _3) would
// all collide on "crewship-team-<slug>" and backups would try to pause
// containers that don't exist with that name on the current instance.
func (h *BackupHandler) SetCrewContainerName(fn func(slug string) string) {
	h.crewContainerName = fn
}

// createRequest is the JSON body of POST /api/v1/admin/backups.
//
// Exactly one of Passphrase, Recipient or NoEncrypt must be set (the
// CLI enforces this before calling). Recipient is an `age1…` X25519
// public key; Passphrase is a user-supplied secret run through scrypt.

type createRequest struct {
	Scope string `json:"scope"` // "crew" or "workspace"
	// ScopeLevel selects which per-crew sections the collector
	// pulls in: "quick" (workspace + memory), "standard" (default,
	// adds /home/agent + /opt/crew-tools), or "full" (adds
	// /var/lib so service data — redis, postgresql, ... — survives
	// a wipe-and-restore cycle). Empty resolves to "standard".
	ScopeLevel string `json:"scope_level,omitempty"`
	CrewID     string `json:"crew_id,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Recipient  string `json:"recipient,omitempty"`
	NoEncrypt  bool   `json:"no_encrypt,omitempty"`
	OutputDir  string `json:"output_dir,omitempty"`
}

type createResponse struct {
	Path          string    `json:"path"`
	Size          int64     `json:"size_bytes"`
	SHA256        string    `json:"payload_sha256"`
	FormatVersion int       `json:"format_version"`
	Scope         string    `json:"scope"`
	ScopeLevel    string    `json:"scope_level,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Encrypted     bool      `json:"encrypted"`
}

// Create handles POST /api/v1/admin/backups. Runs the backup inline;
// typical durations are seconds-to-minute so no async job queue yet.

func (h *BackupHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	role := RoleFromContext(ctx)

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if user == nil || workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	scope := backup.Scope(req.Scope)
	if !scope.Valid() || scope == backup.ScopeInstance {
		replyError(w, http.StatusBadRequest, "scope must be 'crew' or 'workspace'")
		return
	}
	// Normalise + validate the trimmed crew_id so a whitespace-only
	// value is rejected server-side and the canonical form is what
	// CreateBackup sees. Direct API callers that skip the UI trim also
	// get caught by this.
	trimmedCrewID := strings.TrimSpace(req.CrewID)
	if scope == backup.ScopeCrew && trimmedCrewID == "" {
		replyError(w, http.StatusBadRequest, "crew_id required for crew scope")
		return
	}
	// Trim ONLY for the "is this a real selector or whitespace
	// padding?" gate. The actual encryption secret stays as the user
	// supplied it — Restore reads req.Passphrase verbatim, so any
	// silent rewrite here would create a bundle the same admin
	// could not decrypt afterwards. A "   "-only passphrase still
	// gets blocked because trimmedPassphrase is empty for selector
	// counting purposes.
	trimmedPassphrase := strings.TrimSpace(req.Passphrase)
	trimmedRecipient := strings.TrimSpace(req.Recipient)

	// Exactly-one encryption selector. Passphrase, Recipient, or
	// NoEncrypt — never multiple.
	encryptionSelectors := 0
	if trimmedPassphrase != "" {
		encryptionSelectors++
	}
	if trimmedRecipient != "" {
		encryptionSelectors++
	}
	if req.NoEncrypt {
		encryptionSelectors++
	}
	if encryptionSelectors == 0 {
		replyError(w, http.StatusBadRequest, "passphrase, recipient, or no_encrypt=true required")
		return
	}
	if encryptionSelectors > 1 {
		replyError(w, http.StatusBadRequest, "exactly one of passphrase / recipient / no_encrypt may be supplied")
		return
	}

	ops := h.dockerOps

	// Explicit passphrase vs recipient wire — no prefix sniffing. CLI
	// and UI send the mode they picked, server parses the matching
	// field. An accidental age1-shaped passphrase no longer gets
	// silently reinterpreted as an X25519 recipient.
	var passphrase string
	var recipients []age.Recipient
	if trimmedRecipient != "" {
		// age public keys are bech32, so trimming-then-parsing is
		// safe and saves a "spurious whitespace" failure mode.
		rec, err := age.ParseX25519Recipient(trimmedRecipient)
		if err != nil {
			replyError(w, http.StatusBadRequest, "invalid age1 recipient: "+err.Error())
			return
		}
		recipients = []age.Recipient{rec}
	} else {
		// Use the user-supplied passphrase verbatim so Restore (which
		// reads req.Passphrase unchanged) can decrypt with the exact
		// same characters the admin typed.
		passphrase = req.Passphrase
	}

	// Constrain custom output directory to live under the default so an
	// admin cannot use POST /backups as a write primitive to arbitrary
	// host paths. validateBackupPath handles the symlink + prefix
	// checks we already use for Inspect / Restore / Download.
	outputDir := req.OutputDir
	if outputDir != "" {
		if err := validateBackupPath(outputDir); err != nil {
			replyError(w, http.StatusBadRequest, "invalid output_dir: "+err.Error())
			return
		}
	}

	level := backup.ScopeLevel(strings.TrimSpace(req.ScopeLevel))
	if level == "" {
		level = backup.DefaultScopeLevel
	}
	if !level.Valid() {
		replyError(w, http.StatusBadRequest, "scope_level must be 'quick', 'standard', or 'full'")
		return
	}

	result, err := backup.CreateBackup(ctx, h.db, backup.CreateOptions{
		Scope:             scope,
		Level:             level,
		WorkspaceID:       workspaceID,
		CrewID:            trimmedCrewID,
		OutputDir:         outputDir,
		CrewshipVersion:   h.crewshipVersion,
		Actor:             backup.Actor{UserID: user.ID, Email: user.Email, Role: role},
		Passphrase:        passphrase,
		Recipients:        recipients,
		NoEncrypt:         req.NoEncrypt,
		CrewContainerName: h.resolveCrewContainerName(),
		DockerOps:         ops,
	})
	if err != nil {
		h.logger.Warn("backup create failed", "error", err, "workspace", workspaceID, "user", user.ID)
		status := statusForBackupError(err)
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	WriteAuditLog(ctx, h.db, h.journal, "backup.create", "backup", result.Path, user.ID, workspaceID, map[string]interface{}{
		"scope":          string(scope),
		"scope_level":    string(result.Manifest.ScopeLevel),
		"size_bytes":     result.Size,
		"payload_sha256": result.SHA256,
		"encrypted":      result.Manifest.Encryption.Enabled,
	})

	// Index the bundle in backup_catalog so the admin UI list view
	// does not have to filesystem-scan on every refresh. Failure to
	// catalogue does NOT fail the create — the bundle is safely on
	// disk either way; the admin just loses the fast-list affordance
	// for that row and gets back-filled on next startup scan.
	catEntry := backup.CatalogEntryFromResult(result, result.Manifest)
	if catEntry.CreatedBy == "" {
		catEntry.CreatedBy = user.Email
	}
	if err := backup.UpsertCatalogEntry(ctx, h.db, catEntry); err != nil {
		h.logger.Warn("backup catalog upsert failed", "error", err, "path", result.Path)
	}

	writeJSON(w, http.StatusCreated, createResponse{
		Path:          result.Path,
		Size:          result.Size,
		SHA256:        result.SHA256,
		FormatVersion: result.Manifest.FormatVersion,
		Scope:         string(result.Manifest.Scope),
		ScopeLevel:    string(result.Manifest.ScopeLevel),
		CreatedAt:     result.Manifest.CreatedAt,
		Encrypted:     result.Manifest.Encryption.Enabled,
	})
}

// List handles GET /api/v1/admin/backups.

type restoreRequest struct {
	Path       string `json:"path"`
	Passphrase string `json:"passphrase,omitempty"`
	// Identity is one age X25519 secret key (the "AGE-SECRET-KEY-1…"
	// string the admin printed at create-with-recipient time). When
	// the bundle was sealed with --recipient, the holder of the
	// matching identity is the only one who can decrypt; the API
	// previously exposed only Passphrase, which made age-recipient
	// bundles impossible to restore via the admin UI / REST without
	// dropping into the CLI on the host.
	Identity    string `json:"identity,omitempty"`
	AsWorkspace string `json:"as_workspace,omitempty"`
	AsCrew      string `json:"as_crew,omitempty"`
	// Replace, when true, wipes existing target rows matching the
	// bundle's workspace (by id OR slug) before INSERT. Canonical
	// disaster-recovery path: after `dev.sh nuke` the fresh-bootstrap
	// workspace has a new CUID but the same slug — --replace clears
	// the conflicting target so the bundle lands with original IDs.
	Replace bool `json:"replace,omitempty"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// Restore handles POST /api/v1/admin/backups/restore.

func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if user == nil {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req restoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		replyError(w, http.StatusBadRequest, "path required")
		return
	}
	if err := validateBackupPath(req.Path); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Existence check returns 404 before RestoreBackup gets a chance
	// to fail with "open bundle: no such file" (which maps to 500).
	// Routed through backup.Exists rather than os.Stat to preserve
	// the provider-pattern boundary that the HTTP layer MUST NOT
	// cross (per internal/**/*.go guideline).
	if _, err := backup.Exists(ctx, req.Path); err != nil {
		if errors.Is(err, backup.ErrBundleNotFound) {
			replyError(w, http.StatusNotFound, "backup not found")
			return
		}
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Cross-tenant write guard: the bundle must either match the
	// caller's current workspace (by id OR slug — slug match covers
	// the post-`dev.sh nuke` DR scenario where the fresh-bootstrap
	// workspace has a NEW CUID but the same slug as the source) OR
	// the caller's instance must have no workspace yet (canonical
	// "first restore on a fresh instance" DR path). Otherwise
	// reject with 403 so an admin can't restore another tenant's
	// bundle into their workspace context.
	//
	// Pre-fix this handler dropped the binding entirely, which
	// CodeRabbit flagged as a cross-tenant write risk (issue #594
	// comment, internal/api/backup.go:348). This re-introduces the
	// check in a way that allows DR while blocking cross-tenant
	// abuse.
	allowed, denyReason, err := allowRestore(ctx, h.db, req.Path, workspaceID)
	if err != nil {
		h.logger.Warn("backup restore authorization probe failed", "error", err, "path", req.Path)
		replyError(w, http.StatusInternalServerError, "authorize restore: "+err.Error())
		return
	}
	if !allowed {
		h.logger.Warn("backup restore denied", "reason", denyReason, "path", req.Path, "workspace_id", workspaceID, "user", user.ID)
		replyError(w, http.StatusForbidden, denyReason)
		return
	}

	ops := h.dockerOps

	var identities []age.Identity
	if id := strings.TrimSpace(req.Identity); id != "" {
		parsed, err := age.ParseX25519Identity(id)
		if err != nil {
			replyError(w, http.StatusBadRequest, "invalid age identity: "+err.Error())
			return
		}
		identities = []age.Identity{parsed}
	}

	result, err := backup.RestoreBackup(ctx, h.db, backup.RestoreOptions{
		Path:         req.Path,
		Passphrase:   req.Passphrase,
		Identities:   identities,
		AsWorkspace:  req.AsWorkspace,
		AsCrew:       req.AsCrew,
		Replace:      req.Replace,
		DryRun:       req.DryRun,
		Actor:        backup.Actor{UserID: user.ID, Email: user.Email, Role: role},
		DockerOps:    ops,
		ContainerFor: h.resolveCrewContainerName(),
	})
	if err != nil {
		h.logger.Warn("backup restore failed", "error", err, "path", req.Path, "user", user.ID)
		writeJSON(w, statusForBackupError(err), map[string]string{"error": err.Error()})
		return
	}

	// Distinct audit action for dry-runs so an admin reading the
	// audit log can tell the difference between "verified that this
	// bundle would restore" and "actually restored data". Both are
	// worth recording for compliance, but they are NOT the same
	// event — the dry-run mutated nothing in workspaces / crews /
	// agents, only this audit row itself.
	auditAction := "backup.restore"
	if req.DryRun {
		auditAction = "backup.restore.dry_run"
	}
	WriteAuditLog(ctx, h.db, h.journal, auditAction, "backup", req.Path, user.ID, workspaceID, map[string]interface{}{
		"scope":         string(result.Manifest.Scope),
		"crews_count":   result.CrewsCount,
		"rows_inserted": result.RowsInserted,
		"dry_run":       req.DryRun,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"manifest":              result.Manifest,
		"restored_ws":           result.RestoredWs,
		"restored_workspace_id": result.RestoredWorkspaceID,
		"crews_count":           result.CrewsCount,
		"rows_inserted":         result.RowsInserted,
		"docker_phase_skipped":  result.DockerPhaseSkipped,
	})
}

// allowRestore enforces the cross-tenant write guard described in
// the Restore handler above. Three allow paths:
//
//  1. Bundle's workspace ID matches the caller's current
//     workspaceID (canonical re-restore into the same workspace).
//  2. Bundle's workspace slug matches the slug of the row at the
//     caller's current workspaceID (post-nuke fresh-bootstrap
//     with the same slug under a new id — the DR scenario the
//     previous strict ID gate broke).
//  3. The instance has zero workspaces (canonical "first restore
//     on a fresh instance" — typical after `crewship start` with
//     no bootstrap completed).
//
// Returns (true, "", nil) on allow, (false, reason, nil) on deny,
// or (_, _, err) on probe failure.
func allowRestore(ctx context.Context, db *sql.DB, bundlePath, callerWorkspaceID string) (bool, string, error) {
	// Path 3: empty instance → DR escape hatch.
	var wsCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&wsCount); err != nil {
		return false, "", fmt.Errorf("count workspaces: %w", err)
	}
	if wsCount == 0 {
		return true, "", nil
	}
	// Need the bundle's workspace identity for paths 1 and 2.
	m, err := backup.Inspect(ctx, bundlePath)
	if err != nil || m == nil {
		// Defer the real error to the restore flow which gives a
		// better message; here we just deny. Guard against
		// (nil, nil) returns explicitly — current backup.Inspect
		// always pairs nil with an error, but a defensive nil
		// check is cheap and prevents a future contract change
		// from triggering a nil-pointer panic on the next line.
		return false, "could not read bundle manifest for authorization", nil
	}
	if m.Contents.Workspace == nil {
		// No workspace anchor in manifest (instance-scope or
		// custom-built dump). Defer the decision; restore will
		// fail loudly if the bundle isn't workspace-scoped.
		return true, "", nil
	}
	bundleID := m.Contents.Workspace.ID
	bundleSlug := m.Contents.Workspace.Slug
	if callerWorkspaceID != "" {
		// Path 1: ID match.
		if bundleID == callerWorkspaceID {
			return true, "", nil
		}
		// Path 2: slug match against caller's workspace row.
		var callerSlug string
		err := db.QueryRowContext(ctx, `SELECT slug FROM workspaces WHERE id = ?`, callerWorkspaceID).Scan(&callerSlug)
		if err == nil && callerSlug != "" && callerSlug == bundleSlug {
			return true, "", nil
		}
		// Unexpected probe failures must surface as 500, not 403 —
		// a deny here would mask a server-side issue and confuse
		// the operator into thinking the bundle is wrong when it's
		// actually a DB hiccup. sql.ErrNoRows is the only benign
		// case (caller's workspaceID points to a deleted row) and
		// falls through to deny normally.
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, "", fmt.Errorf("lookup caller workspace slug: %w", err)
		}
	}
	// Generic deny — deliberately does NOT echo bundleID / bundleSlug
	// back to the client. Returning those values would let an
	// unauthenticated probe enumerate workspace slugs by trying paths.
	// The real bundle identity stays in the server-side log written by
	// the caller (Restore handler logs the workspace_id at Warn level).
	return false, "bundle is not bound to your current workspace; restore on the source instance, or use a fresh instance for cross-tenant DR", nil
}

// Status handles GET /api/v1/admin/backups/status. Reports whether an
// advisory backup_lock row is currently held for the caller's workspace.
// Useful when Create returns 409 "another backup is already in progress"
// and the admin wants to know who has the lock and when it expires.

func bundleBelongsToWorkspace(ctx context.Context, path, workspaceID string) bool {
	if workspaceID == "" {
		return false
	}
	m, err := backup.Inspect(ctx, path)
	if err != nil || m == nil {
		return false
	}
	if m.Contents.Workspace != nil && m.Contents.Workspace.ID == workspaceID {
		return true
	}
	// Crew-scope bundles embed the parent workspace in the single
	// crew summary (CrewSummary doesn't carry the ws id directly, so
	// we fall through to the workspace summary match above for MVP).
	return false
}

// validateBackupPath refuses paths outside DefaultBackupsDir so these
// admin endpoints cannot be coerced into arbitrary-file read/write
// primitives. Symlinks are resolved before the prefix check so a
// malicious link under the backups dir (e.g.
// ~/.crewship/backups/evil -> /etc/passwd) cannot bypass the
// boundary once os.Open follows it. A future --allow-external-dir
// flag can relax this once a real use case appears.

func validateBackupPath(path string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("path must not contain '..'")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		return fmt.Errorf("resolve default dir: %w", err)
	}
	absDefault, err := filepath.Abs(defaultDir)
	if err != nil {
		return fmt.Errorf("resolve default dir: %w", err)
	}
	// Reject symlinks outright: a Crewship backup file is always a
	// regular file written by us, never a symlink. Lstat probes the
	// link itself, not its target.
	if info, err := os.Lstat(absPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path must not be a symlink")
		}
	}
	// Resolve symlinks on both sides so the prefix check compares the
	// canonical paths. For a path that does not yet exist (e.g. the
	// final file name during Create), EvalSymlinks errors — walk up
	// the ancestry to the nearest existing directory, resolve that,
	// and re-append the trailing segments. Apple's /var → /private/var
	// redirect is the most common reason this matters.
	resolvedPath := resolveExistingAncestor(absPath)
	resolvedDefault := absDefault
	if rd, err := filepath.EvalSymlinks(absDefault); err == nil {
		resolvedDefault = rd
	}
	if !strings.HasPrefix(resolvedPath, resolvedDefault+string(filepath.Separator)) &&
		resolvedPath != resolvedDefault {
		return fmt.Errorf("path must live under %s", resolvedDefault)
	}
	return nil
}

// resolveExistingAncestor returns the symlink-resolved form of p using
// the closest existing directory on its way up. A leaf that does not
// yet exist still gets a canonical prefix so validateBackupPath can
// compare against a resolved default directory. When every ancestor
// fails EvalSymlinks we return the original absolute path unchanged.

func resolveExistingAncestor(p string) string {
	for dir := p; dir != "/" && dir != "." && dir != ""; dir = filepath.Dir(dir) {
		if rp, err := filepath.EvalSymlinks(dir); err == nil {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return p
			}
			return filepath.Join(rp, rel)
		}
	}
	return p
}

// Metrics handles GET /api/v1/admin/backups/metrics. Returns the
// current in-memory counter snapshot — process-lifetime counters
// that reset on restart. The snapshot is PROCESS-WIDE (cross-workspace
// lock-held map, global counters), so workspace admins must not see
// it: the endpoint is gated to the instance-level OWNER
// (CREWSHIP_OWNER_EMAIL env) only.

func statusForBackupError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch {
	case errors.Is(err, backup.ErrAdminRequired):
		return http.StatusForbidden
	case errors.Is(err, backup.ErrAgentRunning),
		errors.Is(err, backup.ErrLockHeld):
		return http.StatusConflict
	case errors.Is(err, backup.ErrFormatTooNew),
		errors.Is(err, backup.ErrFormatTooOld),
		errors.Is(err, backup.ErrSchemaTooOld),
		errors.Is(err, backup.ErrInvalidManifest),
		errors.Is(err, backup.ErrInvalidScope):
		return http.StatusBadRequest
	case errors.Is(err, backup.ErrDecryption),
		errors.Is(err, backup.ErrInvalidChecksum):
		return http.StatusBadRequest
	case errors.Is(err, backup.ErrNoOpRestore):
		// The DB was never mutated; surface as 409 so the client sees
		// "nothing to do" rather than "internal error".
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// resolveCrewContainerName returns the slug→container-name function
// the backup runner should use. Prefers the injected mapping (set by
// the router from the active ContainerProvider, so multi-instance
// prefixes like "crewship-3-team-" are honored), falls back to the
// default "crewship-team-" prefix only when no provider is wired —
// keeps unit tests + early-init code paths building without forcing
// every test to construct a provider stub.
func (h *BackupHandler) resolveCrewContainerName() func(slug string) string {
	if h.crewContainerName != nil {
		return h.crewContainerName
	}
	return func(slug string) string { return "crewship-team-" + slug }
}

// selfTestRequest is the JSON body of POST /api/v1/admin/backups/self-test.
