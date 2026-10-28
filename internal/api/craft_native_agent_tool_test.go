package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftNativeAgentToolClosedGrammar(t *testing.T) {
	for _, raw := range []string{`{"operation":"read"}`, `{"operation":"acquire"}`, `{"operation":"renew","generation":"7"}`, `{"operation":"release","generation":"7"}`, `{"operation":"apply","generation":"7","base_version":"9","request_id":"x:1","commands":[{"command":"text.insert","params":{"text":"x"}}]}`} {
		if _, err := parseCraftNativeAgentTool([]byte(raw)); err != nil {
			t.Fatalf("valid bounded operation %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{"operation":"read","page_id":"other"}`, `{"operation":"acquire","generation":"7"}`, `{"operation":"renew","generation":7}`, `{"operation":"renew","generation":"07"}`, `{"operation":"release","generation":"7","query":{}}`, `{"operation":"apply","generation":"7","base_version":"9","request_id":"x","commands":[]}`, `{"operation":"apply","generation":"7","base_version":"9","request_id":"x","commands":[{"params":{"text":"a","text":"b"}}]}`, `{"operation":"read","operation":"read"}`, `{"operation":"read","query":null}`} {
		if _, err := parseCraftNativeAgentTool([]byte(raw)); err == nil {
			t.Fatalf("ambiguous/foreign request accepted: %s", raw)
		}
	}
}

// SQL is seeded only to test the private binding/receipt boundary. This fixture
// is not evidence of a genuine dispatched agent or browser acceptance.
func nativeAgentToolFixture(t *testing.T) (*artifactPrincipalFixture, *craftNativeAgentManager, context.Context, func(string) *httptest.ResponseRecorder, *int) {
	t.Helper()
	f, m, human := nativeAgentAdmissionFixture(t)
	mustExec(t, f.rig.db, `PRAGMA synchronous=FULL`)
	m.human.agents = m
	f.pages.artifactMasterToken = "synthetic-native-agent-master"
	accepted, err := m.produceTask(human, "@native-agent-page", craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true})
	if err != nil {
		t.Fatal(err)
	}
	e := m.entries[accepted.WorkID]
	f.receipt = &e.grant.Original
	f.admit(t)
	payload, err := json.Marshal(map[string]any{"trigger_type": "USER", "chat_id": f.a.ChatID, "trigger_message_id": f.a.MessageID, "human_authority": f.receipt})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `UPDATE journal_entries SET payload=? WHERE trace_id=? AND entry_type='run.started'`, string(payload), f.a.RunID)

	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	mustExec(t, f.rig.db, `UPDATE work_items SET state='running',generation=1,attempts=1 WHERE id=?`, accepted.WorkID)
	mustExec(t, f.rig.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,runtime_locator,runtime_phase,started_at) VALUES(?,?,1,1,'fixture-host',?,?,'fixture-runtime','confirmed',?)`, f.a.RunID, accepted.WorkID, future, stamp, stamp)
	tx, err := f.rig.db.BeginTx(human, nil)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := m.prepareConfirmed(human, tx, e, f.a.RunID)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = m.publishConfirmed(confirmed); err != nil {
		t.Fatal(err)
	}
	cfg := mooring.NativeStoreConfig{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	m.documents.applications["wordcraft"] = craftNativeApplication{Store: cfg}
	m.config.Profiles = map[string]CraftNativeDocumentProfile{"wordcraft": {Application: "wordcraft"}}
	calls := new(int)
	m.config.AgentApply = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
		*calls++
		return []byte(`{"body":[{"kind":"para","text":"agent edited"}]}`), nil
	}
	m.config.AgentInspect = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"text":"engine view"}`), nil
	}
	versions := artifacts.NewVersionStore(f.rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err = versions.Apply(human, artifacts.VersionKey{WorkspaceID: f.rig.wsID, PageID: e.grant.Scope.ArtifactID}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {MediaType: "application/json", Content: []byte(`{"body":[]}`)}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.a.HumanUserID, nil }); err != nil {
		t.Fatal(err)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/craft-native/task", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID), ctxInternalTokenCrew, f.a.CrewID)
		w := httptest.NewRecorder()
		m.toolCall(w, r.WithContext(ctx))
		return w
	}
	return f, m, human, call, calls
}

func TestCraftNativeAgentToolHumanBusyThenRealAgentReceiptAndExactRetry(t *testing.T) {
	f, m, human, call, calls := nativeAgentToolFixture(t)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(human)
	r.SetPathValue("slug", "@native-agent-page")
	w := httptest.NewRecorder()
	m.human.sessions(w, r)
	var session struct {
		Token string `json:"session_token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatal("human admission", w.Code, w.Body)
	}
	h := craftNativeLeaseCall(m.human, human, session.Token, "@native-agent-page", "acquire", `{}`)
	if h.Code != 200 {
		t.Fatal("human acquire", h.Code, h.Body)
	}
	refused := call(`{"operation":"acquire"}`)
	if refused.Code != 409 || !strings.Contains(refused.Body.String(), `"holder_kind":"human"`) {
		t.Fatal("human busy", refused.Code, refused.Body)
	}
	if *calls != 0 {
		t.Fatal("human busy invoked engine")
	}
	var head int64
	if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&head); err != nil || head != 1 {
		t.Fatal("busy published version", head, err)
	}
	var humanLease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(h.Body.Bytes(), &humanLease)
	released := craftNativeLeaseCall(m.human, human, session.Token, "@native-agent-page", "release", fmt.Sprintf(`{"generation":%q}`, humanLease.Generation))
	if released.Code != 200 {
		t.Fatal(released.Code, released.Body)
	}
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	packet := fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"original","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, lease.Generation)
	first := call(packet)
	if first.Code != 200 {
		t.Fatal("apply", first.Code, first.Body)
	}
	var receipt mooring.NativeSaveReceipt
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.Version != "2" || receipt.SessionID != "native-agent:"+f.a.RunID || receipt.Phase != "committed" || receipt.Durability != "node" {
		t.Fatal("original agent receipt", receipt, err)
	}
	repeat := call(packet)
	if repeat.Code != 200 || repeat.Body.String() != first.Body.String() || *calls != 1 {
		t.Fatal("retry reran engine or changed receipt", repeat.Code, repeat.Body, *calls)
	}
	changed := call(strings.Replace(packet, `"text":"x"`, `"text":"changed"`, 1))
	if changed.Code != 409 || *calls != 1 {
		t.Fatal("same requestID accepted changed edit", changed.Code, *calls)
	}
	var actor, message string
	if err := f.rig.db.QueryRow(`SELECT actor,message FROM artifact_versions WHERE page_id='native-agent-page' AND version=2`).Scan(&actor, &message); err != nil || actor != "agent:"+f.a.AgentID || !strings.Contains(message, "native-agent:"+f.a.RunID) {
		t.Fatal("durable attribution", actor, message, err)
	}
	denied := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"stale-base","commands":[{}]}`, lease.Generation))
	if denied.Code != 409 || *calls != 1 {
		t.Fatal("stale base reached engine", denied.Code, *calls)
	}
	if result := call(fmt.Sprintf(`{"operation":"release","generation":%q}`, lease.Generation)); result.Code != 200 {
		t.Fatal(result.Code, result.Body)
	}
	committed, uncertain, held := craftNativeAgentCompletion(m.runs[f.a.RunID])
	if committed == nil || uncertain || held.Generation != 0 {
		t.Fatal("completion not tied to receipt and released lease")
	}
}

func TestCraftNativeAgentToolHistoricalRetryCannotClearAnotherUncertainAttempt(t *testing.T) {
	f, m, _, call, calls := nativeAgentToolFixture(t)
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	packet := func(id, base, text string) string {
		return fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":%q,"request_id":%q,"commands":[{"command":"text.insert","params":{"text":%q}}]}`, lease.Generation, base, id, text)
	}
	a := packet("A", "1", "a")
	if result := call(a); result.Code != 200 {
		t.Fatal("A", result.Code, result.Body)
	}
	// A generic database failure exercises unknown-outcome handling. The fixture
	// does not pretend this injected rollback is an actual lost COMMIT reply.
	mustExec(t, f.rig.db, `CREATE TRIGGER native_agent_uncertain BEFORE INSERT ON artifact_versions WHEN NEW.version=3 BEGIN SELECT RAISE(ABORT,'unclassified publication failure'); END`)
	if result := call(packet("B", "2", "b")); result.Code == 200 {
		t.Fatal("B unexpectedly committed")
	}
	e := m.runs[f.a.RunID]
	if !e.tool.uncertain {
		t.Fatal("uncertain branch not calibrated")
	}
	if result := call(a); result.Code != 200 {
		t.Fatal("historical A should reconcile", result.Code, result.Body)
	}
	if !e.tool.uncertain {
		t.Fatal("historical A replay cleared B uncertainty")
	}
	before := *calls
	if result := call(packet("C", "2", "c")); result.Code == 200 || *calls != before {
		t.Fatal("new C ran before exact B reconciliation", result.Code, *calls, before)
	}
}

func TestCraftNativeAgentToolHistoricalRetryCannotRegressLastCommittedReceipt(t *testing.T) {
	f, m, _, call, _ := nativeAgentToolFixture(t)
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	packet := func(id, base string) string {
		return fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":%q,"request_id":%q,"commands":[{"command":"text.insert","params":{"text":"x"}}]}`, lease.Generation, base, id)
	}
	a := packet("A", "1")
	for _, body := range []string{a, packet("B", "2"), a} {
		if result := call(body); result.Code != 200 {
			t.Fatal(result.Code, result.Body)
		}
	}
	receipt, _, _ := craftNativeAgentCompletion(m.runs[f.a.RunID])
	if receipt == nil || receipt.Version != "3" {
		t.Fatal("historical A regressed completion receipt", receipt)
	}
}

func TestCraftNativeAgentCompletionNeverWaitsForInFlightTool(t *testing.T) {
	e := &craftNativeAgentEntry{}
	e.callMu.Lock()
	done := make(chan bool, 1)
	go func() {
		receipt, uncertain, lease := craftNativeAgentCompletion(e)
		done <- receipt == nil && uncertain && lease.Generation == 0
	}()
	select {
	case safe := <-done:
		e.callMu.Unlock()
		if !safe {
			t.Fatal("in-flight tool claimed settled completion")
		}
	case <-time.After(100 * time.Millisecond):
		e.callMu.Unlock()
		<-done
		t.Fatal("completion waited for in-flight engine instead of reporting uncertainty")
	}
}

func TestCraftNativeAgentToolRevocationDuringOriginalEngineNeverPublishes(t *testing.T) {
	f, m, _, call, calls := nativeAgentToolFixture(t)
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	m.config.AgentApply = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
		*calls++
		mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
		mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.userID)
		return []byte(`{"body":[{"kind":"para","text":"must not commit"}]}`), nil
	}
	result := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"revoked-during-engine","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, lease.Generation))
	if result.Code != 403 || *calls != 1 {
		t.Fatal("revoked engine result published", result.Code, result.Body, *calls)
	}
	var head int64
	if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&head); err != nil || head != 1 {
		t.Fatal("revocation crossed Node publication", head, err)
	}
	receipt, _, _ := craftNativeAgentCompletion(m.runs[f.a.RunID])
	if receipt != nil {
		t.Fatal("revoked result claimed receipt")
	}
}

func TestCraftNativeAgentApplyRenewalRegistersAndJoinsOnRequestCancellation(t *testing.T) {
	f, m, _, call, _ := nativeAgentToolFixture(t)
	acquired := call(`{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	if acquired.Code != 200 || json.Unmarshal(acquired.Body.Bytes(), &lease) != nil {
		t.Fatal(acquired.Code, acquired.Body)
	}
	entered := make(chan bool, 1)
	m.config.AgentApply = func(ctx context.Context, _ CraftNativeDocumentProfile, _ []byte, _ json.RawMessage) ([]byte, error) {
		entered <- backgroundWorkInFlight.Load() > 0
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"cancel-renewal","commands":[{"command":"text.insert","params":{"text":"never published"}}]}`, lease.Generation)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/craft-native/task", strings.NewReader(body))
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	req.Header.Set("Authorization", "Bearer "+token)
	ctx = context.WithValue(context.WithValue(ctx, ctxInternalTokenWS, f.a.WorkspaceID), ctxInternalTokenCrew, f.a.CrewID)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); m.toolCall(w, req.WithContext(ctx)); done <- w }()
	select {
	case registered := <-entered:
		cancel()
		if !registered {
			t.Error("apply renewal was not registered before engine execution")
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("engine did not enter")
	}
	select {
	case w := <-done:
		if w.Code == 200 {
			t.Fatal("cancelled engine claimed a committed save", w.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request cancellation did not join renewal")
	}
	if !waitForBackgroundWork(5 * time.Second) {
		t.Fatal("renewal outlived cancelled apply")
	}
	var version int64
	if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&version); err != nil || version != 1 {
		t.Fatal("cancelled apply published", version, err)
	}
}
