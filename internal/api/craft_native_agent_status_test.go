package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func nativeAgentStatusCall(m *craftNativeAgentManager, ctx context.Context, workID, slug string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/pages/unused/craft/tasks/unused", nil).WithContext(ctx)
	r.SetPathValue("workID", workID)
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	m.taskStatus(w, r)
	return w
}
func TestCraftNativeAgentStatusProjectsOnlyActualToolOutcomes(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	acquire := call(`{"operation":"acquire"}`)
	if acquire.Code != 200 {
		t.Fatal(acquire.Code, acquire.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquire.Body.Bytes(), &lease)
	saved := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"status-proof","commands":[{"command":"text.insert","params":{"text":"private-command-text"}}]}`, lease.Generation))
	if saved.Code != 200 {
		t.Fatal(saved.Code, saved.Body)
	}
	e := m.runs[f.a.RunID]
	response := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	var status struct {
		Events    []craftNativeAgentToolEvent `json:"events"`
		Receipt   json.RawMessage             `json:"last_committed_receipt"`
		Uncertain bool                        `json:"uncertain"`
		Inflight  bool                        `json:"operation_inflight"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || len(status.Events) != 2 || status.Uncertain || status.Inflight {
		t.Fatal("actual tool status", response.Body, err)
	}
	if status.Events[0].Operation != "acquire" || status.Events[0].Status != 200 || status.Events[1].Operation != "apply" || status.Events[1].Status != 200 || status.Events[1].Receipt == nil || status.Events[1].Receipt.Version != "2" {
		t.Fatal("projected outcome was not actual tool event", status.Events)
	}
	var receipt struct{ Version, Durability, SessionID string }
	if err := json.Unmarshal(status.Receipt, &receipt); err != nil || receipt.Version != "2" || receipt.Durability != "node" || receipt.SessionID != "native-agent:"+f.a.RunID {
		t.Fatal("status did not preserve actual receipt", receipt, err)
	}
	for _, private := range []string{"private-command-text", "make report", "commands", "params", "bearerHash", "original_human", "synthetic-native-agent-master"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("task status leaked private input or credential", private)
		}
	}
	// Closing a grant stops all tool access but does not erase its actual outcome.
	m.mu.Lock()
	e.closed = true
	m.mu.Unlock()
	if again := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page"); again.Code != 200 || !strings.Contains(again.Body.String(), `"grant_closed":true`) {
		t.Fatal("closed history unavailable", again.Code, again.Body)
	}
	if denied := call(`{"operation":"read"}`); denied.Code != 403 {
		t.Fatal("status reopened a closed agent grant", denied.Code)
	}
}
func TestCraftNativeAgentStatusNeverBlocksBehindEngineAndRechecksHumanPageRights(t *testing.T) {
	f, m, human, _, _ := nativeAgentToolFixture(t)
	e := m.runs[f.a.RunID]
	e.callMu.Lock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- nativeAgentStatusCall(m, human, e.workID, "@native-agent-page") }()
	select {
	case response := <-done:
		e.callMu.Unlock()
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"operation_inflight":true`) || strings.Contains(response.Body.String(), "last_committed_receipt") {
			t.Fatal("in-flight status pretended settled outcome", response.Code, response.Body)
		}
	case <-time.After(time.Second):
		e.callMu.Unlock()
		<-done
		t.Fatal("human status waited for the agent engine")
	}
	mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
	if response := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page"); response.Code != 403 {
		t.Fatal("revoked human retained private task status", response.Code, response.Body)
	}
}

// These are valid current credentials, not a spoofed/expired AuthUser stand-in.
func nativeAgentStatusFreshHuman(t *testing.T, f *artifactPrincipalFixture, user, workspace string) (context.Context, auth.OriginalCredential) {
	t.Helper()
	expiry := time.Now().Add(time.Hour).UTC()
	session := "status-fresh-" + user + "-" + workspace
	mustExec(t, f.rig.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES(?,?,?)`, session, user, expiry.Format(time.RFC3339Nano))
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: user, SessionID: session, CredentialExpiresAt: expiry.Add(-time.Minute)}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u := &AuthUser{ID: user, SessionID: session, OriginalCredential: source}
	ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, workspace), *source)
	if _, err = validateOriginalHumanCredential(ctx, f.rig.db, *source, "pages:write"); err != nil {
		t.Fatal("positive current source calibration", err)
	}
	return ctx, *source
}
func nativeAgentStatusProveWrite(t *testing.T, m *craftNativeAgentManager, ctx context.Context, source auth.OriginalCredential, workspace, page string) {
	t.Helper()
	tx, err := m.writer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = m.human.pageAuthority(ctx, tx, source, mooring.EditLeaseScope{WorkspaceID: workspace, ArtifactID: page}, true); err != nil {
		t.Fatal("positive canonical Pagewrite calibration", err)
	}
}
func TestCraftNativeAgentStatusRejectsOtherCurrentHumanEvenWithCanonicalPageWrite(t *testing.T) {
	f, m, human, _, _ := nativeAgentToolFixture(t)
	e := m.runs[f.a.RunID]
	if got := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page"); got.Code != 200 {
		t.Fatal("original positive", got.Code, got.Body)
	}
	mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
	other, source := nativeAgentStatusFreshHuman(t, f, f.rig.adminID, f.rig.wsID)
	nativeAgentStatusProveWrite(t, m, other, source, f.rig.wsID, "native-agent-page")
	if got := nativeAgentStatusCall(m, other, e.workID, "@native-agent-page"); got.Code != 404 {
		t.Fatal("new owner read original human's private task", got.Code, got.Body)
	}
}
func TestCraftNativeAgentStatusRejectsOtherPageAndWorkspaceAfterPositiveWriteCalibration(t *testing.T) {
	for _, otherWorkspace := range []bool{false, true} {
		t.Run(fmt.Sprintf("otherWorkspace=%t", otherWorkspace), func(t *testing.T) {
			f, m, _, _, _ := nativeAgentToolFixture(t)
			e := m.runs[f.a.RunID]
			ws := f.rig.wsID
			if otherWorkspace {
				ws = "status-other-workspace"
				mustExec(t, f.rig.db, `INSERT INTO workspaces(id,name,slug)VALUES(?,'StatusOther','status-other')`, ws)
				mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role)VALUES('status-other-member',?,?,'OWNER')`, ws, f.a.HumanUserID)
			}
			mustExec(t, f.rig.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json)VALUES('status-other-page',?,'status-other-page','Other',?,'{}')`, ws, f.a.HumanUserID)
			mustExec(t, f.rig.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) SELECT 'status-other-page',?,application_id,engine_revision,engine_sha256,editor_bundle FROM craft_native_documents WHERE page_id='native-agent-page'`, ws)
			other, source := nativeAgentStatusFreshHuman(t, f, f.a.HumanUserID, ws)
			nativeAgentStatusProveWrite(t, m, other, source, ws, "status-other-page")
			if got := nativeAgentStatusCall(m, other, e.workID, "@status-other-page"); got.Code != 404 {
				t.Fatal("task crossed current Page/workspace scope", got.Code, got.Body)
			}
		})
	}
}
func TestCraftNativeAgentStatusBoundsActualHistoryAndNeverIncludesPrivateToolOutput(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	e := m.runs[f.a.RunID]
	const private = "PRIVATE_NATIVE_BODY_AND_TOOL_OUTPUT"
	m.config.AgentInspect = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"text":"` + private + `"}`), nil
	}
	for n := 0; n < 70; n++ {
		w := call(`{"operation":"read","query":{"command":"text","params":{}}}`)
		if w.Code != 200 || !strings.Contains(w.Body.String(), private) {
			t.Fatal("actual private output calibration", n, w.Code)
		}
	}
	got := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page")
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body)
	}
	var status struct {
		Events []craftNativeAgentToolEvent `json:"events"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &status); err != nil || len(status.Events) != 64 {
		t.Fatal("unbounded or missing completed events", len(status.Events), err)
	}
	for _, event := range status.Events {
		if event.Operation != "read" || event.Status != 200 || event.Receipt != nil || event.HolderKind != "" {
			t.Fatal("invented event metadata", event)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.At); err != nil {
			t.Fatal(err)
		}
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	for _, secret := range []string{private, token, "synthetic-native-agent-master", "make report", `"commands"`, `"params"`, `"query"`, `"input"`} {
		if strings.Contains(got.Body.String(), secret) {
			t.Fatal("private task projection exposed private payload or token")
		}
	}
}
func TestCraftNativeAgentStatusDoesNotInventHTTP200ForPanickedTool(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	e := m.runs[f.a.RunID]
	reached := false
	m.config.AgentInspect = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
		reached = true
		panic("unit native callback panic")
	}
	func() {
		defer func() { _ = recover() }()
		call(`{"operation":"read","query":{"command":"text","params":{}}}`)
	}()
	if !reached {
		t.Fatal("callback panic was not actually reached")
	}

	got := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page")
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body)
	}
	var status struct {
		Events []craftNativeAgentToolEvent `json:"events"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, event := range status.Events {
		if event.Operation == "read" && event.Status == 200 {
			t.Fatal("panicked tool without any HTTP response was projected as actual HTTP200", got.Body)
		}
	}
}

func TestCraftNativeAgentToolPanicDuringPublicationKeepsExactUnknownAttemptFence(t *testing.T) {
	f, m, _, call, _ := nativeAgentToolFixture(t)
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	profile := m.documents.applications["wordcraft"]
	profile.Store.Validate = func(context.Context, []byte) error { panic("synthetic publication panic") }
	m.documents.applications["wordcraft"] = profile
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"panic-unknown","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, lease.Generation))
	}()
	if !panicked {
		t.Fatal("panic branch not calibrated")
	}
	e := m.runs[f.a.RunID]
	if !e.tool.uncertain || e.tool.uncertainAttempt == nil || e.tool.uncertainAttempt != e.tool.currentAttempt {
		t.Fatal("publication panic lost exact unknown-attempt fence")
	}
	for _, event := range e.tool.events {
		if event.Operation == "apply" && event.Status == 200 {
			t.Fatal("publication panic invented successful HTTP event")
		}
	}
}

func TestCraftNativeAgentStatusPreservesActualBusyAndEngineRefusalHTTPEvents(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	e := m.runs[f.a.RunID]
	token := nativeAgentHumanIssued(t, m.human, human)
	if w := craftNativeLeaseCall(m.human, human, token, "@native-agent-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal("positive real human holder", w.Code, w.Body)
	}
	if w := call(`{"operation":"acquire"}`); w.Code != 409 || !strings.Contains(w.Body.String(), `"holder_kind":"human"`) {
		t.Fatal("actual busy refusal", w.Code, w.Body)
	}
	m.config.AgentInspect = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
		return nil, ErrCraftNativeAgentEngine
	}
	if w := call(`{"operation":"read","query":{"command":"text","params":{}}}`); w.Code != 422 {
		t.Fatal("actual engine refusal", w.Code, w.Body)
	}
	got := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page")
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body)
	}
	var status struct {
		Events  []craftNativeAgentToolEvent `json:"events"`
		Receipt json.RawMessage             `json:"last_committed_receipt"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &status); err != nil || len(status.Events) != 2 || status.Receipt != nil {
		t.Fatal("actual refusal history", got.Body, err)
	}
	if status.Events[0].Operation != "acquire" || status.Events[0].Status != 409 || status.Events[0].HolderKind != "human" || status.Events[0].Receipt != nil {
		t.Fatal("actual human busy lost", status.Events[0])
	}
	if status.Events[1].Operation != "read" || status.Events[1].Status != 422 || status.Events[1].HolderKind != "" || status.Events[1].Receipt != nil {
		t.Fatal("actual engine refusal lost", status.Events[1])
	}
	var versions int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='native-agent-page'`).Scan(&versions); err != nil || versions != 1 {
		t.Fatal("refusal invented a Node version", versions, err)
	}
}
