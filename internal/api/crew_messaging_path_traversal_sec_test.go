package api

// Path-traversal proofs for the LIVE crew-shared file surface —
// GET/POST /api/v1/internal/crew-files/{crewId} (CrewMessagingHandler
// ReadFile / WriteFile, wired in router_internal.go).
//
// PR #1569 fixed the same two classes in internal/provider/localfs and
// internal/fileserver. fileserver.Server is not wired to any route, so
// that half of the fix is latent; these handlers are the ones serving
// traffic. Each test asserts the EFFECT of the escape (bytes landing on,
// or leaking out of, a file outside the crew's shared tree), never just
// "an error came back" — a status-code-only assertion would pass against
// a handler that refuses for the wrong reason.
//
// Classes covered:
//
//	1. create-path symlink following — the destination leaf does not exist
//	   yet, so EvalSymlinks can never see it; os.Create follows a symlink an
//	   agent planted at that leaf. Agent containers own /crew/shared at uid
//	   1001 on a shared bind-mount, so planting it is inside the threat model.
//	2. identifier validation — the crew id was joined into the shared-dir
//	   path unvalidated, and every containment check is relative to that same
//	   crew directory, so the check's own base moves with the attacker's input.
//	3. mkdir-before-check — MkdirAll ran on the unresolved destination dir
//	   before the containment check, creating directories outside the tree.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secMsgRig is covMsgRig with an explicit nested storage root, so a test
// can point at a directory that is unambiguously OUTSIDE the storage tree
// (siblings of storageDir under the same base).
func secMsgRig(t *testing.T) (h *CrewMessagingHandler, db *sql.DB, base, storageDir, fromCrew, toCrew string) {
	t.Helper()
	db = setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	base = t.TempDir()
	storageDir = filepath.Join(base, "storage")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	h = NewCrewMessagingHandler(db, storageDir, newTestLogger())

	fromCrew, toCrew = "sec-from", "sec-to"
	seedCrewRow(t, db, fromCrew, wsID, "From", "sec-from")
	seedCrewRow(t, db, toCrew, wsID, "To", "sec-to")
	if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('sec-conn', ?, ?, ?, 'bidirectional', 'active')`, wsID, fromCrew, toCrew); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	return h, db, base, storageDir, fromCrew, toCrew
}

// Class 1 — the create path follows a symlink planted at the destination
// LEAF. The dir containment check passes (incoming/<requester> is a real
// directory inside the shared tree); only the final component is the link,
// and it is the component EvalSymlinks was never asked about.
//
// Effect asserted: a file outside the storage tree is overwritten with the
// uploaded bytes.
func TestSecCrewFilesWrite_LeafSymlinkOverwritesFileOutsideSharedTree(t *testing.T) {
	h, _, base, storageDir, fromCrew, toCrew := secMsgRig(t)

	dest := filepath.Join(storageDir, "crews", toCrew, "shared", "incoming", fromCrew)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	victim := filepath.Join(base, "victim.txt")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	// The crew's own agent (uid 1001, owner of /crew/shared) plants this.
	if err := os.Symlink(victim, filepath.Join(dest, "report.txt")); err != nil {
		t.Fatalf("plant leaf symlink: %v", err)
	}

	rec := httptest.NewRecorder()
	h.WriteFile(rec, covMsgUpload(t, toCrew, fromCrew, "report.txt", "PWNED", true))

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != "ORIGINAL" {
		t.Fatalf("ESCAPE: file outside the storage tree was rewritten through a leaf symlink; %s = %q (status %d, body %s)",
			victim, got, rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusCreated {
		t.Errorf("status = 201 on a symlinked destination leaf; want a refusal")
	}
}

// Class 1, ownership variant — even when the symlink target is only
// chown'd and not rewritten, the handler hands uid 1001 ownership of a
// file outside the tree. Covered by the same refusal, asserted separately
// so a fix that blocks the write but keeps the chown still fails.
func TestSecCrewFilesWrite_LeafSymlinkDoesNotCreateOutsideTree(t *testing.T) {
	h, _, base, storageDir, fromCrew, toCrew := secMsgRig(t)

	dest := filepath.Join(storageDir, "crews", toCrew, "shared", "incoming", fromCrew)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	// Leaf symlink to a path that does not exist yet: os.Create through it
	// CREATES the target, so the escape lands even with nothing to overwrite.
	if err := os.Symlink(filepath.Join(outside, "planted.txt"), filepath.Join(dest, "drop.txt")); err != nil {
		t.Fatalf("plant leaf symlink: %v", err)
	}

	rec := httptest.NewRecorder()
	h.WriteFile(rec, covMsgUpload(t, toCrew, fromCrew, "drop.txt", "PWNED", true))

	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatalf("ESCAPE: a new file was created outside the storage tree at %s (status %d, body %s)",
			filepath.Join(outside, "planted.txt"), rec.Code, rec.Body.String())
	}
}

// Class 3 — MkdirAll ran before the containment check, so a symlinked
// intermediate component let the handler mkdir outside the tree even
// though the subsequent check refused the write.
//
// Effect asserted: no directory is created outside the storage tree.
func TestSecCrewFilesWrite_SymlinkedParentMkdirsOutsideStorageTree(t *testing.T) {
	h, _, base, storageDir, fromCrew, toCrew := secMsgRig(t)

	shared := filepath.Join(storageDir, "crews", toCrew, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatalf("mkdir shared: %v", err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	// "incoming" itself is the escaping link; the per-requester dir under it
	// does not exist yet, so MkdirAll is what walks through the link.
	if err := os.Symlink(outside, filepath.Join(shared, "incoming")); err != nil {
		t.Fatalf("plant dir symlink: %v", err)
	}

	rec := httptest.NewRecorder()
	h.WriteFile(rec, covMsgUpload(t, toCrew, fromCrew, "f.txt", "data", true))

	if _, err := os.Stat(filepath.Join(outside, fromCrew)); err == nil {
		t.Fatalf("ESCAPE: directory created outside the storage tree at %s (status %d, body %s)",
			filepath.Join(outside, fromCrew), rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusCreated {
		t.Errorf("status = 201 through an escaping directory symlink; want a refusal")
	}
}

// Class 2 — the crew id is joined straight into the shared-dir path, and
// every containment check is taken against that same crew directory. An id
// of "../../<anything>" therefore moves the check's own base along with the
// target and passes trivially (this is bug #2 of PR #1569, in the handler
// that actually serves traffic).
//
// NOT reachable end-to-end today, and the test says so rather than
// pretending otherwise: both ReadFile and WriteFile gate on canCommunicate,
// which needs a crew_connections row, and crew_connections.to_crew_id
// carries a FOREIGN KEY onto crews.id — a traversal id has no crews row, so
// the row cannot exist (the create endpoint additionally re-checks both
// crews are in the workspace). The FK is the wall, and it is a wall in a
// different subsystem than the one doing the joining.
//
// So the proof is taken one layer down, at the resolver, where the effect is
// unambiguous: the returned path lands outside the storage root the handler
// is supposed to be confined to.
func TestSecCrewFilesResolve_CrewIDTraversalEscapesStorageRoot(t *testing.T) {
	h, _, base, storageDir, _, _ := secMsgRig(t)

	// storagePath/crews/../../outside/shared → base/outside/shared
	evilCrewID := "../../outside"
	loot := filepath.Join(base, "outside", "shared")
	if err := os.MkdirAll(loot, 0o755); err != nil {
		t.Fatalf("mkdir loot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(loot, "secret.txt"), []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatalf("seed loot: %v", err)
	}

	target, errStr := h.resolveCrewSharedPath(evilCrewID, "secret.txt", false)
	defer target.Close()
	if errStr == "" && !strings.HasPrefix(filepath.Clean(target.abs), filepath.Clean(storageDir)+string(filepath.Separator)) {
		t.Fatalf("ESCAPE: crew id %q resolved to %s, outside the storage root %s",
			evilCrewID, target.abs, storageDir)
	}
}

// Identifier-validation table: every shape safepath.ValidateComponent
// rejects must be refused before the id reaches a path join. Pins the
// contract so a future refactor cannot quietly reopen class 2.
func TestSecCrewFilesResolve_RejectsUnsafeCrewIDs(t *testing.T) {
	h, _, _, _, _, _ := secMsgRig(t)

	for _, id := range []string{"", ".", "..", "../..", "../../outside", "a/b", `a\b`, "/abs", "x\x00y"} {
		t.Run("id="+strings.ReplaceAll(id, "\x00", "NUL"), func(t *testing.T) {
			target, errStr := h.resolveCrewSharedPath(id, "f.txt", false)
			defer target.Close()
			if errStr != "invalid crew id" {
				t.Fatalf("resolveCrewSharedPath(%q) → err %q, want %q", id, errStr, "invalid crew id")
			}
			if target != nil {
				t.Fatalf("resolveCrewSharedPath(%q) returned a usable target for an unsafe crew id: %s", id, target.abs)
			}
		})
	}
}
