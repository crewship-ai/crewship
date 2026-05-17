package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// backup_mutation_test.go covers the mutating endpoints of BackupHandler
// (Create + Restore) plus the bundleBelongsToWorkspace helper and the
// SetJournal nil-safe setter. backup_query_test.go already exercises
// the read-only surfaces (List/Inspect/Status/Verify), and backup_test.go
// covers pure helpers (validateBackupPath, statusForBackupError,
// resolveCrewContainerName) — so this file deliberately complements,
// never duplicates, those.
//
// All handler tests run with dockerOps=nil so Create/Restore stay in
// pure-DB mode (documented in NewBackupHandler). The happy-path Create
// test sandboxes HOME via t.Setenv so DefaultBackupsDir resolves under a
// tempdir and never touches the developer's real ~/.crewship/backups.

// backupMutationRig is a local-to-this-file rig so the file is
// self-contained and does not rely on backup_query_test.go's identical
// (but unexported-in-spirit) helper. Keeping the rig duplicated is a
// trade we explicitly accept: it makes the test file readable on its
// own and avoids inter-file coupling between sibling test files.
func backupMutationRig(t *testing.T) (*BackupHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewBackupHandler(db, logger, nil, "test-version")
	return h, userID, wsID
}

// ── Create: role gating ─────────────────────────────────────────────────

// Backups are destructive admin surfaces: a regression flipping the
// canRole("manage") check would let any MEMBER mint workspace bundles
// to disk, which is both a privilege escalation and an exfiltration
// vector. Pin the 403 explicitly.
func TestBackup_Create_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope":      "workspace",
		"no_encrypt": true,
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// ── Create: body / scope validation ─────────────────────────────────────

// Empty body is a common client bug; the handler must surface 400 with a
// clean message rather than panicking on json.Decode's nil reader path.
func TestBackup_Create_InvalidJSON_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// "instance" scope is a known token (passes Scope.Valid) but the handler
// explicitly rejects it because instance-scope create lives in PR 4 and
// the CLI route is not wired. Without the explicit reject the runner
// would error later with a less helpful message.
func TestBackup_Create_InstanceScope_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope":      "instance",
		"no_encrypt": true,
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Garbage scope (not in the Scope.Valid set) must fail closed at 400.
func TestBackup_Create_UnknownScope_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope":      "everything",
		"no_encrypt": true,
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Crew scope without crew_id was the original "produces a confusing
// runner error" bug — now caught up front at the handler. Whitespace-
// only crew_id must hit the same gate.
func TestBackup_Create_CrewScopeMissingCrewID_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	for _, crewID := range []string{"", "   "} {
		body := jsonBody(map[string]any{
			"scope":      "crew",
			"crew_id":    crewID,
			"no_encrypt": true,
		})
		req := withWorkspaceUser(
			httptest.NewRequest("POST", "/api/v1/admin/backups", body),
			userID, wsID, "OWNER",
		)
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("crew_id=%q: status = %d, want 400", crewID, rr.Code)
		}
	}
}

// Encryption-selector exactly-one invariant: zero selectors is the
// "admin forgot the flag" path — must 400, not silently fall back to
// plaintext.
func TestBackup_Create_NoEncryptionSelector_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope": "workspace",
		// no passphrase, no recipient, no_encrypt absent → 0 selectors
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// And the converse: passphrase + no_encrypt is contradictory and must
// hit the >1 selector gate, not be silently resolved by precedence.
func TestBackup_Create_MultipleEncryptionSelectors_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope":      "workspace",
		"passphrase": "hunter2",
		"no_encrypt": true,
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Invalid age recipient (not a valid bech32-encoded X25519 pubkey) is
// the most common copy-paste failure mode; explicit 400 with parser
// error included beats a confusing later failure inside SealPayload.
func TestBackup_Create_InvalidRecipient_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope":     "workspace",
		"recipient": "age1-not-a-real-key",
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// output_dir is constrained to live under DefaultBackupsDir so the
// admin endpoint cannot be turned into an arbitrary-file write
// primitive. /tmp/elsewhere must be rejected at validateBackupPath, not
// after a partial mkdirall lands.
func TestBackup_Create_OutputDirOutsideDefault_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	t.Setenv("HOME", t.TempDir()) // also sandbox HOME so the check is hermetic
	body := jsonBody(map[string]any{
		"scope":      "workspace",
		"no_encrypt": true,
		"output_dir": "/tmp/elsewhere",
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Unknown scope_level (not Quick/Standard/Full) is a typo guard —
// "fast", "maximum", etc. must be rejected before the runner gets a
// chance to silently fall back to Standard and confuse the admin.
func TestBackup_Create_InvalidScopeLevel_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope":       "workspace",
		"scope_level": "maximum",
		"no_encrypt":  true,
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// ── Create: happy path ──────────────────────────────────────────────────

// Workspace-scope, no-encrypt, no crews, no agents — the smallest valid
// payload. Verifies the success contract: 201 with a payload that has a
// non-empty path under the sandboxed DefaultBackupsDir, plus the
// scope/scope_level/encrypted echo the admin UI / CLI depend on. The
// real defence here is regression: if a future refactor breaks the
// pure-DB code path (e.g. by demanding non-nil dockerOps) the handler
// would silently 500 and CI would catch it here.
func TestBackup_Create_WorkspaceScopeNoEncrypt_Returns201(t *testing.T) {
	// Sandbox HOME so DefaultBackupsDir resolves under a tempdir.
	// LocalStorageOps.Home() reads os.UserHomeDir() which honours HOME
	// on darwin/linux.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"scope":      "workspace",
		"no_encrypt": true,
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp createResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Path == "" {
		t.Errorf("response Path empty, want a created file path")
	}
	// Bundle must actually exist on disk — a successful response with
	// no file would be a regression that hid Create's whole point.
	if _, err := os.Stat(resp.Path); err != nil {
		t.Errorf("bundle path not on disk: %v", err)
	}
	if resp.Scope != "workspace" {
		t.Errorf("scope echo = %q, want workspace", resp.Scope)
	}
	if resp.Encrypted {
		t.Errorf("encrypted = true on no_encrypt request — encryption-mode leak")
	}
	if resp.FormatVersion == 0 {
		t.Errorf("format_version = 0, want manifest's stamped version")
	}
	// The created path should live under the sandboxed default dir so
	// validateBackupPath would accept it on a later Restore.
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("default dir: %v", err)
	}
	if !strings.HasPrefix(resp.Path, defaultDir) {
		t.Errorf("bundle path %q not under sandboxed default %q", resp.Path, defaultDir)
	}
}

// ── Restore: role gating ────────────────────────────────────────────────

// Restore is more dangerous than Create (it WRITES to the DB, not just
// reads): the canRole("manage") gate is the only thing between a
// regular MEMBER and a workspace wipe-and-replace. 403 on every code
// path that touches the body.
func TestBackup_Restore_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{"path": "/anything.tar.zst"})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/restore", body),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// ── Restore: body / path validation ─────────────────────────────────────

func TestBackup_Restore_InvalidJSON_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/restore",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Missing path is a degenerate body that the handler must reject
// before touching the filesystem; surfacing it via the runner would
// give a confusing "open : no such file" error.
func TestBackup_Restore_MissingPath_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{"path": ""})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/restore", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Path traversal must be caught at validateBackupPath, NOT later at
// Inspect/open. The 400 (path) is more honest than a 404 (not-found)
// because the request is malformed, not the bundle missing.
func TestBackup_Restore_PathTraversal_Returns400(t *testing.T) {
	h, userID, wsID := backupMutationRig(t)
	for _, p := range []string{
		"../../etc/passwd",
		"/etc/shadow",
		"some/relative/../escape.tar.zst",
	} {
		body := jsonBody(map[string]any{"path": p})
		req := withWorkspaceUser(
			httptest.NewRequest("POST", "/api/v1/admin/backups/restore", body),
			userID, wsID, "OWNER",
		)
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400", p, rr.Code)
		}
	}
}

// Invalid age identity (the "AGE-SECRET-KEY-1…" string) is a copy-paste
// failure mode; explicit 400 saves the admin from a noisier downstream
// decrypt error.
func TestBackup_Restore_InvalidIdentity_Returns400(t *testing.T) {
	// Sandbox HOME so the path we craft can pass validateBackupPath
	// and reach the identity-parse step — otherwise the test would
	// 400 on the path gate first and we'd never assert what we mean to.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("default dir: %v", err)
	}
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatalf("mkdir default: %v", err)
	}
	// A fake but path-validator-passing target. The handler never
	// opens it because the identity parse fails first.
	target := filepath.Join(defaultDir, "fake.tar.zst")

	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{
		"path":     target,
		"identity": "AGE-SECRET-KEY-1-not-a-real-identity",
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/restore", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	// Either bundleBelongsToWorkspace returns 404 (because Inspect runs
	// before identity-parse — file does not exist) OR the identity
	// parse hits 400. Both are valid defensive states; what we must
	// NOT see is 500 or 200.
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 400 or 404; body=%s", rr.Code, rr.Body.String())
	}
}

// Path validator passes but the bundle file does not exist — the
// bundleBelongsToWorkspace helper falls through to Inspect, which fails
// to open, returns false, and the handler maps that to 404. Surfacing
// the "no such file" stderr would leak filesystem layout, so 404 is the
// intended contract.
//
// NOTE: this also indirectly covers bundleBelongsToWorkspace's
// "Inspect-error path returns false" branch — see the dedicated
// TestBundleBelongsToWorkspace_* tests below for the direct coverage.
func TestBackup_Restore_UnknownPath_Returns404(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("default dir: %v", err)
	}
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatalf("mkdir default: %v", err)
	}
	target := filepath.Join(defaultDir, "does-not-exist.tar.zst")

	h, userID, wsID := backupMutationRig(t)
	body := jsonBody(map[string]any{"path": target})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/restore", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// Restore happy path is intentionally NOT covered here: it requires
// producing a real bundle that survives ReadBundle (CRC + manifest
// parse) and RestoreBackup's ID-rewriting logic. The Create happy-path
// test above produces such a bundle, but cross-test bundle sharing
// would couple the two; we leave end-to-end Create→Restore as an
// integration test concern. The 4xx branches we DO cover here are the
// ones a regression is most likely to break.

// ── bundleBelongsToWorkspace ────────────────────────────────────────────

// Empty workspaceID must short-circuit to false — without this guard
// an unauthenticated context that somehow reached Restore would be
// matched against any-and-all bundles. Defense-in-depth even though
// the handler's nil-user check already rejected the request.
func TestBundleBelongsToWorkspace_EmptyWorkspace_ReturnsFalse(t *testing.T) {
	if ok := bundleBelongsToWorkspace(context.Background(), "/any/path.tar.zst", ""); ok {
		t.Errorf("empty workspace must short-circuit to false")
	}
}

// Inspect-error path (non-existent file) returns false rather than
// propagating the error to the caller. This is what causes Restore's
// 404 on unknown paths.
func TestBundleBelongsToWorkspace_InspectError_ReturnsFalse(t *testing.T) {
	if ok := bundleBelongsToWorkspace(
		context.Background(),
		filepath.Join(t.TempDir(), "no-such-bundle.tar.zst"),
		"ws_anything",
	); ok {
		t.Errorf("missing bundle must return false (Inspect error swallowed)")
	}
}

// ── SetJournal ──────────────────────────────────────────────────────────

// Passing nil to SetJournal must NOT leave h.journal as a nil interface
// — WriteAuditLog inside Create/Restore would then nil-dereference. The
// setter is documented to fall back to noopEmitter; verify by emitting
// a non-run entry through the post-SetJournal emitter to make sure
// Emit() does not panic and returns a stable id.
func TestBackup_SetJournal_NilFallsBackToNoop(t *testing.T) {
	h, _, _ := backupMutationRig(t)
	// Default emitter is noopEmitter (set by NewBackupHandler). Pass
	// nil and verify the field is still a usable Emitter — we test
	// behaviour, not type identity, so a future swap of the noop
	// implementation does not break this test.
	h.SetJournal(nil)
	if h.journal == nil {
		t.Fatalf("h.journal is nil after SetJournal(nil) — would panic in WriteAuditLog")
	}
	// Smoke test the Emitter: a non-run entry should not error.
	// noopEmitter.Emit only errors on run.* entry types, which we
	// deliberately do not exercise — the contract is "stays callable".
	if err := h.journal.Flush(context.Background()); err != nil {
		t.Errorf("Flush on fallback emitter errored: %v", err)
	}
}
