package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
)

func nativeAgentCancelCall(m *craftNativeAgentManager, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@native-agent-page/craft/tasks/cancel", strings.NewReader(`{}`)).WithContext(ctx)
	r.SetPathValue("slug", "@native-agent-page")
	w := httptest.NewRecorder()
	m.cancelTask(w, r)
	return w
}

func nativeAgentHumanIssued(t *testing.T, m *craftNativeHTTP, ctx context.Context) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@native-agent-page/craft/sessions", strings.NewReader(`{}`)).WithContext(ctx)
	r.SetPathValue("slug", "@native-agent-page")
	w := httptest.NewRecorder()
	m.sessions(w, r)
	var out struct {
		Token string `json:"session_token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Token == "" {
		t.Fatal("native agent fixture human admission", w.Code, w.Body)
	}
	return out.Token
}

func TestCraftNativeAgentHumanCancelFencesInFlightEngineAndDoesNotWaitCallMutex(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	e := m.runs[f.a.RunID]
	nativeAgentRetirementRuntime(m, e, false)
	m.dispatcher = &dispatch.Dispatcher{} // unit-only readiness, no real dispatcher/model
	w := call(`{"operation":"acquire"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	m.config.AgentApply = func(ctx context.Context, _ CraftNativeDocumentProfile, _ []byte, _ json.RawMessage) ([]byte, error) {
		close(entered)
		select {
		case <-release:
			return []byte(`{"body":["late"]}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"blocked-engine","commands":[{"command":"text.insert","params":{"text":"late"}}]}`, lease.Generation))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("engine never entered")
	}
	cancelled := make(chan *httptest.ResponseRecorder, 1)
	go func() { cancelled <- nativeAgentCancelCall(m, human) }()
	select {
	case stopped := <-cancelled:
		if stopped.Code != 200 && stopped.Code != 202 {
			unblock()
			t.Fatalf("cancel: %d %s", stopped.Code, stopped.Body)
		}
		if !strings.Contains(stopped.Body.String(), `"lease_retired":true`) {
			t.Fatal("cancel did not retire lease", stopped.Body)
		}
	case <-time.After(2 * time.Second):
		unblock()
		t.Fatal("cancel waited behind native engine callMu")
	}
	if old := call(`{"operation":"acquire"}`); old.Code != 403 {
		unblock()
		t.Fatal("withdrawn agent reacquired", old.Code, old.Body)
	}
	token := nativeAgentHumanIssued(t, m.human, human)
	if next := craftNativeLeaseCall(m.human, human, token, "@native-agent-page", "acquire", `{}`); next.Code != 200 {
		unblock()
		t.Fatal("human cannot acquire retired lease", next.Code, next.Body)
	}
	unblock()
	select {
	case completed := <-result:
		if completed.Code == 200 {
			t.Fatal("late engine save published after cancel", completed.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine request did not settle")
	}
	var head int64
	if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&head); err != nil || head != 1 {
		t.Fatal("cancel created a version", head, err)
	}
}

func TestCraftNativeAgentCancelNeverRevokesHumanHolder(t *testing.T) {
	f, m, human, _, _ := nativeAgentToolFixture(t)
	e := m.runs[f.a.RunID]
	nativeAgentRetirementRuntime(m, e, false)
	m.dispatcher = &dispatch.Dispatcher{}
	token := nativeAgentHumanIssued(t, m.human, human)
	if w := craftNativeLeaseCall(m.human, human, token, "@native-agent-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := nativeAgentCancelCall(m, human); w.Code != 403 {
		t.Fatal("human lease could be force-revoked", w.Code, w.Body)
	}
	read := craftNativeLeaseCall(m.human, human, token, "@native-agent-page", "read", "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"mine":true`) {
		t.Fatal("human lost custody", read.Code, read.Body)
	}
}
