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

// Actual child/store/activation and independent canonical human credentials.
// The host contexts are isolated fixtures, not signed HTTP or raw participant
// proofs. This covers restore/read and author decisions, not native Saved/D3.
func TestCollaborationAuthorActualSessionKeepsIndependentOriginalRights(t *testing.T) {
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
	activation, err := runtime.Activate(ctx, cell.store, owner, restore)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newCollaborationSessionHumanRegistry(ctx, cell, activation, owner, clock, 8)
	if err != nil {
		t.Fatal(err)
	}
	if registry.hostSessionID() != sessionID || registry.runtime != runtime {
		t.Fatal("actual startup identity replaced")
	}
	if _, err = newCollaborationSessionHumanRegistry(ctx, cell, activation, owner, clock, 8); err == nil {
		t.Fatal("one original store bound twice")
	}
	// Add an independent MEMBER whose only Page reach is this explicit grant.
	mustExec(t, rig.db, `INSERT INTO users(id,email) VALUES('actual-author-jana','actual-author-jana@synthetic.invalid')`)
	mustExec(t, rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('actual-author-jana-member',?,'actual-author-jana','MEMBER')`, unit.scope.WorkspaceID)
	mustExec(t, rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','actual-author-jana','write',?)`, unit.scope.ArtifactID, rig.userID)
	expiry := time.Now().Add(time.Hour)
	mustExec(t, rig.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('actual-author-jana-session','actual-author-jana',?)`, expiry.UTC().Format(time.RFC3339Nano))
	janaSource, err := captureOriginalHumanCredential(t.Context(), rig.db, &AuthUser{ID: "actual-author-jana", SessionID: "actual-author-jana-session", CredentialExpiresAt: expiry}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	jana := context.WithValue(t.Context(), ctxUser, &AuthUser{ID: janaSource.UserID, SessionID: janaSource.ID, OriginalCredential: janaSource})
	jana = auth.WithOriginalCredential(context.WithValue(jana, ctxAuthKind, AuthKindSession), *janaSource)
	jana = collaborationIngressTestContext(t, unit.fence, jana)
	ticket, admission, err := registry.issue(human, owner, true)
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
	_, janaAdmission, err := registry.issue(jana, owner, true)
	if err != nil {
		t.Fatal("independent actual-session Jana admission", err)
	}
	janaRequest := httptest.NewRequest("POST", "/host-only-author-read", nil).WithContext(jana)
	readJana := func() error {
		return registry.runHumanOperation(janaRequest, janaAdmission.participant, func(bound context.Context) error {
			_, actual, e := cell.store.ReadCheckpoint(bound, unit.scope)
			if e == nil && !bytes.Equal(actual, checkpoint) {
				t.Fatal("Jana checkpoint changed")
			}
			return e
		})
	}
	if err = readJana(); err != nil {
		t.Fatal("original independent reader", err)
	}
	mustExec(t, rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id='actual-author-jana'`, unit.scope.ArtifactID)
	if err = readJana(); err == nil {
		t.Fatal("revoked Jana read from actual store")
	}
	if err = read(); err != nil {
		t.Fatal("unaffected original author denied by broad delivery epoch", err)
	}
	tx, err := rig.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = owners.Check(ctx, tx, owner); err != nil {
		t.Fatal(err)
	}
	if err = registry.sessionStore.check(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if changed, e := collaborationHumanCapturedFenceChanged(ctx, tx, unit.scope, janaAdmission); e != nil || !changed {
		t.Fatal("actual-session Jana revocation not known", e)
	}
	if changed, e := collaborationHumanCapturedFenceChanged(ctx, tx, unit.scope, admission); e != nil || changed {
		t.Fatal("independent author incorrectly revoked", e)
	}
	currentPermission, err := registry.checkCaptured(ctx, tx, admission, true, 0)
	if err != nil || currentPermission == admission.permission {
		t.Fatal("delivery epoch not separated from original author", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	registry.mu.Lock()
	unchanged := registry.admissions[admission.participant] == admission && registry.admissions[janaAdmission.participant] == janaAdmission
	registry.mu.Unlock()
	if !unchanged {
		t.Fatal("actual-session check refreshed original author/source/member/horizon")
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
