package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"

	_ "modernc.org/sqlite"
)

// crewshipWorkspace is the workspace every fixture crew belongs to. The
// dispatcher's tenancy fence (fenceTenancy) proves crew_id resolves inside the
// RUN's workspace, so a fixture whose crew has no workspace is a fixture that
// cannot dispatch — the column and the value are part of the contract now, not
// scenery.
const crewshipWorkspace = "ws_real"

// crewshipPolicyDB builds the one table policy.Resolver reads, with a single
// crew at the requested autonomy level, in crewshipWorkspace.
func crewshipPolicyDB(t *testing.T, crewID, level string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    workspace_id TEXT,
    autonomy_level TEXT,
    behavior_mode TEXT,
    autonomy_set_by_user_id TEXT,
    autonomy_set_at TEXT,
    autonomy_reason TEXT
);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	// The escalation backlog cap counts rows here (crewship_escalation_cap.go).
	// Present and empty on every fixture so the cap answers "0 pending" rather
	// than failing closed on a missing table for verbs that never reach it.
	if _, err := db.Exec(`
CREATE TABLE escalations (
    id TEXT PRIMARY KEY,
    crew_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
);`); err != nil {
		t.Fatalf("escalations schema: %v", err)
	}
	// The chats table the tenancy fence reads for an author-supplied chat_id.
	if _, err := db.Exec(`CREATE TABLE chats (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
INSERT INTO chats (id, workspace_id) VALUES ('chat_1', '` + crewshipWorkspace + `')`); err != nil {
		t.Fatalf("chats schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, autonomy_level, behavior_mode) VALUES (?, ?, ?, 'warn')`,
		crewID, crewshipWorkspace, level); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return db
}

// capturedCall is one request the fake internal API received.
type capturedCall struct {
	method string
	path   string
	token  string
	body   map[string]any
}

// fakeInternalAPI stands in for the daemon's own internal routes.
func fakeInternalAPI(t *testing.T, calls *[]capturedCall) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		*calls = append(*calls, capturedCall{
			method: r.Method,
			path:   r.URL.Path,
			token:  r.Header.Get("X-Internal-Token"),
			body:   body,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"identifier":"ENG-42"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The master token buys no server-side workspace injection — the middleware's
// scope-pinning branches are for BOUND tokens only. So the dispatcher must
// inject identity itself, and an authored arg must never be able to replace
// it: a routine naming a foreign workspace_id or a sibling crew_id would be
// exactly the cross-tenant hole that lack of injection opens.
func TestCrewshipActions_InjectedIdentityBeatsAuthoredArgs(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_ok", "full")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	if actions == nil {
		t.Fatal("dispatcher not constructed")
	}

	out, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: "issue.create",
		Args: map[string]any{
			"title": "real title",
			// A routine author trying to act as another tenant.
			"workspace_id":    "ws_evil",
			"crew_id":         "crew_evil",
			"author_run_id":   "run_forged",
			"author_agent_id": "agent_forged",
		},
		WorkspaceID: "ws_real",
		CrewID:      "crew_ok",
		AgentID:     "agent_real",
		RunID:       "run_real",
		ChainDepth:  2,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out != `{"identifier":"ENG-42"}` {
		t.Errorf("output = %q, want the route's response body", out)
	}
	if len(calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(calls))
	}
	c := calls[0]
	if c.method != "POST" || c.path != "/api/v1/internal/issues" {
		t.Errorf("dispatched to %s %s", c.method, c.path)
	}
	if c.token != "master-token" {
		t.Errorf("X-Internal-Token = %q", c.token)
	}
	for field, want := range map[string]string{
		"workspace_id":    "ws_real",
		"crew_id":         "crew_ok",
		"author_agent_id": "agent_real",
		// The provenance that lets an automation reacting to this issue
		// resolve the originating run and inherit its chain budget.
		"author_run_id": "run_real",
	} {
		if got, _ := c.body[field].(string); got != want {
			t.Errorf("body[%q] = %q, want %q — the run's identity must win over the author's args", field, got, want)
		}
	}
	if got, _ := c.body["title"].(string); got != "real title" {
		t.Errorf("authored args must still pass through, got title=%q", got)
	}
}

// A crew that is not permitted to take the action unattended must not have it
// taken. `guided` holds mission_create for approval on the agent path; a
// routine has nobody to approve it, so a held decision is a refusal — and the
// message has to say what would unblock it, or the routine just "does
// nothing".
func TestCrewshipActions_HeldDecisionRefusesWithoutCalling(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_strict", "strict")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	_, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb:        "issue.create",
		Args:        map[string]any{"title": "x"},
		WorkspaceID: "ws_real",
		CrewID:      "crew_strict",
	})
	if err == nil {
		t.Fatal("a strict crew must not create an issue unattended")
	}
	if !strings.Contains(err.Error(), "autonomy_level=strict") {
		t.Errorf("the refusal must name the level that caused it, got %q", err)
	}
	if !strings.Contains(err.Error(), "crewship policy set") {
		t.Errorf("the refusal must name the fix, got %q", err)
	}
	if len(calls) != 0 {
		t.Errorf("made %d calls despite the refusal — the gate must run BEFORE the write", len(calls))
	}
}

// An unwired gate is not an allow. A nil resolver means the policy layer was
// never wired, and gateInternalAction's literal fallback holds everything for
// exactly that reason; the routine path must inherit the same fail-closed.
func TestCrewshipActions_NilResolverFailsClosed(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)

	actions := newCrewshipActions(srv.URL, "master-token", nil, nil, slog.Default())
	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: "issue.create", Args: map[string]any{"title": "x"},
		WorkspaceID: "ws", CrewID: "crew",
	}); err == nil {
		t.Fatal("an unwired policy resolver must fail closed, not wave the write through")
	}
	if len(calls) != 0 {
		t.Errorf("made %d calls with no policy wired", len(calls))
	}
}

// A verb the registry declares but does not govern must not be dispatchable
// even if a definition smuggles it past save (an older build, a hand-edited
// row).
func TestCrewshipActions_UngovernedVerbRefusedAtDispatch(t *testing.T) {
	var ungoverned string
	for _, v := range pipeline.CrewshipVerbs() {
		if pipeline.CrewshipVerbPolicyAction(v) == "" {
			ungoverned = v
			break
		}
	}
	if ungoverned == "" {
		// SKIP-WAIVER: not debt, and deliberately no tracking issue. The
		// dispatch-time twin of pipeline's save-time guard: it refuses a verb
		// that has no policy action, and with every verb governed there is
		// nothing to refuse. It un-skips itself when a new ungoverned verb is
		// declared, which is the only moment it has anything to say.
		t.Skip("every crewship verb now has a policy action")
	}
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_full", "full")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: ungoverned, Args: map[string]any{"identifier": "ENG-1", "body": "x"},
		WorkspaceID: "ws", CrewID: "crew_full",
	}); err == nil {
		t.Fatalf("%q has no policy action and must not dispatch", ungoverned)
	}
	if len(calls) != 0 {
		t.Errorf("made %d calls for an ungoverned verb", len(calls))
	}
}

// Route templates take their placeholder from the args, escaped — an
// identifier that renders empty must be an error, not `/issues//comments`,
// which 404s in a way nobody can read.
func TestCrewshipRoutePath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tmpl    string
		args    map[string]any
		want    string
		wantErr bool
	}{
		{"no placeholder", "/api/v1/internal/issues", nil, "/api/v1/internal/issues", false},
		{"filled", "/api/v1/internal/issues/{identifier}/comments",
			map[string]any{"identifier": "ENG-7"}, "/api/v1/internal/issues/ENG-7/comments", false},
		{"escaped", "/api/v1/internal/issues/{identifier}",
			map[string]any{"identifier": "a/b"}, "/api/v1/internal/issues/a%2Fb", false},
		{"missing", "/api/v1/internal/issues/{identifier}", map[string]any{}, "", true},
		{"blank after render", "/api/v1/internal/issues/{identifier}",
			map[string]any{"identifier": "  "}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := crewshipRoutePath(tc.tmpl, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Every verb's PolicyAction is a plain string, because internal/pipeline must
// not import internal/policy. That makes a typo compile — and then fail
// SILENTLY-ish forever: DecideAction answers its defensive InboxApprove for an
// action it has never heard of, so the verb refuses at 03:00 with a message
// about autonomy levels and never says "typo". This is the check the type
// system cannot do, on the seam where the two packages meet.
func TestCrewshipVerbs_EveryPolicyActionIsDeclared(t *testing.T) {
	for _, v := range pipeline.CrewshipVerbs() {
		name := pipeline.CrewshipVerbPolicyAction(v)
		if name == "" {
			continue // declared-but-ungoverned is refused at save, by design
		}
		if !policy.IsKnownAction(policy.Action(name)) {
			t.Errorf("verb %q is gated on policy action %q, which internal/policy does not declare — "+
				"it would refuse forever on the defensive default", v, name)
		}
	}
}

// The five verbs that shipped refused must now DISPATCH for a crew whose
// autonomy level allows them. Without this, "the Actions exist" and "the verbs
// work" are two different claims and only the first is tested.
func TestCrewshipActions_PreviouslyRefusedVerbsDispatch(t *testing.T) {
	for _, tc := range []struct {
		verb string
		args map[string]any
		path string
	}{
		{"issue.update", map[string]any{"identifier": "ENG-1", "status": "IN_PROGRESS"},
			"/api/v1/internal/issues/ENG-1"},
		{"issue.comment", map[string]any{"identifier": "ENG-1", "body": "hi"},
			"/api/v1/internal/issues/ENG-1/comments"},
		{"issue.link", map[string]any{"identifier": "ENG-1", "target_identifier": "ENG-2", "relation_type": "relates_to"},
			"/api/v1/internal/issues/ENG-1/relations"},
		{"assignment.create", map[string]any{"target_slug": "viktor", "task": "look", "chat_id": "chat_1"},
			"/api/v1/internal/assignments"},
		{"escalation.create", map[string]any{"from_slug": "lead", "reason": "stuck", "chat_id": "chat_1"},
			"/api/v1/internal/escalations"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			var calls []capturedCall
			srv := fakeInternalAPI(t, &calls)
			db := crewshipPolicyDB(t, "crew_full", "full")

			actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
			if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
				Verb: tc.verb, Args: tc.args,
				WorkspaceID: "ws_real", CrewID: "crew_full",
				AgentID: "agent_real", RunID: "run_real",
			}); err != nil {
				t.Fatalf("%s must dispatch now that it is governed: %v", tc.verb, err)
			}
			if len(calls) != 1 {
				t.Fatalf("made %d calls, want 1", len(calls))
			}
			if calls[0].path != tc.path {
				t.Errorf("dispatched to %q, want %q", calls[0].path, tc.path)
			}
		})
	}
}

// The acting agent must come from the RUN under BOTH names the internal routes
// read it by. agent_id is what issue.comment requires (a comment with no author
// is a 400), and actor_agent_id is what the delegation cap measures depth from —
// an authored value there is depth laundering, which is the exact failure
// delegation_limits.go was written against. This test reddens if either is
// merged the other way round.
func TestCrewshipActions_ActingAgentIsInjectedUnderBothNames(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_full", "full")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: "assignment.create",
		Args: map[string]any{
			"target_slug": "viktor", "task": "look", "chat_id": "chat_1",
			// A routine author claiming to be somebody shallower in the tree.
			"agent_id":       "agent_forged",
			"actor_agent_id": "agent_forged",
		},
		WorkspaceID: "ws_real", CrewID: "crew_full", AgentID: "agent_real",
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(calls))
	}
	for _, field := range []string{"agent_id", "actor_agent_id"} {
		if got, _ := calls[0].body[field].(string); got != "agent_real" {
			t.Errorf("body[%q] = %q, want %q — the run's acting agent must win over the author's args",
				field, got, "agent_real")
		}
	}
}

// The escalation backlog cap: the bound the autonomy matrix structurally cannot
// express. escalation_create is allowed at EVERY autonomy level — including
// full, used here — so if this number does not hold, nothing does.
func TestCrewshipActions_EscalationCapRefusesOnABacklog(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_full", "full")

	// One under the default: still allowed.
	for i := 0; i < defaultEscalationMaxPendingPerCrew-1; i++ {
		if _, err := db.Exec(`INSERT INTO escalations (id, crew_id, status) VALUES (?, 'crew_full', 'PENDING')`,
			"esc_"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	req := pipeline.CrewshipRequest{
		Verb:        "escalation.create",
		Args:        map[string]any{"from_slug": "lead", "reason": "stuck", "chat_id": "chat_1"},
		WorkspaceID: "ws_real", CrewID: "crew_full", AgentID: "agent_real",
	}
	if _, err := actions.Do(context.Background(), req); err != nil {
		t.Fatalf("under the cap must still escalate: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(calls))
	}

	// One more unresolved row puts the crew AT the limit — the next routine
	// escalation is refused, without a call.
	if _, err := db.Exec(`INSERT INTO escalations (id, crew_id, status) VALUES ('esc_last', 'crew_full', 'PENDING')`); err != nil {
		t.Fatal(err)
	}
	_, err := actions.Do(context.Background(), req)
	if err == nil {
		t.Fatal("a crew at its escalation backlog limit must not raise another from a routine")
	}
	if !strings.Contains(err.Error(), SettingEscalationMaxPendingPerCrew) {
		t.Errorf("the refusal must name the setting an operator would change, got %q", err)
	}
	if len(calls) != 1 {
		t.Errorf("made %d calls — the cap must run BEFORE the write", len(calls))
	}

	// Resolving the queue gives the budget back. That is the point of counting a
	// BACKLOG rather than a rate: no window, no timer, nothing to reset.
	if _, err := db.Exec(`UPDATE escalations SET status = 'RESOLVED' WHERE crew_id = 'crew_full'`); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Do(context.Background(), req); err != nil {
		t.Fatalf("a resolved queue must restore the budget: %v", err)
	}
}

// A cap that cannot read its own state has not established that this escalation
// is inside it. Paging a human anyway is the unbounded behaviour it exists to
// end, so an unreadable table refuses.
func TestCrewshipActions_EscalationCapFailsClosedOnUnreadableState(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_full", "full")
	if _, err := db.Exec(`DROP TABLE escalations`); err != nil {
		t.Fatal(err)
	}

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb:        "escalation.create",
		Args:        map[string]any{"from_slug": "lead", "reason": "stuck", "chat_id": "chat_1"},
		WorkspaceID: "ws_real", CrewID: "crew_full",
	}); err == nil {
		t.Fatal("an unreadable escalation count must refuse, not proceed")
	}
	if len(calls) != 0 {
		t.Errorf("made %d calls despite an unreadable cap", len(calls))
	}
}

// The case the injection comment argues for and no test pinned: an EMPTY
// acting agent.
//
// buildCrewshipBody defends the forged-identity case twice — it deletes the
// author's identity keys, then sets its own unconditionally. With a non-empty
// AgentID either layer alone is enough, so removing one changes nothing a test
// can see, and both were removable with the suite still green.
//
// The empty case is where they stop being redundant. A routine with no author
// agent that sets these keys itself is exactly the shape where forging one is
// worth something: `actor_agent_id` is what the delegation cap measures depth
// from, so a surviving author value is depth laundering — the failure
// delegation_limits.go exists in order not to have.
func TestCrewshipActions_ForgedIdentityDiesEvenWithNoActingAgent(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_full", "full")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: "assignment.create",
		Args: map[string]any{
			"target_slug": "viktor", "task": "look", "chat_id": "chat_1",
			"agent_id":       "agent_forged",
			"actor_agent_id": "agent_forged",
		},
		WorkspaceID: "ws_real", CrewID: "crew_full",
		// No acting agent — a schedule-fired routine with no author.
		AgentID: "",
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(calls))
	}
	for _, field := range []string{"agent_id", "actor_agent_id"} {
		got, _ := calls[0].body[field].(string)
		if got == "agent_forged" {
			t.Errorf("body[%q] kept the author's forged value — with no acting agent to overwrite it, "+
				"the delete is the only thing standing between a routine and laundered delegation depth", field)
		}
		if got != "" {
			t.Errorf("body[%q] = %q, want empty: there is no acting agent to claim", field, got)
		}
	}
}

// A redirect is where a master credential leaves the machine.
//
// The dispatcher sends X-Internal-Token — the MASTER internal token, the one
// that buys unscoped access to every internal route — on a loopback call. Go's
// default client follows up to ten redirects, and it only strips headers it
// knows are credentials (Authorization, Cookie, WWW-Authenticate). X-Internal-
// Token is not on that list, so a 3xx pointing anywhere would carry the token
// there, to any host, with no error and no log line.
//
// The far server here is a stand-in for that "anywhere": it records the token it
// received, so a passing test is the claim that nothing arrived — not that the
// response happened to be discarded.
func TestCrewshipActions_RedirectIsNotFollowedAndTheMasterTokenStaysHome(t *testing.T) {
	var stolen []string
	offHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stolen = append(stolen, r.Header.Get("X-Internal-Token"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"identifier":"ENG-42"}`))
	}))
	t.Cleanup(offHost.Close)

	// The daemon's own address, answering the internal route with a 302. No
	// handler writes one today (see the CheckRedirect comment in
	// crewship_actions.go); one http.Redirect, or a proxy in front of the
	// loopback address, is the whole distance between here and there.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, offHost.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	db := crewshipPolicyDB(t, "crew_ok", "full")
	actions := newCrewshipActions(redirector.URL, "master-token", policy.NewResolver(db), db, slog.Default())
	if actions == nil {
		t.Fatal("dispatcher not constructed")
	}

	out, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb:        "issue.create",
		Args:        map[string]any{"title": "real title"},
		WorkspaceID: "ws_real",
		CrewID:      "crew_ok",
		AgentID:     "agent_real",
		RunID:       "run_real",
	})

	if len(stolen) != 0 {
		t.Errorf("the redirect was followed and the master internal token was sent off-host: %q — "+
			"Go copies every header it does not recognise as a credential across hosts, and "+
			"X-Internal-Token is not one it recognises", stolen)
	}
	// The 3xx itself has to surface as the failure. Swallowing it would leave a
	// routine step silently doing nothing, which is the other half of why the
	// redirect must not be followed: the step's job was to reach OUR route.
	if err == nil {
		t.Fatalf("Do returned no error for a 302; got output %q", out)
	}
	if !strings.Contains(err.Error(), "302") {
		t.Errorf("error = %v, want it to name the 302 the daemon answered with", err)
	}
}
