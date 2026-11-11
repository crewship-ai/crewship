package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/ws"
	"github.com/gorilla/websocket"
)

// What a trusted membership change does to work that is already under way:
// what was issued under the old rights (a session, a mandate, a receipt) stays
// dead for good, and what is only a login credential holds exactly what a new
// login would hold now. membership_authority_boundary_test.go proves this at
// the fence; these rows prove it on the open stream and the running upload.
type heldTarget struct {
	db       *sql.DB
	ws, user string
}

func (h heldTarget) role(t *testing.T, role string) {
	t.Helper()
	mustExec(t, h.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, role, h.ws, h.user)
}

func (h heldTarget) memberID(t *testing.T) (id string, revision int64) {
	t.Helper()
	if err := h.db.QueryRow(`SELECT id,access_revision FROM workspace_members WHERE workspace_id=? AND user_id=?`, h.ws, h.user).Scan(&id, &revision); err != nil {
		t.Fatal(err)
	}
	return id, revision
}

func (h heldTarget) rowElsewhere(t *testing.T, role, mode string) {
	t.Helper()
	mustExec(t, h.db, `INSERT OR IGNORE INTO workspaces(id,name,slug) VALUES('held-other','Other','held-other')`)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('held-other-member','held-other',?,?,?)`, h.user, role, mode)
}

type heldChange struct {
	name string
	// Seeded before admission, so nothing here is itself the change.
	seed   func(*testing.T, heldTarget)
	mutate func(*testing.T, heldTarget)
	// Nothing about this workspace changed: an admission here must stay.
	unrelated bool
	// Capabilities reach a stored admission through access_revision only; a
	// fence built from generations alone does not see them, here or on main.
	capabilityOnly bool
	// The human still holds the right afterwards, so a request may finish.
	rightsBack bool
}

func heldChanges() []heldChange {
	seedTrusted := func(t *testing.T, h heldTarget) { h.rowElsewhere(t, "MEMBER", "trusted") }
	return []heldChange{
		{name: "role down", mutate: func(t *testing.T, h heldTarget) { h.role(t, "VIEWER") }},
		{name: "role down and back up", rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			h.role(t, "VIEWER")
			h.role(t, "MANAGER")
		}},
		{name: "capability added and removed", capabilityOnly: true, rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `UPDATE workspace_members SET capabilities='["chat"]' WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
			mustExec(t, h.db, `UPDATE workspace_members SET capabilities=NULL WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
		}},
		{name: "removal", mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
		}},
		{name: "removal and rejoin under a new id", rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
			mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('held-new-member',?,?,'MANAGER')`, h.ws, h.user)
		}},
		{name: "removal and restore under the same id", rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			id, revision := h.memberID(t)
			mustExec(t, h.db, `DELETE FROM workspace_members WHERE id=?`, id)
			mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_revision) VALUES(?,?,?,'MANAGER',?)`, id, h.ws, h.user, revision)
		}},
		{name: "restricted here", mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
		}},
		{name: "restricted here, then lifted", rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
			mustExec(t, h.db, `UPDATE workspace_members SET access_mode='trusted' WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
		}},
		{name: "restricted row elsewhere", mutate: func(t *testing.T, h heldTarget) { h.rowElsewhere(t, "MEMBER", "restricted") }},
		{name: "restricted row elsewhere, then removed", rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			h.rowElsewhere(t, "MEMBER", "restricted")
			mustExec(t, h.db, `DELETE FROM workspace_members WHERE id='held-other-member'`)
		}},
		{name: "control: trusted role change elsewhere", seed: seedTrusted, unrelated: true, rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `UPDATE workspace_members SET role='ADMIN' WHERE id='held-other-member'`)
		}},
		{name: "control: leaving a trusted workspace elsewhere", seed: seedTrusted, unrelated: true, rightsBack: true, mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `DELETE FROM workspace_members WHERE id='held-other-member'`)
		}},
	}
}

// The real SSE handler. After the change the next delivery is either the lease
// or session_retired, and a retired session token stays refused: PRD §4.6,
// reconnect does not restore a removed mandate.
func TestOpenNativeStreamAcrossMembershipChange(t *testing.T) {
	const user = "native-scope-writer"
	for _, test := range heldChanges() {
		t.Run(test.name, func(t *testing.T) {
			f, m, _ := craftNativeHTTPFixture(t)
			craftNativeGrantedWriter(t, f)
			h := heldTarget{f.db, f.wsID, user}
			h.role(t, "MANAGER")
			if test.seed != nil {
				test.seed(t, h)
			}
			ctx := craftNativeFreshRequestContext(t, f, user, "native-scope-session")
			token, id := craftNativeIssued(t, m, ctx)
			next := craftNativeOpenStreamOnTick(t, f, m, ctx, token)
			if event, _ := next(); event != "lease" {
				t.Fatalf("calibration event %q", event)
			}
			test.mutate(t, h)
			event, data := next()
			if test.unrelated {
				if event != "lease" {
					t.Fatalf("membership elsewhere retired the open stream: %s %v", event, data)
				}
				return
			}
			if event != "session_retired" {
				t.Fatalf("the open stream outlived the change: %s %v", event, data)
			}
			fresh := craftNativeFreshRequestContext(t, f, user, "native-scope-session")
			if err := craftNativeStreamBound(m, fresh, token, id); err == nil {
				t.Fatal("the retired session token came back")
			}
		})
	}
}

// A Design socket is fenced by generations and evaluated live. It holds no
// pinned member, so a capability change does not reach it: capabilities gate no
// Design right after admission (pages_design.go, canSeePage and mayEditSpec).
func TestOpenDesignSocketAcrossMembershipChange(t *testing.T) {
	const user = "held-design-editor"
	for _, test := range heldChanges() {
		t.Run(test.name, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			f.addUser(t, user)
			if w := f.grant(t, user, "write"); w.Code != 200 {
				t.Fatalf("grant %d %s", w.Code, w.Body)
			}
			h := heldTarget{f.h.db, f.workspace, user}
			h.role(t, "MANAGER")
			if test.seed != nil {
				test.seed(t, h)
			}
			socket := f.dial(t, user)
			test.mutate(t, h)
			if test.unrelated || test.capabilityOnly {
				designSocketLive(t, socket)
				return
			}
			if err := socket.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err == nil {
				designAuthorityClosed(t, socket)
			}
		})
	}
}

// The chat socket keeps its credential across any membership change, on main as
// here: a stream is fenced by session and token, never by this revision. Delivery
// is decided live by the channel authorizer. What the held credential can still
// mint is what a new login mints now, under today's member row, and never the
// receipt taken before the change.
func TestOpenSocketCredentialHoldsNoMoreThanANewLogin(t *testing.T) {
	for _, test := range heldChanges() {
		t.Run(test.name, func(t *testing.T) {
			var seed func(*pagesTransferRig)
			if test.seed != nil {
				seed = func(f *pagesTransferRig) { test.seed(t, heldTarget{f.db, f.wsID, f.userID}) }
			}
			b, _ := newBoundaryConsumers(t, seed)
			h := heldTarget{b.f.db, b.f.wsID, b.f.userID}
			socket, release, err := b.mw.authenticateStream(t.Context(), b.claims)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			channels := ws.NewDBChannelAuthorizer(b.f.db)
			channel := "session:" + b.receipt.ChatID
			if ok, err := channels.CanSubscribe(t.Context(), h.user, channel); err != nil || !ok {
				t.Fatal("calibration subscribe", ok, err)
			}
			test.mutate(t, h)
			if !auth.StreamCredentialCurrent(socket) {
				t.Fatal("a membership change closed the login credential of a stream")
			}
			internal := NewInternalHandler(b.f.db, "synthetic-master", newTestLogger())
			mint := func(source *auth.OriginalCredential) string {
				t.Helper()
				receipt, err := internal.originalHumanChatReceipt(t.Context(), b.receipt.ChatID, h.user, nil, source)
				if err != nil {
					t.Fatal(err)
				}
				if receipt == nil {
					return ""
				}
				if !test.unrelated && *receipt == *b.receipt {
					t.Fatal("the receipt taken before the change came back")
				}
				return receipt.MemberID
			}
			now := craftNativeFreshRequestContext(t, b.f, h.user, "native-source")
			held, fresh := mint(auth.OriginalCredentialFromContext(socket)), mint(auth.OriginalCredentialFromContext(now))
			if held != "" && held != fresh {
				t.Fatalf("the held credential minted member %q, a new login %q", held, fresh)
			}
			allowed, err := channels.CanSubscribe(t.Context(), h.user, channel)
			if err != nil {
				t.Fatal(err)
			}
			if allowed != (fresh != "") {
				t.Fatalf("channel allowed=%v, a new login's receipt %q", allowed, fresh)
			}
		})
	}
}

// heldUpload runs the real handler with its body still arriving when the rights
// change: reserved, partly read, then finished after the change.
func heldUpload(t *testing.T, db *sql.DB, handler http.HandlerFunc, ctx context.Context, slug, body string, change func()) *httptest.ResponseRecorder {
	t.Helper()
	reader, writer := io.Pipe()
	r := httptest.NewRequest(http.MethodPost, "/", reader).WithContext(ctx)
	r.ContentLength = int64(len(body))
	if slug != "" {
		r.SetPathValue("slug", slug)
	}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler(w, r) }()
	reserved := false
	for deadline := time.Now().Add(10 * time.Second); !reserved && time.Now().Before(deadline); {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM artifact_storage_reservations`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if reserved = count > 0; !reserved {
			select {
			case <-done:
				t.Fatalf("calibration: refused before the body: %d %s", w.Code, w.Body)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	if !reserved {
		t.Fatal("no reservation")
	}
	half := len(body) / 2
	if _, err := io.WriteString(writer, body[:half]); err != nil {
		t.Fatal(err)
	}
	change()
	_, _ = io.WriteString(writer, body[half:])
	_ = writer.Close()
	<-done
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_storage_reservations`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("reservation left behind: %d %v", left, err)
	}
	return w
}

// A login credential is checked against live rights when the work commits. The
// reservation is only a byte allowance, so a body that was refused rights at the
// end publishes nothing, and one that has them again publishes as a new request
// would. An agent run holds a mandate from the old member revision, so nothing
// brings its upload back.
func TestUploadInFlightAcrossMembershipChange(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	body := func(slug string) string {
		return `{"slug":"` + slug + `","name":"Held",` + strings.TrimPrefix(artifactProjectBody(0, slug), "{")
	}
	for _, test := range heldChanges() {
		t.Run("human/"+test.name, func(t *testing.T) {
			var seed func(*pagesTransferRig)
			if test.seed != nil {
				seed = func(f *pagesTransferRig) { test.seed(t, heldTarget{f.db, f.wsID, f.userID}) }
			}
			b, ctx := newBoundaryConsumers(t, seed)
			h := heldTarget{b.f.db, b.f.wsID, b.f.userID}
			w := heldUpload(t, b.f.db, b.pa.pages.CreateArtifactProject, ctx, "", body("held-human"), func() { test.mutate(t, h) })
			var pages int
			if err := b.f.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='held-human'`).Scan(&pages); err != nil {
				t.Fatal(err)
			}
			if test.rightsBack {
				if w.Code != 201 || pages != 1 {
					t.Fatalf("rights hold at commit, yet %d %s", w.Code, w.Body)
				}
				return
			}
			if w.Code == 201 || pages != 0 {
				t.Fatalf("published without the right: %d, pages %d, %s", w.Code, pages, w.Body)
			}
		})
		t.Run("agent/"+test.name, func(t *testing.T) {
			f := artifactProjectOriginalRunFixture(t)
			h := heldTarget{f.rig.db, f.a.WorkspaceID, f.a.HumanUserID}
			h.role(t, "MANAGER")
			if test.seed != nil {
				test.seed(t, h)
			}
			f.receipt = artifactRunFreshReceipt(t, f)
			f.admit(t)
			human := withWorkspace(withUser(t.Context(), &AuthUser{ID: f.a.HumanUserID}), f.a.WorkspaceID, "MANAGER")
			grant := pagesRequest(t, http.MethodPost, "/api/v1/pages/artifacts/consent", f.a.WorkspaceID, f.a.HumanUserID, "MANAGER", `{"run_id":"`+f.a.RunID+`"}`)
			grant.Header.Set("Authorization", "Bearer "+mintTokenFor(t, f.rig.db, f.a.HumanUserID, "held-upload-human"))
			grant = grant.WithContext(context.WithValue(grant.Context(), ctxAuthKind, AuthKindCLIToken))
			granted := httptest.NewRecorder()
			f.pages.GrantArtifactProjectRunConsent(granted, grant)
			if granted.Code != 200 {
				t.Fatalf("calibration consent: %d %s", granted.Code, granted.Body)
			}
			ctx := context.WithValue(human, artifactRunKey{}, &f.a)
			w := heldUpload(t, f.rig.db, f.pages.CreateArtifactProject, ctx, "", body("held-agent"), func() { test.mutate(t, h) })
			if test.unrelated {
				if w.Code != 201 {
					t.Fatalf("membership elsewhere refused the run's upload: %d %s", w.Code, w.Body)
				}
				return
			}
			if w.Code == 201 {
				t.Fatalf("a run's mandate outlived the change: %s", w.Body)
			}
		})
	}
}

func artifactRunFreshReceipt(t *testing.T, f *artifactPrincipalFixture) *chataudience.Receipt {
	t.Helper()
	expiry := time.Now().Add(time.Hour).UTC()
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, SessionID: "original-session", CredentialExpiresAt: expiry}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := NewInternalHandler(f.rig.db, "synthetic-artifact-original-master", newTestLogger()).originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, source)
	if err != nil || receipt == nil {
		t.Fatal("fresh receipt", err)
	}
	return receipt
}

// Every stored authority that also pins the member row: Mooring human admission,
// a Mooring agent task, a native agent grant and a Page review token.
func TestStoredAuthorityDoesNotReviveAcrossMembershipChange(t *testing.T) {
	for _, test := range heldChanges() {
		t.Run("mooring human/"+test.name, func(t *testing.T) {
			r, registry, owner, _, _ := collaborationAdmissionFixture(t)
			h := heldTarget{r.db, r.wsID, r.userID}
			h.role(t, "MANAGER")
			if test.seed != nil {
				test.seed(t, h)
			}
			ctx := collaborationIngressTestContext(t, registry.fence, craftNativeFreshRequestContext(t, r, r.userID, "admission-source"))
			_, admission, err := registry.issue(ctx, owner, true)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, h)
			tx, err := r.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = registry.checkCaptured(t.Context(), tx, admission, true, 0)
			if (err == nil) != test.unrelated {
				t.Fatalf("admission after the change: %v, unrelated=%v", err, test.unrelated)
			}
		})
		t.Run("mooring agent/"+test.name, func(t *testing.T) {
			b, _ := func() (*boundaryConsumers, context.Context) {
				var seed func(*pagesTransferRig)
				if test.seed != nil {
					seed = func(f *pagesTransferRig) { test.seed(t, heldTarget{f.db, f.wsID, f.userID}) }
				}
				return newBoundaryConsumers(t, seed)
			}()
			h := heldTarget{b.f.db, b.f.wsID, b.f.userID}
			key := mooring.SessionKey{WorkspaceID: h.ws, ArtifactID: "native-page", ApplicationID: "wordcraft"}
			tx, err := b.f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			vector, err := captureCollaborationAuthorVector(t.Context(), tx, key, h.user, b.pa.a.AgentID, b.pa.a.CrewID)
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			member, revision := h.memberID(t)
			task := collaborationAgentTaskBinding{authorVector: vector, key: key, originalCredential: *b.source, memberID: member, memberRevision: revision}
			task.work.agentID, task.work.crewID = b.pa.a.AgentID, b.pa.a.CrewID
			test.mutate(t, h)
			read, err := b.f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer read.Rollback()
			changed, err := collaborationAgentCapturedFenceChanged(t.Context(), read, task)
			if err != nil {
				t.Fatal(err)
			}
			if changed == test.unrelated {
				t.Fatalf("task fence changed=%v, unrelated=%v", changed, test.unrelated)
			}
		})
		t.Run("native agent/"+test.name, func(t *testing.T) {
			f, m, _ := nativeAgentAdmissionFixture(t)
			h := heldTarget{f.rig.db, f.a.WorkspaceID, f.a.HumanUserID}
			h.role(t, "MANAGER")
			if test.seed != nil {
				test.seed(t, h)
			}
			ctx := craftNativeFreshRequestContext(t, f.rig, f.a.HumanUserID, "original-session")
			accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			e := m.entries[accepted.WorkID]
			m.mu.Unlock()
			test.mutate(t, h)
			tx, err := f.rig.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// The grant also pins the broad workspace epoch, so it never outlives
			// even a change elsewhere; that strictness predates this revision.
			if err = m.checkOriginalGrant(t.Context(), tx, e.grant); !errors.Is(err, errCraftNativeAgentDenied) {
				t.Fatalf("a native agent grant outlived the change: %v", err)
			}
		})
		t.Run("review token/"+test.name, func(t *testing.T) {
			var seed func(*pagesTransferRig)
			if test.seed != nil {
				seed = func(f *pagesTransferRig) { test.seed(t, heldTarget{f.db, f.wsID, f.userID}) }
			}
			b, ctx := newBoundaryConsumers(t, seed)
			h := heldTarget{b.f.db, b.f.wsID, b.f.userID}
			r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			before, err := b.pa.pages.catalogReviewEpoch(r, "native-page")
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, h)
			after, err := b.pa.pages.catalogReviewEpoch(r, "native-page")
			if err == nil && after == before {
				t.Fatal("a review token from before the change is still current")
			}
		})
	}
}

// A Page grant ends when its issuer loses standing and does not come back with
// it (pages_grants_authz.go, #5069). The session a grantee opened on it stays
// dead, and a fresh session cannot be opened on the old grant either.
func TestGranteeSessionAcrossIssuerMembershipChange(t *testing.T) {
	const user = "native-scope-writer"
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, heldTarget)
		keep   bool
	}{
		{name: "issuer down and back up", mutate: func(t *testing.T, h heldTarget) {
			h.role(t, "VIEWER")
			h.role(t, "ADMIN")
		}},
		{name: "issuer removed and rejoined", mutate: func(t *testing.T, h heldTarget) {
			mustExec(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, h.ws, h.user)
			mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('held-new-issuer',?,?,'ADMIN')`, h.ws, h.user)
		}},
		{name: "issuer restricted elsewhere, then removed", mutate: func(t *testing.T, h heldTarget) {
			h.role(t, "MANAGER")
			h.rowElsewhere(t, "MEMBER", "restricted")
			mustExec(t, h.db, `DELETE FROM workspace_members WHERE id='held-other-member'`)
			h.role(t, "ADMIN")
		}},
		{name: "control: issuer's trusted row elsewhere", keep: true, mutate: func(t *testing.T, h heldTarget) {
			h.rowElsewhere(t, "OWNER", "trusted")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _ := craftNativeHTTPFixture(t)
			ctx := craftNativeGrantedWriter(t, f)
			token, id := craftNativeIssued(t, m, ctx)
			if err := craftNativeStreamBound(m, ctx, token, id); err != nil {
				t.Fatal("calibration", err)
			}
			test.mutate(t, heldTarget{f.db, f.wsID, f.adminID})
			next := craftNativeFreshRequestContext(t, f, user, "native-scope-session")
			if err := craftNativeStreamBound(m, next, token, id); (err == nil) != test.keep {
				t.Fatalf("grantee session after the issuer's change: %v, keep=%v", err, test.keep)
			}
			if w := craftNativeSessionCall(m, next); (w.Code == 201) != test.keep {
				t.Fatalf("fresh session on the grant after the issuer's change: %d %s, keep=%v", w.Code, w.Body, test.keep)
			}
		})
	}
}

// A workspace removed outright takes its member rows through a foreign key, and
// the tombstone must be written there too: a backup restored afterwards inserts
// the row under its old id.
func TestRetiredMemberIDSurvivesWorkspaceDeletion(t *testing.T) {
	f := pagesTransferSetup(t)
	mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('held-gone','Gone','held-gone')`)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('held-gone-member','held-gone',?,'MEMBER')`, f.userID)
	var before int64
	if err := f.db.QueryRow(`SELECT authority_revision FROM users WHERE id=?`, f.userID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `DELETE FROM workspaces WHERE id='held-gone'`)
	mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('held-gone','Gone','held-gone')`)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('held-gone-member','held-gone',?,'MEMBER')`, f.userID)
	var after int64
	if err := f.db.QueryRow(`SELECT authority_revision FROM users WHERE id=?`, f.userID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("a row restored after its workspace was deleted kept the global revision at %d", after)
	}
}
