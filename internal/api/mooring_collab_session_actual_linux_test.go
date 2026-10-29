//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual child, FULL SQL and canonical original human-source checks. The
// authenticated context is the isolated host fixture, not a signed HTTP proof.
// No raw participant transport, content send, Saved or deployed UI is claimed.
func TestCollaborationSessionActualActivationOwnsOriginalHumanStore(t *testing.T) {
	runCollaborationSessionActualStartup(t, false)
}

func TestCollaborationSessionActualInitializeSourceABARollsBack(t *testing.T) {
	runCollaborationSessionActualStartup(t, true)
}

func runCollaborationSessionActualStartup(t *testing.T, initializeSourceABA bool) {
	pinned := func(pathEnv, hashEnv string, limit int64) []byte {
		t.Helper()
		path, hash := os.Getenv(pathEnv), os.Getenv(hashEnv)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			t.Fatalf("bounded pinned %s missing: %v", pathEnv, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != hash {
			t.Fatalf("%s digest mismatch", pathEnv)
		}
		return raw
	}
	pinned("MOORING_SESSION_BINARY", "MOORING_SESSION_BINARY_SHA256", 64<<20)
	checkpoint := pinned("MOORING_SESSION_CHECKPOINT", "MOORING_SESSION_CHECKPOINT_SHA256", 4<<20)
	rig, unit, _, human, _ := collaborationAdmissionFixture(t)
	mustExec(t, rig.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	owners := mooring.NewOwnerStore(rig.db, true, clock, time.Minute)
	versions := mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	runtime, err := mooring.StartSessionRuntime(t.Context(), mooring.SessionRuntimeConfig{Runtime: mooring.RuntimeConfig{Enabled: true, Binary: os.Getenv("MOORING_SESSION_BINARY"), Versions: versions, ControlProfile: mooring.PrivateSessionProfile}, Scope: unit.scope})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	cell, err := newCollaborationSessionStore(true, unit.pages, owners, unit.fence, runtime, unit.scope, versions)
	if err != nil {
		t.Fatal(err)
	}
	wireScope := protocol.Scope{WorkspaceID: protocol.Identifier(unit.scope.WorkspaceID), ArtifactID: protocol.Identifier(unit.scope.ArtifactID), ApplicationID: "wordcraft"}
	wireVersions := protocol.Versions{ProtocolVersion: versions.Protocol, ModelVersion: versions.Model, EngineVersion: versions.Engine}
	sessionID := "actual-human-owned-session"
	open := protocol.SessionOpen{Type: "session_open", RequestID: "human-open", ControlProfile: mooring.PrivateSessionProfile, Scope: wireScope, SessionID: protocol.Identifier(sessionID), RuntimeRef: protocol.Identifier(runtime.RuntimeRef()), Versions: wireVersions, RequiredCapabilities: []protocol.SessionOpenRequiredCapabilitiesItem{protocol.SessionOpenRequiredCapabilitiesItemCheckpointIdentityV1}}
	opened := make(chan error, 1)
	openStarted := time.Now()
	go func() { _, e := runtime.Open(t.Context(), open); opened <- e }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	control, err := runtime.NextControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := control.Frame()
	if err != nil {
		t.Fatal(err)
	}
	var acquire protocol.OwnerAcquire
	if json.Unmarshal(raw, &acquire) != nil || acquire.Type != "owner_acquire" || acquire.Scope != wireScope || acquire.RuntimeRef != open.RuntimeRef {
		t.Fatal("uncorrelated owner acquisition")
	}
	owner, err := owners.Acquire(ctx, unit.scope, runtime.RuntimeRef())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/host-only-initialize", nil).WithContext(human)
	if initializeSourceABA {
		mustExec(t, rig.db, `CREATE TRIGGER test_session_initialize_source_aba AFTER INSERT ON mooring_collab_audit WHEN NEW.kind='initialized'
	 BEGIN UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='admission-source';
	 UPDATE user_sessions SET revoked_at=NULL WHERE id='admission-source'; END`)
		if err = cell.initialize(request, owner, checkpoint); err == nil {
			t.Fatal("source ABA inside initialization committed")
		}
		var initialized int
		if err = rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_documents WHERE page_id=?`, unit.scope.ArtifactID).Scan(&initialized); err != nil || initialized != 0 {
			t.Fatal("failed source initialization left a document", err)
		}
		return
	}
	if err = cell.initialize(request, owner, checkpoint); err != nil {
		t.Fatal("original human initialization", err)
	}
	if _, _, err = cell.store.ReadCheckpoint(human, unit.scope); err == nil {
		t.Fatal("unbound startup store granted a reader")
	}
	var permission string
	if err = rig.db.QueryRow(`SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=?`, unit.scope.WorkspaceID).Scan(&permission); err != nil {
		t.Fatal(err)
	}
	grant := protocol.OwnerGrant{Type: "owner_grant", RequestID: acquire.RequestID, Scope: wireScope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(owner.Generation, 10)), Token: owner.Token}, LeaseDurationMs: "60000", Revision: "0", RenewalNonce: acquire.RenewalNonce, PermissionVersion: protocol.Counter(permission)}
	raw, _ = json.Marshal(grant)
	if err = runtime.ReplyControl(ctx, control, raw); err != nil {
		t.Fatal(err)
	}
	snapshot, err := cell.startupCheckpoint(request, owner)
	if err != nil {
		t.Fatal("original authenticated startup checkpoint", err)
	}
	targets := make([]protocol.TargetVersion, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		targets = append(targets, protocol.TargetVersion{TargetID: protocol.Identifier(target.TargetID), Revision: protocol.Counter(target.Revision), StructureRevision: protocol.Counter(target.StructureRevision), Deleted: target.Deleted})
	}
	restore := protocol.SessionRestore{Type: "session_restore", RequestID: "human-restore", ControlProfile: mooring.PrivateSessionProfile, Scope: wireScope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: grant.Fence, Versions: wireVersions, Targets: targets, Checkpoint: snapshot.Checkpoint}
	t.Logf("startup before restore elapsed=%s checkpoint_bytes=%d", time.Since(openStarted), len(checkpoint))
	if _, err = runtime.Restore(ctx, restore); err != nil {
		t.Fatalf("restore: %v; runtime: %v; elapsed: %s", err, runtime.Err(), time.Since(openStarted))
	}
	if err = <-opened; err != nil {
		t.Fatal(err)
	}
	// A separate current reader can restore the service without write rights;
	// revoking and restoring that reader's credential must not revive its source.
	mustExec(t, rig.db, `INSERT INTO users(id,email) VALUES('startup-reader','startup-reader@synthetic.invalid')`)
	mustExec(t, rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('startup-reader-member',?,'startup-reader','MEMBER')`, unit.scope.WorkspaceID)
	mustExec(t, rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','startup-reader','read',?)`, unit.scope.ArtifactID, rig.userID)
	expiry := time.Now().Add(time.Hour)
	mustExec(t, rig.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('startup-reader-source','startup-reader',?)`, expiry.UTC().Format(time.RFC3339Nano))
	readerSource, err := captureOriginalHumanCredential(t.Context(), rig.db, &AuthUser{ID: "startup-reader", SessionID: "startup-reader-source", CredentialExpiresAt: expiry}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	reader := context.WithValue(t.Context(), ctxUser, &AuthUser{ID: "startup-reader", SessionID: "startup-reader-source", OriginalCredential: readerSource})
	reader = auth.WithOriginalCredential(context.WithValue(reader, ctxAuthKind, AuthKindSession), *readerSource)
	reader = collaborationIngressTestContext(t, unit.fence, reader)
	readerRequest := httptest.NewRequest("POST", "/host-only-startup-read", nil).WithContext(reader)
	if _, err = cell.startupCheckpoint(readerRequest, owner); err != nil {
		t.Fatal("current read-only startup reader denied", err)
	}
	mustExec(t, rig.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='startup-reader-source'`)
	mustExec(t, rig.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='startup-reader-source'`)
	if snapshot, err := cell.startupCheckpoint(readerRequest, owner); err == nil || snapshot.Checkpoint.State != "" {
		t.Fatal("revoked/restored startup source read retained content", err)
	}
	// Preactivation authority has a private purpose and a bounded lifetime.
	var escaped context.Context
	if err = cell.runStartup(request, owner, "read_checkpoint", func(bound context.Context) error {
		escaped = bound
		copied := *cell.store
		if _, err := copied.ReadFullCheckpoint(bound, owner); err == nil {
			t.Fatal("copied store borrowed preactivation authority")
		}
		if err := cell.store.Initialize(bound, owner, checkpoint); err == nil {
			t.Fatal("startup read context borrowed initialize authority")
		}
		_, err := cell.store.ReadFullCheckpoint(bound, owner)
		return err
	}); err != nil {
		t.Fatal("scoped startup read", err)
	}
	if _, err = cell.store.ReadFullCheckpoint(escaped, owner); err == nil {
		t.Fatal("escaped startup context retained authority")
	}
	if err = cell.runStartup(request, owner, "initialize", func(bound context.Context) error {
		if _, err := cell.store.ReadFullCheckpoint(bound, owner); err == nil {
			t.Fatal("initialize context borrowed startup read authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	foreignOwner := owner
	foreignOwner.OwnerID = "different-process"
	if _, err = cell.startupCheckpoint(request, foreignOwner); err == nil {
		t.Fatal("different process owner read startup state")
	}
	activation, err := runtime.Activate(ctx, cell.store, owner, restore)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newCollaborationSessionHumanRegistry(ctx, cell, activation, owner, clock, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cell.startupCheckpoint(request, owner); err == nil {
		t.Fatal("bound cell reopened preactivation read authority")
	}
	if registry.hostSessionID() != sessionID || registry.runtime != runtime {
		t.Fatal("actual startup identity replaced")
	}
	if _, err = newCollaborationSessionHumanRegistry(ctx, cell, activation, owner, clock, 8); err == nil {
		t.Fatal("one original store bound twice")
	}
	ticket, admission, err := registry.issue(human, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.join(ctx, runtime, owner, ticket, "actual-session-human-join"); err != nil {
		t.Fatal(err)
	}
	read := func() error {
		return registry.runHumanOperation(request, admission.participant, func(bound context.Context) error {
			_, actual, e := cell.store.ReadCheckpoint(bound, unit.scope)
			if e == nil && !bytes.Equal(actual, checkpoint) {
				t.Fatal("checkpoint replaced")
			}
			return e
		})
	}
	if err = read(); err != nil {
		t.Fatal("current reader denied", err)
	}
	copiedStore := *cell.store
	if err = registry.runHumanOperation(request, admission.participant, func(bound context.Context) error { _, _, e := copiedStore.ReadCheckpoint(bound, unit.scope); return e }); err == nil {
		t.Fatal("copied store borrowed original cell authority")
	}
	foreignCell, err := newCollaborationSessionStore(true, unit.pages, owners, unit.fence, runtime, unit.scope, versions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = newCollaborationSessionHumanRegistry(ctx, foreignCell, activation, owner, clock, 8); err == nil {
		t.Fatal("actual activation bound a different initialized store")
	}
	if err = read(); err != nil {
		t.Fatal("rejected foreign binding poisoned original store", err)
	}
	mustExec(t, rig.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='admission-source'`)
	mustExec(t, rig.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='admission-source'`)
	if err = read(); err == nil {
		t.Fatal("original source ABA revived admitted reader")
	}
	runtime.Close()
	if _, _, err = registry.issue(human, owner, false); err == nil {
		t.Fatal("dead actual runtime issued fresh admission")
	}
}
