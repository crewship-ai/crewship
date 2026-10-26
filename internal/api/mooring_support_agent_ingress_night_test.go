package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
)

// These are local handler/SQL boundary probes. They do not claim a dispatched
// agent, CLI subprocess, browser, or successful native Yrs submission.
func TestSupportNightAgentIngressConfirmedMandateIsNotHumanSession(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	handle := x.attach(t, x.prepare(t))
	request := x.request()
	x.check(t, request, handle, true)
	if auth.OriginalCredentialFromContext(request.Context()) != nil {
		t.Fatal("agent fixture must not impersonate human original")
	}
	manager := &collaborationHTTPManager{pages: x.f.pages, fence: x.fence, clock: x.clock}
	factory := &commandProductFactory{enabled: true, manager: manager}
	for _, endpoint := range []string{"command-session", "session"} {
		t.Run(endpoint, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://studio.example/api/v1/pages/@agent-shared-page/mooring/"+endpoint, strings.NewReader(`{"write":true}`)).WithContext(request.Context())
			r.Header = request.Header.Clone()
			r.SetPathValue("slug", "@agent-shared-page")
			r.Header.Set("Origin", "https://studio.example")
			w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: x.fence}
			if endpoint == "command-session" {
				factory.serve(w, r)
				// Model net/http end-of-handler flushing for a buffered denial.
				if err := w.FlushError(); err != nil {
					t.Fatal(err)
				}
			} else {
				manager.session(w, r)
			}
			expected := "command access denied"
			if endpoint == "session" {
				expected = "Mooring access denied"
			}
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), expected) {
				t.Fatalf("expected human-only boundary denial, got %d %s", w.Code, w.Body.String())
			}
			t.Logf("confirmed mandate Check=allowed; %s HTTP%d: %s", endpoint, w.Code, w.Body.String())
		})
	}
}

func TestSupportNightAgentIngressOriginHasSeparateDenial(t *testing.T) {
	_, manager, request := commandProductTestManager(t)
	manager.config.PublicOrigin = "https://studio.example"
	factory := &commandProductFactory{enabled: true, manager: manager}
	r := httptest.NewRequest("POST", "https://studio.example/api/v1/pages/@admission-page/mooring/command-session", strings.NewReader(`{"action":"open"}`)).WithContext(request.Context())
	r.SetPathValue("slug", "@admission-page")
	r.Header.Set("Origin", "https://foreign.example")
	w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: manager.fence}
	factory.serve(w, r)
	if err := w.FlushError(); err != nil {
		t.Fatal(err)
	}
	if w.Code != 403 || !strings.Contains(w.Body.String(), "command origin denied") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	t.Logf("human scope passed; HTTP%d: %s", w.Code, w.Body.String())
}

func TestSupportNightAgentIngressMandateDoesNotRegisterLiveMCPTask(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	handle := x.attach(t, x.prepare(t))
	request := x.request()
	x.check(t, request, handle, true)
	manager := &collaborationHTTPManager{pages: x.f.pages, fence: x.fence, clock: x.clock, agentMandates: x.store}
	controller := &collaborationAgentTaskController{manager: manager, confirmed: make(map[string]*collaborationLiveAgent)}
	manager.agentTasks = controller
	if _, err := controller.lookup(request); err == nil {
		t.Fatal("standalone mandate must not synthesize a live task registration")
	}
	w := httptest.NewRecorder()
	manager.agentTool(w, request)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "document task denied") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	t.Logf("mandate Check=allowed, no confirmed live-task registration; HTTP%d: %s", w.Code, w.Body.String())
	// Negative controls isolate mandate, bearer identity and grant failures.
	for _, mode := range []string{"missing-mandate", "wrong-crew-audience", "wrong-bearer", "missing-ingress", "revoked-grant"} {
		t.Run(mode, func(t *testing.T) {
			candidate := request.Clone(request.Context())
			candidate.Header = request.Header.Clone()
			h := handle
			switch mode {
			case "missing-mandate":
				h = ""
			case "wrong-crew-audience":
				candidate = candidate.WithContext(context.WithValue(candidate.Context(), ctxInternalTokenCrew, "foreign-crew"))
			case "wrong-bearer":
				candidate.Header.Set("Authorization", "Bearer synthetic-wrong-run")
			case "missing-ingress":
				ctx := context.WithValue(t.Context(), ctxInternalTokenWS, x.f.a.WorkspaceID)
				ctx = context.WithValue(ctx, ctxInternalTokenCrew, x.f.a.CrewID)
				candidate = candidate.WithContext(ctx)
			case "revoked-grant":
				mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page'`)
			}
			x.tx(t, func(tx *sql.Tx) {
				if _, err := x.store.Check(candidate, tx, h, x.key, "task"); err == nil {
					t.Fatal("negative control unexpectedly allowed")
				}
			})
			t.Log("mandate Check=denied")
		})
	}
}
