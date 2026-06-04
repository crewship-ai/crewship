package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/policy"
)

// covAPRawReq builds a request with a raw (non-JSON-encoded) body and
// the workspace/user context populated — mirrors personaTestRig.authedReq
// but lets a test send malformed JSON so the readJSON error branch fires.
// Prefixed covAP per the harness naming rule.
func covAPRawReq(t *testing.T, r *personaTestRig, method, target, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.SetPathValue("agentId", r.agentID)
	req.SetPathValue("crewId", r.crewID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, r.wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u1"})
	return req.WithContext(ctx)
}

// covAPNoStorageRig clones the seeded DB-backed handler but with an empty
// outputBasePath so requireStorage() short-circuits to 503. Reuses the
// rig's db/logger; only the storage root is blanked.
func covAPNoStorageRig(t *testing.T, r *personaTestRig) *PersonaHandler {
	t.Helper()
	silent := slog.New(slog.NewTextHandler(discardWriterCovAP{}, nil))
	return NewPersonaHandler(r.h.db, silent, "", r.h.policyResolver)
}

// discardWriterCovAP is a no-op io.Writer for the silent logger above.
type discardWriterCovAP struct{}

func (discardWriterCovAP) Write(p []byte) (int, error) { return len(p), nil }

// --- 503: storage not configured ------------------------------------------

func TestCovAPStorageUnconfigured503(t *testing.T) {
	r := newPersonaTestRig(t)
	h := covAPNoStorageRig(t, r)

	cases := []struct {
		name string
		call func(w http.ResponseWriter, req *http.Request)
		meth string
	}{
		{"GetAgent", h.GetAgentPersona, http.MethodGet},
		{"PutAgent", h.PutAgentPersona, http.MethodPut},
		{"DeleteAgent", h.DeleteAgentPersona, http.MethodDelete},
		{"AgentHistory", h.GetAgentPersonaHistory, http.MethodGet},
		{"SuggestAgent", h.SuggestAgentPersona, http.MethodPost},
		{"GetCrew", h.GetCrewPersona, http.MethodGet},
		{"PutCrew", h.PutCrewPersona, http.MethodPut},
		{"DeleteCrew", h.DeleteCrewPersona, http.MethodDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, r.authedReq(t, tc.meth, "/", nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: expected 503; got %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- 404: agent / crew not found -------------------------------------------

func TestCovAPAgentNotFound404(t *testing.T) {
	r := newPersonaTestRig(t)
	mk := func(method string) *http.Request {
		req := r.authedReq(t, method, "/", map[string]string{"content": "x"})
		req.SetPathValue("agentId", "does-not-exist")
		return req
	}
	cases := []struct {
		name string
		call func(w http.ResponseWriter, req *http.Request)
		meth string
	}{
		{"GetAgent", r.h.GetAgentPersona, http.MethodGet},
		{"PutAgent", r.h.PutAgentPersona, http.MethodPut},
		{"DeleteAgent", r.h.DeleteAgentPersona, http.MethodDelete},
		{"AgentHistory", r.h.GetAgentPersonaHistory, http.MethodGet},
		{"SuggestAgent", r.h.SuggestAgentPersona, http.MethodPost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, mk(tc.meth))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: expected 404; got %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCovAPCrewNotFound404(t *testing.T) {
	r := newPersonaTestRig(t)
	mk := func(method string) *http.Request {
		req := r.authedReq(t, method, "/", map[string]string{"content": "x"})
		req.SetPathValue("crewId", "no-such-crew")
		return req
	}
	cases := []struct {
		name string
		call func(w http.ResponseWriter, req *http.Request)
		meth string
	}{
		{"GetCrew", r.h.GetCrewPersona, http.MethodGet},
		{"PutCrew", r.h.PutCrewPersona, http.MethodPut},
		{"DeleteCrew", r.h.DeleteCrewPersona, http.MethodDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, mk(tc.meth))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: expected 404; got %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- 400: malformed JSON bodies --------------------------------------------

func TestCovAPBadJSON400(t *testing.T) {
	r := newPersonaTestRig(t)
	cases := []struct {
		name string
		call func(w http.ResponseWriter, req *http.Request)
		meth string
	}{
		{"PutAgent", r.h.PutAgentPersona, http.MethodPut},
		{"SuggestAgent", r.h.SuggestAgentPersona, http.MethodPost},
		{"PutCrew", r.h.PutCrewPersona, http.MethodPut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, covAPRawReq(t, r, tc.meth, "/", "{not-valid-json"))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400; got %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- 400: suggest with empty content ---------------------------------------

func TestCovAPSuggestEmptyContent400(t *testing.T) {
	r := newPersonaTestRig(t)
	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{
		"content": "   ",
	}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for blank content; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// --- 413: suggest with oversize content ------------------------------------

func TestCovAPSuggestOversize413(t *testing.T) {
	r := newPersonaTestRig(t)
	big := strings.Repeat("x", memory.PersonaCapBytes+1)
	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{
		"content": big,
	}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// --- 413: crew PUT with oversize content -----------------------------------

func TestCovAPPutCrewOversize413(t *testing.T) {
	r := newPersonaTestRig(t)
	big := strings.Repeat("y", memory.PersonaCapBytes+1)
	rec := httptest.NewRecorder()
	r.h.PutCrewPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{
		"content": big,
	}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// --- crew round trip incl. DELETE ------------------------------------------

func TestCovAPCrewDeleteRoundTrip(t *testing.T) {
	r := newPersonaTestRig(t)
	// Write a crew persona then reset it.
	rec := httptest.NewRecorder()
	r.h.PutCrewPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{
		"content": "Crew tone: blunt.",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT crew: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	r.h.DeleteCrewPersona(rec, r.authedReq(t, http.MethodDelete, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE crew: %d %s", rec.Code, rec.Body.String())
	}
	// GET after delete: empty content, layer=crew.
	rec = httptest.NewRecorder()
	r.h.GetCrewPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET crew: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["content"] != "" {
		t.Errorf("expected empty crew content after delete; got %+v", got)
	}
}

// --- solo agent (crew_id IS NULL) path → soloAgentMemoryDir ----------------

func TestCovAPSoloAgentPersona(t *testing.T) {
	r := newPersonaTestRig(t)
	// Seed a crew-less agent so resolveAgentPaths takes the solo branch.
	if _, err := r.h.db.Exec(`
		INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role, role_title)
		VALUES ('solo1','ws1',NULL,'solo','Solo','AGENT','Operator')`); err != nil {
		t.Fatalf("seed solo agent: %v", err)
	}
	mk := func(method string, body any) *http.Request {
		req := r.authedReq(t, method, "/", body)
		req.SetPathValue("agentId", "solo1")
		return req
	}

	// PUT writes into the solo subtree.
	rec := httptest.NewRecorder()
	r.h.PutAgentPersona(rec, mk(http.MethodPut, map[string]string{"content": "Solo tone."}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT solo: %d %s", rec.Code, rec.Body.String())
	}

	// File landed under solo/{workspaceID}/agents/solo/.memory.
	soloPaths := memory.PersonaPaths{
		AgentDir: filepath.Join(r.output, "solo", r.wsID, "agents", "solo", ".memory"),
	}
	resolved, err := memory.LoadPersona(soloPaths)
	if err != nil {
		t.Fatalf("LoadPersona solo: %v", err)
	}
	if !strings.Contains(resolved.Content, "Solo tone") {
		t.Errorf("solo persona not written to solo subtree; got %q", resolved.Content)
	}

	// GET resolves the agent layer for the solo agent.
	rec = httptest.NewRecorder()
	r.h.GetAgentPersona(rec, mk(http.MethodGet, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET solo: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["layer"] != "agent" || !strings.Contains(got["content"].(string), "Solo") {
		t.Errorf("expected solo agent layer; got %+v", got)
	}
}

// NOTE: SuggestAgentPersona's DecisionRejected (403) branch is NOT
// covered. The policy matrix maps ActionPersonaSuggest to inbox_approve
// (strict/guided/trusted) or auto_journal (full) for every valid
// autonomy_level — see policy.Policy.DecideAction. The DB CHECK
// constraint forbids any other level, and PersonaHandler holds a
// concrete *policy.Resolver (not an interface), so the rejected branch
// is unreachable from a black-box handler test. Skipped intentionally.

// --- 500 via fault injection: db.Close() before DB-touching handlers -------

func TestCovAPDBClosed500(t *testing.T) {
	cases := []struct {
		name string
		meth string
		call func(h *PersonaHandler) func(http.ResponseWriter, *http.Request)
		crew bool
	}{
		{"GetAgent", http.MethodGet, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.GetAgentPersona }, false},
		{"PutAgent", http.MethodPut, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.PutAgentPersona }, false},
		{"DeleteAgent", http.MethodDelete, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.DeleteAgentPersona }, false},
		{"AgentHistory", http.MethodGet, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.GetAgentPersonaHistory }, false},
		{"SuggestAgent", http.MethodPost, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.SuggestAgentPersona }, false},
		{"GetCrew", http.MethodGet, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.GetCrewPersona }, true},
		{"PutCrew", http.MethodPut, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.PutCrewPersona }, true},
		{"DeleteCrew", http.MethodDelete, func(h *PersonaHandler) func(http.ResponseWriter, *http.Request) { return h.DeleteCrewPersona }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPersonaTestRig(t)
			var body any
			if tc.meth == http.MethodPut || tc.meth == http.MethodPost {
				body = map[string]string{"content": "x"}
			}
			req := r.authedReq(t, tc.meth, "/", body)
			// Fault injection: close the DB so the lookup query fails,
			// driving the handler's 500 (non-ErrNoRows) branch.
			if err := r.h.db.Close(); err != nil {
				t.Fatalf("close db: %v", err)
			}
			rec := httptest.NewRecorder()
			tc.call(r.h)(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("%s: expected 500 on closed db; got %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- isPersonaAutoApply predicate ------------------------------------------

func TestCovAPIsPersonaAutoApply(t *testing.T) {
	autoApply := []policy.Decision{
		policy.DecisionAutoJournal,
		policy.DecisionAutoLogJournal,
		policy.DecisionAutoLogInbox,
	}
	for _, d := range autoApply {
		if !isPersonaAutoApply(d) {
			t.Errorf("expected %v to be auto-apply", d)
		}
	}
	for _, d := range []policy.Decision{policy.DecisionInboxApprove, policy.DecisionRejected} {
		if isPersonaAutoApply(d) {
			t.Errorf("expected %v NOT to be auto-apply", d)
		}
	}
}

// --- hashPersona / newAuditID helpers --------------------------------------

func TestCovAPHashAndAuditID(t *testing.T) {
	if got := hashPersona("abc"); len(got) != 64 {
		t.Errorf("hashPersona should be 64 hex chars; got %d (%q)", len(got), got)
	}
	if hashPersona("a") == hashPersona("b") {
		t.Errorf("distinct inputs should hash differently")
	}
	id1, err := newAuditID()
	if err != nil {
		t.Fatalf("newAuditID: %v", err)
	}
	id2, _ := newAuditID()
	if id1 == id2 {
		t.Errorf("audit IDs should be unique; got duplicate %q", id1)
	}
	if len(id1) != 32 {
		t.Errorf("audit ID should be 32 hex chars (16 bytes); got %d", len(id1))
	}
}
