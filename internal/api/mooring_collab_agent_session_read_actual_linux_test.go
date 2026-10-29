//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual Rust/SQL/BOOTTIME; original HTTP contexts and confirmed work/runtime
// rows remain synthetic host fixtures, not signed HTTP/paid runtime proof.
func withActualAgentReadSession(t *testing.T, call func(*collaborationAgentTestFixture, *collaborationSessionStore, *collaborationHumanRegistry, mooring.OwnerLease), inspectOrigins ...bool) {
	t.Helper()
	pinned := func(pathEnv, hashEnv string, limit int64) []byte {
		t.Helper()
		path, hash := os.Getenv(pathEnv), os.Getenv(hashEnv)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			t.Fatalf("bounded pinned %s missing: %v", pathEnv, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("agent startup stage 1", err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != hash {
			t.Fatalf("%s digest mismatch", pathEnv)
		}
		return raw
	}
	pinned("MOORING_SESSION_BINARY", "MOORING_SESSION_BINARY_SHA256", 64<<20)
	checkpoint := pinned("MOORING_SESSION_CHECKPOINT", "MOORING_SESSION_CHECKPOINT_SHA256", 4<<20)
	// Use the production FULL handle before capturing any original proof.
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	full, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { full.Close() })
	f := newArtifactPrincipalFixtureWithRig(t, pagesTransferSetupWithDB(t, full))
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.rig.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('original-session',?,?)`, f.a.HumanUserID, expiry.Format(time.RFC3339Nano))
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, SessionID: "original-session", CredentialExpiresAt: expiry.Add(-time.Minute)}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	x := newCollaborationAgentTestFixtureForPrincipal(t, f, source, "read")
	mustExec(t, x.f.rig.db, `DELETE FROM mooring_collab_documents WHERE page_id=?`, x.key.ArtifactID)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal("agent startup stage 2", err)
	}
	owners := mooring.NewOwnerStore(x.f.rig.db, true, clock, time.Minute)
	versions := mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	runtime, err := mooring.StartSessionRuntime(t.Context(), mooring.SessionRuntimeConfig{Runtime: mooring.RuntimeConfig{Enabled: true, Binary: os.Getenv("MOORING_SESSION_BINARY"), Versions: versions, ControlProfile: mooring.PrivateSessionProfile}, Scope: x.key})
	if err != nil {
		t.Fatal("agent startup stage 3", err)
	}
	defer runtime.Close()
	defer func() {
		testutil.SnapshotActualFailure(t, full.DB, filepath.Join(filepath.Dir(path), "before-close.sqlite"))
	}()
	cell, err := newCollaborationSessionStore(true, x.f.pages, owners, x.fence, runtime, x.key, versions)
	if err != nil {
		t.Fatal("agent startup stage 4", err)
	}
	wireScope := protocol.Scope{WorkspaceID: protocol.Identifier(x.key.WorkspaceID), ArtifactID: protocol.Identifier(x.key.ArtifactID), ApplicationID: "wordcraft"}
	wireVersions := protocol.Versions{ProtocolVersion: versions.Protocol, ModelVersion: versions.Model, EngineVersion: versions.Engine}
	sessionID := "actual-human-owned-session"
	open := protocol.SessionOpen{Type: "session_open", RequestID: "human-open", ControlProfile: mooring.PrivateSessionProfile, Scope: wireScope, SessionID: protocol.Identifier(sessionID), RuntimeRef: protocol.Identifier(runtime.RuntimeRef()), Versions: wireVersions, RequiredCapabilities: []protocol.SessionOpenRequiredCapabilitiesItem{protocol.SessionOpenRequiredCapabilitiesItemCheckpointIdentityV1}}
	if len(inspectOrigins) == 1 && inspectOrigins[0] {
		open.RequiredCapabilities = append(open.RequiredCapabilities, protocol.SessionOpenRequiredCapabilitiesItemOriginInventoryV1)
	}
	opened := make(chan error, 1)
	openStarted := time.Now()
	go func() { _, e := runtime.Open(t.Context(), open); opened <- e }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	control, err := runtime.NextControl(ctx)
	if err != nil {
		t.Fatal("agent startup stage 5", err)
	}
	raw, err := control.Frame()
	if err != nil {
		t.Fatal("agent startup stage 6", err)
	}
	var acquire protocol.OwnerAcquire
	if json.Unmarshal(raw, &acquire) != nil || acquire.Type != "owner_acquire" || acquire.Scope != wireScope || acquire.RuntimeRef != open.RuntimeRef {
		t.Fatal("uncorrelated owner acquisition")
	}
	owner, err := owners.Acquire(ctx, x.key, runtime.RuntimeRef())
	if err != nil {
		t.Fatal("agent startup stage 7", err)
	}
	request := httptest.NewRequest("POST", "/host-only-initialize", nil).WithContext(x.human)
	if err = cell.initialize(request, owner, checkpoint); err != nil {
		t.Fatal("original human initialization", err)
	}
	if _, _, err = cell.store.ReadCheckpoint(x.human, x.key); err == nil {
		t.Fatal("unbound startup store granted a reader")
	}
	var permission string
	if err = x.f.rig.db.QueryRow(`SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=?`, x.key.WorkspaceID).Scan(&permission); err != nil {
		t.Fatal("agent startup stage 8", err)
	}
	grant := protocol.OwnerGrant{Type: "owner_grant", RequestID: acquire.RequestID, Scope: wireScope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(owner.Generation, 10)), Token: owner.Token}, LeaseDurationMs: "60000", Revision: "0", RenewalNonce: acquire.RenewalNonce, PermissionVersion: protocol.Counter(permission)}
	raw, _ = json.Marshal(grant)
	if err = runtime.ReplyControl(ctx, control, raw); err != nil {
		t.Fatal("agent startup stage 9", err)
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
		t.Fatal("agent startup stage 10", err)
	}
	activation, err := runtime.Activate(ctx, cell.store, owner, restore)
	if err != nil {
		t.Fatal("agent startup stage 11", err)
	}
	registry, err := newCollaborationSessionHumanRegistry(ctx, cell, activation, owner, clock, 8)
	if err != nil {
		t.Fatal("agent startup stage 12", err)
	}
	call(x, cell, registry, owner)
}

func TestCollaborationAgentActualSessionReadKeepsOriginalMandate(t *testing.T) {
	withActualAgentReadSession(t, func(x *collaborationAgentTestFixture, cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease) {
		var pending *collaborationAgentPending
		x.tx(t, func(tx *sql.Tx) {
			var accepted work.Receipt
			var err error
			pending, accepted, err = x.store.StartTask(x.human, tx, x.key, "read", x.f.a.MessageID, x.f.receipt)
			if err != nil {
				t.Fatal("original read task", err)
			}
			x.workID = accepted.WorkID
		})
		handle := x.attach(t, pending)
		request := x.request()
		if snapshot, err := cell.store.ReadFullCheckpoint(request.Context(), owner); err == nil || snapshot.Checkpoint.State != "" {
			t.Fatal("bare agent context acquired session reader")
		}
		initial, err := cell.agentCheckpoint(request, x.store, handle, owner)
		if err != nil || initial.Checkpoint.State == "" {
			t.Fatal("actual agent checkpoint", err)
		}
		positive := func() {
			t.Helper()
			got, err := cell.agentCheckpoint(request, x.store, handle, owner)
			if err != nil || !bytes.Equal([]byte(got.Checkpoint.State), []byte(initial.Checkpoint.State)) || got.Checkpoint.Digest != initial.Checkpoint.Digest {
				t.Fatal("original agent reader lost identity", err)
			}
		}
		var escaped context.Context
		err = cell.runAgentRead(request, x.store, handle, owner, func(ctx context.Context) error {
			escaped = ctx
			copied := *cell.store
			if s, e := copied.ReadFullCheckpoint(ctx, owner); e == nil || s.Checkpoint.State != "" {
				t.Fatal("copied store borrowed original agent authority")
			}
			wrong := owner
			wrong.Token = strings.Repeat("f", 64)
			if s, e := cell.store.ReadFullCheckpoint(ctx, wrong); e == nil || s.Checkpoint.State != "" {
				t.Fatal("agent read changed owner token")
			}
			mixed := context.WithValue(ctx, ctxUser, &AuthUser{ID: x.f.a.HumanUserID})
			if s, e := cell.store.ReadFullCheckpoint(mixed, owner); e == nil || s.Checkpoint.State != "" {
				t.Fatal("agent read borrowed a human context")
			}
			change := submissionTestChange(t, x.key, "read-purpose-write", "", "synthetic-agent-participant")
			if _, e := cell.store.Commit(ctx, mooring.BatchRequest{Lease: owner, ExpectedRevision: "0", Changes: []mooring.BatchChange{change}}); e == nil {
				t.Fatal("agent read scope became write authority")
			}
			_, e := cell.store.ReadFullCheckpoint(ctx, owner)
			return e
		})
		if err != nil {
			t.Fatal("complete original agent read", err)
		}
		if s, e := cell.store.ReadFullCheckpoint(escaped, owner); e == nil || s.Checkpoint.State != "" {
			t.Fatal("escaped agent context retained authority")
		}
		negatives := []struct {
			name  string
			alter func(*http.Request) *http.Request
		}{
			{"no ingress", func(r *http.Request) *http.Request { return r.WithContext(context.Background()) }},
			{"wrong crew", func(r *http.Request) *http.Request {
				return r.WithContext(context.WithValue(r.Context(), ctxInternalTokenCrew, "other"))
			}},
			{"wrong workspace", func(r *http.Request) *http.Request {
				return r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, "other"))
			}},
			{"human label", func(r *http.Request) *http.Request {
				return r.WithContext(context.WithValue(r.Context(), ctxUser, &AuthUser{ID: x.f.a.HumanUserID}))
			}},
			{"wrong bearer", func(r *http.Request) *http.Request { r.Header.Set("Authorization", "Bearer invalid"); return r }},
		}
		for _, negative := range negatives {
			r := negative.alter(request.Clone(request.Context()))
			called := false
			e := cell.runAgentRead(r, x.store, handle, owner, func(context.Context) error { called = true; return nil })
			if e == nil || called {
				t.Fatal("unqualified agent invoked work", negative.name)
			}
		}
		if s, e := cell.agentCheckpoint(request, x.store, strings.Repeat("f", 128), owner); e == nil || s.Checkpoint.State != "" {
			t.Fatal("foreign mandate returned content")
		}
		foreignDB, e := sql.Open("sqlite", ":memory:")
		if e != nil {
			t.Fatal(e)
		}
		defer foreignDB.Close()
		foreignPages := NewPageHandler(foreignDB, nil, newTestLogger())
		foreign := &collaborationAgentMandateStore{pages: foreignPages, fence: x.fence}
		if s, e := cell.agentCheckpoint(request, foreign, handle, owner); e == nil || s.Checkpoint.State != "" {
			t.Fatal("foreign mandate database returned content")
		}
		positive()
		// The original human is independent of this agent's explicit read grant.
		_, humanAdmission, e := humans.issue(x.human, owner, false)
		if e != nil {
			t.Fatal(e)
		}
		humanRequest := httptest.NewRequest("POST", "/host-only-reader", nil).WithContext(x.human)
		mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id=?`, x.key.ArtifactID, x.f.a.AgentID)
		mustExec(t, x.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'read',?)`, x.key.ArtifactID, x.f.a.AgentID, x.f.a.HumanUserID)
		called := false
		if e = cell.runAgentRead(request, x.store, handle, owner, func(context.Context) error { called = true; return nil }); e == nil || called {
			t.Fatal("revoked/restored grant refreshed original agent proof")
		}
		if e = humans.runHumanOperation(humanRequest, humanAdmission.participant, func(ctx context.Context) error { _, e := cell.store.ReadFullCheckpoint(ctx, owner); return e }); e != nil {
			t.Fatal("agent grant ABA denied independent original human", e)
		}
	})
}
