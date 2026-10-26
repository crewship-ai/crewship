package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Contract regression for the observed idle browser closure. Authentication is
// represented by the existing post-middleware fixture; this does not claim a
// signed JWT, actual WebSocket/Rust process, native document or live browser.
// It specifies externally observable continuity without choosing a handoff
// implementation. The OLD credential/admission must remain bounded.
func TestCollaborationIdleRefreshSameSessionCredentialContract(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "reader", true: "writer"}[write], func(t *testing.T) {
			f := newContinuityRig(t, write)
			withExpiry := func(expiry time.Time) context.Context {
				u := *UserFromContext(f.ctx)
				u.CredentialExpiresAt = expiry
				source, err := captureOriginalHumanCredential(f.ctx, f.r.db, &u, AuthKindSession, "")
				if err != nil {
					t.Fatal(err)
				}
				u.OriginalCredential = source
				return auth.WithOriginalCredential(context.WithValue(f.ctx, ctxUser, &u), *source)
			}
			f.ctx = withExpiry(time.Now().Add(2 * time.Minute))
			ticket, original, err := f.s.issue(f.ctx, f.owner, write)
			if err != nil {
				t.Fatal(err)
			}
			f.admission = original
			joined, err := f.s.join(f.ctx, f.s.runtime, f.owner, ticket, "idle-original-join")
			if err != nil {
				t.Fatal(err)
			}
			m := &collaborationHTTPManager{pages: f.s.pages, fence: f.s.fence, clock: f.clock, config: MooringHTTPConfig{PublicOrigin: "https://studio.example"}, docs: make(map[mooring.SessionKey]*collaborationHTTPDocument)}
			d := &collaborationHTTPDocument{manager: m, key: f.s.scope, ctx: t.Context(), owner: f.owner, humans: f.s, connections: make(map[string]*collaborationHTTPConnection)}
			c := &collaborationHTTPConnection{document: d, admission: original, joined: joined, ref: "idle-original-connection", used: true, joinedDelivered: true, auditedOwner: f.r.userID, participantBinding: &mooring.SessionParticipantBinding{}, origin: "17"}
			c.request = httptest.NewRequest("GET", "https://studio.example/ws", nil).WithContext(f.ctx)
			m.docs[f.s.scope], d.connections[c.ref] = d, c
			oldCap, oldSource := original.freshness.credentialCap, original.source
			var revisionBefore, checkpointBefore string
			if err := f.r.db.QueryRow(`SELECT revision,checkpoint_base64 FROM mooring_collab_documents WHERE page_id='admission-page' AND workspace_id=? AND application_id='wordcraft'`, f.r.wsID).Scan(&revisionBefore, &checkpointBefore); err != nil {
				t.Fatal(err)
			}
			// A later independently authenticated request has the SAME SQL session,
			// binding and authority revision, but a fresh access-token horizon.
			for cycle := 0; cycle < 3; cycle++ {
				fresh := withExpiry(time.Now().Add(time.Duration(10+cycle) * time.Minute))
				req := httptest.NewRequest("POST", "https://studio.example/reauthorize", strings.NewReader(`{"connection_ref":"idle-original-connection"}`)).WithContext(context.WithValue(fresh, ctxWorkspaceID, f.r.wsID))
				req.SetPathValue("slug", "@admission-page")
				req.Header.Set("Origin", "https://studio.example")
				w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: m.fence}
				m.fence.Wrap(http.HandlerFunc(m.reauthorize)).ServeHTTP(w, req)
				if w.Code != http.StatusNoContent {
					t.Fatalf("cycle %d: fresh same-session credential cannot continue original idle connection: status=%d (want 204)", cycle, w.Code)
				}
				if original.source != oldSource || original.freshness.credentialCap != oldCap || original.currentDeadline() > oldCap {
					t.Fatal("continuation extended the OLD credential/admission cap")
				}
				if c.closed || d.closed || c.document != d || c.ref != "idle-original-connection" || c.origin != "17" || c.admission.participant != original.participant || c.admission.session != original.session {
					t.Fatal("credential refresh replaced connection/document/participant identity")
				}
				var revision, checkpoint string
				if err := f.r.db.QueryRow(`SELECT revision,checkpoint_base64 FROM mooring_collab_documents WHERE page_id='admission-page' AND workspace_id=? AND application_id='wordcraft'`, f.r.wsID).Scan(&revision, &checkpoint); err != nil {
					t.Fatal(err)
				}
				if revision != revisionBefore || checkpoint != checkpointBefore {
					t.Fatal("credential refresh changed confirmed document bytes/revision")
				}
			}
			// No replay or native application is invoked by this test. The new
			// future-operation binding must work after the old monotonic cap;
			// the captured OLD proof itself must still fail.
			f.advance(t, 2*time.Minute+time.Second)
			if err := f.check(t, write); err == nil {
				t.Fatal("old captured proof was revived after its cap")
			}
			authorizeFuture := func(intent string) error {
				return f.s.runHumanOperation(c.request, original.participant, func(ctx context.Context) error {
					tx, err := f.r.db.BeginTx(ctx, nil)
					if err != nil {
						return err
					}
					defer tx.Rollback()
					return f.s.authorizeBatchAtBinding(ctx, tx, mooring.BatchAccess{Intent: intent, Scope: f.s.scope, Participant: original.participant}, f.s.runtime, f.s.bindingID)
				})
			}
			intent := "read_checkpoint"
			if write {
				intent = "write"
			}
			if err := authorizeFuture(intent); err != nil {
				t.Fatal("same idle connection lost future authority after qualified refresh", err)
			}
			if !write && authorizeFuture("write") == nil {
				t.Fatal("refreshed reader acquired future write authority")
			}
		})
	}
}
