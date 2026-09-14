package sidecar

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/llmroute"
)

// E0 sidecar identity — MEMORY PRD §4, IMPLEMENTATION §7, test T07.
//
// The defect: DeriveAgentToken is a pure HMAC of (master, workspaceID,
// agentID). Two concurrent runs of one agent therefore presented BYTE-
// IDENTICAL credentials, and identityForToken — which resolves a token by
// constant-time equality against a roster frozen at sidecar boot — could not
// tell them apart, could not attribute a call to the run that made it, and had
// no way to stop a finished run's token from working forever.
//
// These tests are about the three properties that fixes it: a run token is
// distinct per run, it VERIFIES rather than needing to be rostered (so a run
// that starts after boot is admitted without a restart), and a run the
// orchestrator has ended is refused.

const runTestMaster = "master-key-that-is-long-enough-for-hmac"

func runTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRunIdentityServer builds a sidecar booted by run "run-boot" of agent
// "agent-boot", with one other crew member holding a v1 token.
func newRunIdentityServer(t *testing.T) (*Server, string) {
	t.Helper()
	runKey := internaltoken.DeriveAgentRunKey(runTestMaster, "ws-1", "crew-1")
	if runKey == "" {
		t.Fatal("run key derivation produced nothing")
	}
	s := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			AgentID:     "agent-boot",
			AgentSlug:   "boot",
			CrewID:      "crew-1",
			WorkspaceID: "ws-1",
			ChatID:      "chat-boot",
			AgentToken:  internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot"),
			AgentRunKey: runKey,
			RunID:       "run-boot",
			RunChatID:   "chat-boot",
		},
		CrewMembers: []CrewMember{{
			ID:        "agent-peer",
			Slug:      "peer",
			AuthToken: internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-peer"),
		}},
		Logger: runTestLogger(),
	})
	return s, runKey
}

func bearerRequest(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/escalate", nil)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

// The property the whole change exists for.
func TestSidecarIdentity_TwoRunsOfOneAgentAreDistinguishable(t *testing.T) {
	s, runKey := newRunIdentityServer(t)

	tokA := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-a")
	tokB := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-b")
	if tokA == tokB {
		t.Fatal("two runs of one agent minted identical tokens")
	}

	idA, _, runA, presentA, okA := s.identityForRunToken(tokA)
	idB, _, runB, presentB, okB := s.identityForRunToken(tokB)
	if !presentA || !okA || !presentB || !okB {
		t.Fatalf("both run tokens must resolve; got A(present=%v ok=%v) B(present=%v ok=%v)",
			presentA, okA, presentB, okB)
	}
	if idA != "agent-boot" || idB != "agent-boot" {
		t.Errorf("both runs belong to agent-boot; got %q and %q", idA, idB)
	}
	// The whole point: same agent, DIFFERENT run.
	if runA == runB {
		t.Fatalf("both runs resolved to run %q — the sidecar still cannot attribute a call to a run", runA)
	}
	if runA != "run-a" || runB != "run-b" {
		t.Errorf("runs = (%q, %q), want (run-a, run-b)", runA, runB)
	}
}

// A run that starts AFTER the sidecar booted must be admitted without a
// restart. It cannot be in the roster — the roster is frozen at boot — so this
// only works because the token verifies.
func TestSidecarIdentity_RunThatStartedAfterBootIsAdmitted(t *testing.T) {
	s, runKey := newRunIdentityServer(t)

	// An agent that is not the boot agent and not in the crew roster at all,
	// on a run the sidecar has never heard of.
	tok := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-later", "run-later")
	id, _, runID, present, ok := s.identityForRunToken(tok)
	if !present || !ok {
		t.Fatal("a cryptographically valid run token was refused — a run that starts after " +
			"the sidecar booted would need a sidecar restart, which would end every other run " +
			"sharing the container")
	}
	if id != "agent-later" || runID != "run-later" {
		t.Errorf("identity = (%q, %q), want (agent-later, run-later)", id, runID)
	}
	// And it is now registered, so it can later be told it has ended.
	if _, known := s.runs.lookup("run-later"); !known {
		t.Error("a run admitted by token was not recorded, so nothing can mark it ended")
	}
}

// "The sidecar must be able to reject a token whose run is no longer current."
func TestSidecarIdentity_EndedRunIsRefused(t *testing.T) {
	s, runKey := newRunIdentityServer(t)
	tok := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-a")

	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Fatal("the run token must work while the run is live")
	}

	s.runs.end("run-a")

	id, _, runID, present, ok := s.identityForRunToken(tok)
	if ok {
		t.Fatal("a finished run's token still authenticates — a zombie runtime (a detached CLI " +
			"that outlived its run) keeps acting as a live agent")
	}
	if !present {
		t.Error("an ended run's token is still a presented token; present must stay true so the " +
			"route 403s rather than falling back to the token-less path")
	}
	if id != "" {
		t.Errorf("a refused token must resolve to no identity, got %q", id)
	}
	if runID != "run-a" {
		t.Errorf("the refusal should still name the run it refused, got %q", runID)
	}

	// Its sibling run of the SAME agent is untouched. This is the T07 shape:
	// ending B must not disturb A.
	sibling := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-b")
	if _, _, _, _, ok := s.identityForRunToken(sibling); !ok {
		t.Error("ending one run of an agent invalidated another run of the same agent")
	}
}

// An unknown run is ACCEPTED, deliberately: a dropped start notification, or a
// run older than this sidecar, must not be indistinguishable from a forgery.
func TestSidecarIdentity_UnknownRunIsAccepted(t *testing.T) {
	s, runKey := newRunIdentityServer(t)
	tok := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "never-announced")
	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Error("an unannounced but cryptographically valid run was refused; a lost control " +
			"message must not look like a forged token")
	}
}

func TestSidecarIdentity_ForgeryAndScopeAreRefused(t *testing.T) {
	s, runKey := newRunIdentityServer(t)
	otherCrewKey := internaltoken.DeriveAgentRunKey(runTestMaster, "ws-1", "crew-2")

	cases := []struct {
		name string
		tok  string
	}{
		{"garbage", "agtv2.aaa.bbb.ccc.ddd"},
		{"another crew's key", internaltoken.DeriveAgentRunToken(otherCrewKey, "ws-1", "agent-boot", "run-a")},
		{"another workspace", internaltoken.DeriveAgentRunToken(runKey, "ws-2", "agent-boot", "run-a")},
		{"tampered mac", internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-a")[:len(
			internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-a"))-1] + "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, _, _, present, ok := s.identityForRunToken(c.tok)
			if ok {
				t.Errorf("%s resolved to %q, want a refusal", c.name, id)
			}
			if !present {
				t.Errorf("%s must count as a presented token so the route fails closed", c.name)
			}
		})
	}
}

// The v1 roster path is untouched: a mid-upgrade container (an orchestrator
// that mints v2 against a sidecar that predates it, or the reverse) must keep
// working rather than losing its sidecar entirely.
func TestSidecarIdentity_V1RosterStillResolves(t *testing.T) {
	s, _ := newRunIdentityServer(t)

	for _, c := range []struct{ name, tok, wantID, wantSlug string }{
		{"boot agent", internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot"), "agent-boot", "boot"},
		{"crew member", internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-peer"), "agent-peer", "peer"},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, slug, runID, present, ok := s.identityForRunToken(c.tok)
			if !present || !ok {
				t.Fatalf("v1 token for %s no longer resolves", c.name)
			}
			if id != c.wantID || slug != c.wantSlug {
				t.Errorf("identity = (%q, %q), want (%q, %q)", id, slug, c.wantID, c.wantSlug)
			}
			// A v1 token carries no run, and callers must read "" as
			// "unknown run", never as an error.
			if runID != "" {
				t.Errorf("a v1 token reported run %q; it binds no run", runID)
			}
		})
	}
}

// Per-request chat context: the record a run produces must carry ITS chat, not
// whichever chat happened to start the shared sidecar.
func TestSidecarIdentity_ChatContextIsPerRun(t *testing.T) {
	s, runKey := newRunIdentityServer(t)

	// The boot run's chat is known from construction.
	bootTok := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-boot")
	if got := s.requestChatID(bearerRequest(bootTok)); got != "chat-boot" {
		t.Errorf("boot run chat = %q, want chat-boot", got)
	}

	// A second run of the SAME agent, dispatched for a different chat.
	s.runs.start("run-second", runState{AgentID: "agent-boot", AgentSlug: "boot", ChatID: "chat-second"})
	secondTok := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-second")
	got := s.requestChatID(bearerRequest(secondTok))
	if got != "chat-second" {
		t.Errorf("second run chat = %q, want chat-second — an escalation raised by this run "+
			"would be filed against the boot run's chat", got)
	}

	// A v1 token (no run) degrades to the boot chat rather than to nothing:
	// a record with no chat is worse than one with the old, imprecise chat.
	v1 := internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-peer")
	if got := s.requestChatID(bearerRequest(v1)); got != "chat-boot" {
		t.Errorf("v1 token chat = %q, want the boot chat as the documented fallback", got)
	}
}

func TestRunRegistry_Lifecycle(t *testing.T) {
	r := newRunRegistry()

	if !r.current(context.Background(), "never-seen") {
		t.Error("an unknown run must be current — see the type comment for why the default is accept")
	}
	if r.current(context.Background(), "") {
		t.Error("an empty run id is not a run and must not be current")
	}

	r.start("run-a", runState{AgentID: "a1", ChatID: "chat-a"})
	if !r.current(context.Background(), "run-a") {
		t.Error("a started run must be current")
	}
	r.end("run-a")
	if r.current(context.Background(), "run-a") {
		t.Error("an ended run must not be current")
	}
	// A retried start must not resurrect it: a replayed notification would
	// otherwise revive a finished run's credentials.
	r.start("run-a", runState{AgentID: "a1"})
	if r.current(context.Background(), "run-a") {
		t.Error("re-starting an ended run resurrected it")
	}

	// end() for a run never seen starting is still recorded — a run older than
	// this sidecar must still become un-current when it finishes.
	r.end("run-unseen")
	if r.current(context.Background(), "run-unseen") {
		t.Error("ending a run this sidecar never saw start had no effect")
	}
}

func TestRunRegistry_SweepKeepsLiveRuns(t *testing.T) {
	r := newRunRegistry()
	// One live run, and enough ended ones to blow the cap.
	r.start("live", runState{AgentID: "a1", ChatID: "chat-live"})
	for i := 0; i < runRegistryMaxEntries+50; i++ {
		id := "ended-" + itoaRun(i)
		r.start(id, runState{AgentID: "a1"})
		r.end(id)
	}
	r.sweep()

	if len(r.runs) > runRegistryMaxEntries {
		t.Errorf("registry still holds %d entries after a sweep, cap is %d", len(r.runs), runRegistryMaxEntries)
	}
	// Forgetting a live run would silently re-point its escalations at the
	// boot chat — the exact bug per-run context exists to fix.
	st, known := r.lookup("live")
	if !known || st.ChatID != "chat-live" {
		t.Error("the sweep dropped a LIVE run's context")
	}
}

func itoaRun(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// The constant and the route literal must agree. The literal is spelled out in
// buildHandler because the route-coverage guard reads the router's source for
// string literals; this is what keeps the two from drifting.
func TestRunEndPathMatchesTheRoute(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	if !strings.Contains(string(src), `r.URL.Path == "`+runEndPath+`"`) {
		t.Errorf("runEndPath = %q but buildHandler registers no such literal", runEndPath)
	}
	// And it must sit under a segment llmroute already reserves, or a provider
	// route prefix could shadow the endpoint that ends runs.
	seg := strings.Split(strings.TrimPrefix(runEndPath, "/"), "/")[0]
	reserved := false
	for _, r := range llmroute.ReservedPathSegments() {
		if r == seg {
			reserved = true
			break
		}
	}
	if !reserved {
		t.Errorf("run-end route's top-level segment %q is not in llmroute.ReservedPathSegments()", seg)
	}
}

// Ending a run is self-service: the token names the run. There is no "end that
// other run" verb, so a sibling cannot end a run it does not hold a token for.
func TestHandleRunEnd_EndsOnlyTheCallersOwnRun(t *testing.T) {
	s, runKey := newRunIdentityServer(t)
	tokA := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-a")
	tokB := internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", "run-b")
	// Both live.
	s.identityForRunToken(tokA)
	s.identityForRunToken(tokB)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, runEndPath, nil)
	req.Header.Set("Authorization", "Bearer "+tokA)
	s.handleRunEnd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("run end returned %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if _, _, _, _, ok := s.identityForRunToken(tokA); ok {
		t.Error("run A's token still authenticates after A ended")
	}
	if _, _, _, _, ok := s.identityForRunToken(tokB); !ok {
		t.Error("ending run A also ended run B — a sibling run of the same agent was disturbed")
	}

	// Idempotent: the orchestrator retries, and a second end must not read as
	// a failure of the first.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, runEndPath, nil)
	req2.Header.Set("Authorization", "Bearer "+tokA)
	s.handleRunEnd(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("re-ending an ended run returned %d, want 403 (its token no longer names a live run)", rec2.Code)
	}
}

func TestHandleRunEnd_RefusesTokensThatNameNoRun(t *testing.T) {
	s, _ := newRunIdentityServer(t)

	for _, c := range []struct {
		name string
		tok  string
		want int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"forged token", "agtv2.aaa.bbb.ccc.ddd", http.StatusForbidden},
		// A v1 token resolves to an agent but binds no run; ending "the
		// caller's run" is meaningless for it and must not guess.
		{"v1 token binds no run", internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-peer"), http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, runEndPath, nil)
			if c.tok != "" {
				req.Header.Set("Authorization", "Bearer "+c.tok)
			}
			s.handleRunEnd(rec, req)
			if rec.Code != c.want {
				t.Errorf("%s returned %d, want %d: %s", c.name, rec.Code, c.want, rec.Body.String())
			}
		})
	}
}
