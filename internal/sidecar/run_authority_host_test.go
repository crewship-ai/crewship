package sidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// R7, against the REAL host endpoint rather than a fake that implements what we
// hoped it would do.
//
// run_revocation_test.go proves the sidecar's POLICY with a fakeAuthority. That
// is the right tool for the policy — a fake is the only way to make the
// authority fail on command — but it cannot prove the half that actually ships:
// that the client and `GET /api/v1/internal/runs/{runId}/status` agree on the
// path, the auth header, the JSON field names, and above all on what each
// STATUS CODE means. A fake agrees with whatever the client expects, by
// construction.
//
// So everything below stands up internal/api's real Router over a real migrated
// SQLite, seeds the work ledger, and points the production client
// (newHostRunAuthority — not a hand-built struct) at it. The router is used
// whole, so serveInternal's fence and requireInternal's token check are in the
// path too: this is the same code an agent's call traverses in production.

const (
	hostAuthorityWorkspace = "ws-1" // must match runToken() in run_revocation_test.go
	hostAuthorityCrew      = "crew-durable"
	hostAuthorityAgent     = "agent-boot"
	// Long enough for auth.NewJWTValidator; nothing in these tests authenticates
	// a user, the internal surface is X-Internal-Token only.
	hostAuthorityJWTSecret = "test-jwt-secret-long-enough-for-the-validator"
)

// realRunAuthorityHost is crewshipd, as far as the sidecar can tell: the actual
// Router, the actual work ledger, over loopback.
type realRunAuthorityHost struct {
	srv   *httptest.Server
	db    *sql.DB
	token string // the crew-bound internal token a per-crew sidecar is handed
}

func newRealRunAuthorityHost(t *testing.T) *realRunAuthorityHost {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	router, err := api.NewRouter(db, hostAuthorityJWTSecret, runTestLogger(),
		api.WithInternalToken(runTestMaster))
	if err != nil {
		t.Fatalf("build the real api router: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &realRunAuthorityHost{
		srv: srv,
		db:  db,
		// #1159: a per-crew sidecar carries crwv1.<ws>.<crew>.<mac>, which is
		// exactly what the orchestrator hands it (sidecarIPCToken). Using the
		// master token here would prove nothing about the token a real sidecar
		// actually presents.
		token: internaltoken.DeriveCrewToken(runTestMaster, hostAuthorityWorkspace, hostAuthorityCrew),
	}
}

// seedLiveAttempt puts a running work item and its open, current attempt into
// the ledger — the shape the endpoint answers `active: true` for.
func (h *realRunAuthorityHost) seedLiveAttempt(t *testing.T, workID, runID string, generation int64) {
	t.Helper()
	h.seedAttempt(t, workID, runID, "running", generation, generation)
}

// seedAttempt writes the columns directly so a test can describe a state the
// accept path alone cannot produce (a superseded generation, terminal work).
func (h *realRunAuthorityHost) seedAttempt(t *testing.T, workID, runID, state string, itemGen, attemptGen int64) {
	t.Helper()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	var terminal any
	switch state {
	case "succeeded", "failed", "expired", "cancelled":
		terminal = now
	}
	if _, err := h.db.Exec(`
		INSERT INTO work_items (id, workspace_id, source, source_ref, agent_id, crew_id, session_id,
			class, input_json, input_sha256, target_revision, state, generation, attempts,
			priority, eligible_at, created_at, updated_at, terminal_at)
		VALUES (?,?,'manual','',?,?,'','background','{}','sha','rev-1',?,?,0,0,?,?,?,?)`,
		workID, hostAuthorityWorkspace, hostAuthorityAgent, hostAuthorityCrew, state, itemGen,
		now, now, now, terminal); err != nil {
		t.Fatalf("seed work item %s: %v", workID, err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner,
			lease_expires_at, heartbeat_at, started_at, runtime_phase)
		VALUES (?,?,?,?,'test',?,?,?,'confirmed')`,
		runID, workID, attemptGen, attemptGen, now, now, now); err != nil {
		t.Fatalf("seed work attempt %s: %v", runID, err)
	}
}

// endAttempt closes an attempt in the ledger WITHOUT telling the sidecar. That
// is the lost run-end notification: the orchestrator delivers the end notice
// best-effort (`curl … || true`) and nothing local ever learns it was dropped.
func (h *realRunAuthorityHost) endAttempt(t *testing.T, runID string) {
	t.Helper()
	res, err := h.db.Exec(`UPDATE work_attempts SET ended_at = ? WHERE run_id = ?`,
		time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"), runID)
	if err != nil {
		t.Fatalf("end attempt %s: %v", runID, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("end attempt %s touched %d rows; the fixture is not describing what it claims", runID, n)
	}
}

// newHostBackedRunServer is newDurableRunServer wired to a REAL host, with the
// authority switch on so the production constructor (newHostRunAuthority) builds
// the client. Two calls with one stateDir model a process restart.
func newHostBackedRunServer(t *testing.T, stateDir, baseURL, token string) (*Server, string) {
	t.Helper()
	t.Setenv(runStateDirEnv, stateDir)
	t.Setenv(runAuthorityEnv, "1")
	runKey := internaltoken.DeriveAgentRunKey(runTestMaster, hostAuthorityWorkspace, hostAuthorityCrew)
	if runKey == "" {
		t.Fatal("run key derivation produced nothing")
	}
	s := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     baseURL,
			Token:       token,
			AgentID:     hostAuthorityAgent,
			AgentSlug:   "boot",
			CrewID:      hostAuthorityCrew,
			WorkspaceID: hostAuthorityWorkspace,
			ChatID:      "chat-boot",
			ContainerID: "container-real-host",
			AgentToken:  internaltoken.DeriveAgentToken(runTestMaster, hostAuthorityWorkspace, hostAuthorityAgent),
			AgentRunKey: runKey,
			RunID:       "run-boot",
			RunChatID:   "chat-boot",
		},
		Logger: runTestLogger(),
	})
	return s, runKey
}

// fixedClock pins the registry's clock so the grace window and the verdict TTL
// are exercised without sleeping. Returns the advance function.
func pinClock(s *Server) (advance func(time.Duration)) {
	clock := time.Now()
	s.runs.now = func() time.Time { return clock }
	return func(d time.Duration) { clock = clock.Add(d) }
}

// ---------------------------------------------------------------------------
// (1) The acceptance case, end to end against the real handler.
// ---------------------------------------------------------------------------

// End A → restart the sidecar → A's token is refused, B still works — with the
// real host answering, and answering that BOTH runs are still active attempts.
//
// That last part is what makes this stronger than the fake version. The ledger
// is never told A ended; it keeps saying `active: true` for A throughout. So the
// refusal can only come from the sidecar's own durable revocation, and the test
// proves the authority cannot re-admit a locally revoked run — the failure mode
// where adding a host round trip quietly undoes the durable half of R7.
func TestRunAuthorityRealHost_RevocationSurvivesRestartAndTheHostCannotUndoIt(t *testing.T) {
	host := newRealRunAuthorityHost(t)
	host.seedLiveAttempt(t, "wk-a", "run-a", 1)
	host.seedLiveAttempt(t, "wk-b", "run-b", 1)

	stateDir := t.TempDir()
	before, runKey := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	tokA := runToken(runKey, "run-a")
	tokB := runToken(runKey, "run-b")

	// Both are live attempts in the real ledger, so both tokens work — and this
	// is the first thing that would break on an auth/path/field mismatch.
	if _, _, _, _, ok := before.identityForRunToken(tokA); !ok {
		t.Fatal("run A is a live attempt in the real work ledger but its token was refused")
	}
	// Checked AFTER the first resolution: the authority is attached lazily by
	// ensureDurable, from identityForRunToken, so that the journal replay is
	// guaranteed to precede the registry's first admission decision.
	if before.runs.authority == nil {
		t.Fatal("no host authority was built; the admission above went through local state and proves nothing")
	}
	if _, _, _, _, ok := before.identityForRunToken(tokB); !ok {
		t.Fatal("run B is a live attempt in the real work ledger but its token was refused")
	}

	if code := postRunEnd(t, before, tokA); code != http.StatusOK {
		t.Fatalf("run end for A returned %d, want 200", code)
	}
	if _, _, _, _, ok := before.identityForRunToken(tokA); ok {
		t.Fatal("A's token still authenticates in the process that ended it")
	}

	// --- restart: a genuinely fresh Server, fresh registry, same state dir ---
	after, _ := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	if len(after.runs.runs) != 1 {
		t.Fatalf("a freshly constructed server already knows %d runs; the restart is not being modelled",
			len(after.runs.runs))
	}

	active, _, err := hostSaysActive(t, host, "run-a")
	if err != nil || !active {
		t.Fatalf("fixture drift: the ledger must still call run-a an active attempt (active=%v err=%v) — "+
			"otherwise this test cannot tell a durable revocation from the host agreeing", active, err)
	}
	if _, _, _, _, ok := after.identityForRunToken(tokA); ok {
		t.Error("R7: run A's token authenticates again after a sidecar restart. The host still lists it " +
			"as an active attempt, so the only thing that could refuse it is the durable journal — and it did not")
	}
	if _, _, _, _, ok := after.identityForRunToken(tokB); !ok {
		t.Error("run B stopped working across a restart it had nothing to do with; revocation of A must not " +
			"disconnect a sibling run sharing the crew key")
	}
}

// hostSaysActive asks the real endpoint directly, so a fixture assertion never
// depends on the client under test.
func hostSaysActive(t *testing.T, host *realRunAuthorityHost, runID string) (bool, int, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, host.srv.URL+"/api/v1/internal/runs/"+runID+"/status", nil)
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("X-Internal-Token", host.token)
	resp, err := host.srv.Client().Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return false, resp.StatusCode, errors.New(string(body))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return false, resp.StatusCode, err
	}
	active, _ := out["active"].(bool)
	return active, resp.StatusCode, nil
}

// ---------------------------------------------------------------------------
// (2) An unknown run does NOT automatically gain authority when the host is down.
// ---------------------------------------------------------------------------

// The review's wording, tested as written: with the authority ENABLED and the
// host unreachable, a run with no local evidence must not be admitted
// indefinitely.
//
// The host here is a real Router that has been shut down, so the client takes
// the real connection-refused path rather than an injected error.
func TestRunAuthorityRealHost_UnknownRunDoesNotGainAuthorityWhileTheHostIsDown(t *testing.T) {
	host := newRealRunAuthorityHost(t)
	s, runKey := newHostBackedRunServer(t, t.TempDir(), host.srv.URL, host.token)
	advance := pinClock(s)

	host.srv.Close() // crewshipd is gone; nothing local knows anything about these runs

	early := runToken(runKey, "run-unknown-early")
	if _, _, _, _, ok := s.identityForRunToken(early); !ok {
		t.Fatal("a blip stranded a run that was starting; the grace window exists so it does not")
	}
	// The authority is attached lazily (ensureDurable, from identityForRunToken).
	// Without it the run above would have been admitted by the no-authority
	// branch and everything below would pass for the wrong reason.
	if s.runs.authority == nil {
		t.Fatal("the authority is not enabled; this test would pass for the wrong reason")
	}

	// Past the grace window, a run with nothing behind it is refused. Two probes
	// far apart, because "admitted indefinitely" is the exact failure.
	advance(runAuthorityGrace + time.Second)
	late := runToken(runKey, "run-unknown-late")
	if _, _, _, _, ok := s.identityForRunToken(late); ok {
		t.Error("an unverifiable run with no local evidence was admitted after the grace window — " +
			"that is R7's `unknown means current` with extra steps")
	}
	advance(24 * time.Hour)
	later := runToken(runKey, "run-unknown-much-later")
	if _, _, _, _, ok := s.identityForRunToken(later); ok {
		t.Error("a day into the outage an unknown run is still being admitted; the door never closes")
	}
}

// ---------------------------------------------------------------------------
// (3) A live run keeps working through a host outage.
// ---------------------------------------------------------------------------

// Failing closed on a blip must not kill runs that are demonstrably running.
// The evidence here is real: the run was confirmed by the actual endpoint
// before the host went away.
func TestRunAuthorityRealHost_LiveRunSurvivesTheHostGoingAway(t *testing.T) {
	host := newRealRunAuthorityHost(t)
	host.seedLiveAttempt(t, "wk-live", "run-live", 1)

	s, runKey := newHostBackedRunServer(t, t.TempDir(), host.srv.URL, host.token)
	advance := pinClock(s)
	tok := runToken(runKey, "run-live")

	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Fatal("the real ledger calls this the live attempt, but the sidecar refused it")
	}

	host.srv.Close()

	advance(runAuthorityVerdictTTL + time.Second) // the cached verdict has lapsed
	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Fatal("a transient host outage terminated a run with local evidence of being live")
	}
	advance(runAuthorityGrace * 10)
	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Error("a sustained host outage terminated a live run; an outage must not stop work that is " +
			"demonstrably running")
	}
}

// ---------------------------------------------------------------------------
// (4) The lost run-end notification.
// ---------------------------------------------------------------------------

// The run ends in the ledger and the end notice never reaches the sidecar —
// nothing local can notice, because the loss leaves no local trace. The host is
// the only thing that can catch it, and the sidecar must act on the answer AND
// record it durably, so the next process refuses without asking.
func TestRunAuthorityRealHost_LostRunEndNotificationIsCaughtAndRecorded(t *testing.T) {
	host := newRealRunAuthorityHost(t)
	host.seedLiveAttempt(t, "wk-lost", "run-lost", 1)

	stateDir := t.TempDir()
	s, runKey := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	advance := pinClock(s)
	tok := runToken(runKey, "run-lost")

	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Fatal("the run is a live attempt; it must be admitted")
	}

	// The run finishes. `curl … || true` drops the end notice on the floor.
	host.endAttempt(t, "run-lost")

	// Inside the verdict window the sidecar is still trusting its last answer.
	// That window is the exposure, and it is bounded — which is the claim.
	advance(runAuthorityVerdictTTL + time.Second)
	if _, _, _, _, ok := s.identityForRunToken(tok); ok {
		t.Fatal("the ledger no longer lists the run as an active attempt, but its token still works")
	}

	// Durable: the host is now unreachable, and the refusal must still stand in
	// a brand-new process with an empty registry.
	host.srv.Close()
	after, _ := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	if _, _, _, _, ok := after.identityForRunToken(tok); ok {
		t.Error("the authority-derived revocation did not survive the restart, so every process has to " +
			"re-derive it from a host that may not answer")
	}
}

// ---------------------------------------------------------------------------
// (5) The wire: what the client does with every status the handler can return.
// ---------------------------------------------------------------------------

// The part a fake cannot tell you. Each case drives the production client
// against the real router and asserts BOTH what the handler answered and how the
// client classified it — a verdict (err == nil) or a non-answer
// (errRunAuthorityUnanswered), which are routed to opposite policies.
func TestRunAuthorityRealHost_WireContract(t *testing.T) {
	host := newRealRunAuthorityHost(t)
	host.seedLiveAttempt(t, "wk-live", "run-live", 2)
	host.seedAttempt(t, "wk-retried", "run-ended", "running", 3, 3)
	host.endAttempt(t, "run-ended")
	host.seedAttempt(t, "wk-superseded", "run-old", "running", 9, 4)
	host.seedAttempt(t, "wk-done", "run-done", "succeeded", 1, 1)

	t.Setenv(runAuthorityEnv, "1")
	client := newHostRunAuthority(&IPCConfig{BaseURL: host.srv.URL, Token: host.token}, runTestLogger())
	if client == nil {
		t.Fatal("the production constructor built no client")
	}

	for _, tc := range []struct {
		name       string
		runID      string
		wantStatus int
		wantActive bool
		wantErr    bool
	}{
		{"the current attempt of running work", "run-live", 200, true, false},
		{"an attempt that has ended", "run-ended", 200, false, false},
		{"an attempt a later generation replaced", "run-old", 200, false, false},
		{"an attempt of work that finished", "run-done", 200, false, false},
		// The deliberate design decision on the host side: 200 + active:false,
		// NOT 404. A 404 on this surface is byte-identical to "unregistered
		// path" and to "caller refused", and the client MUST NOT read any of
		// those as a verdict.
		{"a run the ledger has never heard of", "run-ghost", 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active, status, err := hostSaysActive(t, host, tc.runID)
			if status != tc.wantStatus {
				t.Fatalf("handler answered %d, want %d (%v)", status, tc.wantStatus, err)
			}
			if active != tc.wantActive {
				t.Fatalf("handler says active=%v, want %v", active, tc.wantActive)
			}
			gotActive, gotErr := client.runIsActive(context.Background(), tc.runID)
			if (gotErr != nil) != tc.wantErr {
				t.Fatalf("client err = %v, wantErr = %v", gotErr, tc.wantErr)
			}
			if gotActive != tc.wantActive {
				t.Errorf("client read active=%v from an answer that said %v", gotActive, tc.wantActive)
			}
		})
	}

	// A 200 active:false is a VERDICT, not a failure to answer. If the client
	// wrapped it in errRunAuthorityUnanswered the run would be kept alive by the
	// unreachable-host policy instead of revoked.
	t.Run("an unknown run is a verdict, not a non-answer", func(t *testing.T) {
		active, err := client.runIsActive(context.Background(), "run-ghost")
		if err != nil {
			t.Fatalf("the client turned a 200 answer into a non-answer: %v", err)
		}
		if active {
			t.Fatal("active=true for a run the ledger has never heard of")
		}
	})

	// The field names, read off the wire rather than through the client's own
	// struct — a rename on either side would otherwise decode to the zero value
	// and silently mean "inactive".
	t.Run("the response carries run_id and active", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, host.srv.URL+"/api/v1/internal/runs/run-live/status", nil)
		req.Header.Set("X-Internal-Token", host.token)
		resp, err := host.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		if _, ok := out["run_id"]; !ok {
			t.Errorf("no run_id in %q; the client verifies the echo and would treat every answer as a non-answer", raw)
		}
		if v, ok := out["active"].(bool); !ok || !v {
			t.Errorf("no boolean `active` in %q", raw)
		}
	})

	// The run id is a PATH SEGMENT on this wire, and the client interpolates it
	// into the URL. Ids are opaque to both halves — they come out of a MAC'd
	// token, not out of this code — so an id carrying a reserved character must
	// still reach the handler as the SAME id. Before the escaping fix it did
	// not, and every failure mode looked like an unreachable authority:
	//
	//	run/slash → 404 (the segment split, the router refused the path)
	//	run?q=1   → 404 (the tail became a query the crew-token gate refused)
	//	run%2F    → an answer about "run/", caught only by the run_id echo check
	//
	// All three are "could not answer", so a live run would have been handed to
	// the unreachable-host policy by URL construction alone.
	t.Run("a run id with reserved characters round-trips", func(t *testing.T) {
		for _, runID := range []string{"run-plain-01hqx", "run with space", "run/slash", "run?q=1", "run%2F"} {
			host.seedLiveAttempt(t, "wk-esc-"+runID, runID, 1)
			active, err := client.runIsActive(context.Background(), runID)
			if err != nil {
				t.Errorf("run id %q: the client could not get an answer about a live attempt: %v", runID, err)
				continue
			}
			if !active {
				t.Errorf("run id %q: a live attempt read back as inactive", runID)
			}
		}
	})

	// A refused caller. The real router answers the same bytes for a bad token
	// as for a path that does not exist — which is why the client treats every
	// non-200 as "could not answer" rather than as "no such run".
	t.Run("a refused caller is a non-answer, never a revocation", func(t *testing.T) {
		bad := newHostRunAuthority(&IPCConfig{
			BaseURL: host.srv.URL,
			Token:   internaltoken.DeriveCrewToken(runTestMaster, hostAuthorityWorkspace, "some-other-crew") + "x",
		}, runTestLogger())
		active, err := bad.runIsActive(context.Background(), "run-live")
		if err == nil {
			t.Fatal("a refused caller was read as a verdict; anyone who can break the sidecar's credential " +
				"could then revoke every run in the crew")
		}
		if !errors.Is(err, errRunAuthorityUnanswered) {
			t.Errorf("err = %v, want it to wrap errRunAuthorityUnanswered", err)
		}
		if active {
			t.Error("active=true out of a refusal")
		}
	})

	// An unavailable ledger: 503 from the handler, a non-answer in the client.
	// Reading it as "inactive" would revoke every live run in the crew during a
	// database blip.
	t.Run("503 from an unavailable ledger is a non-answer", func(t *testing.T) {
		down := newRealRunAuthorityHost(t)
		down.seedLiveAttempt(t, "wk-x", "run-x", 1)
		if err := down.db.Close(); err != nil {
			t.Fatalf("close the ledger: %v", err)
		}
		_, status, _ := hostSaysActive(t, down, "run-x")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("handler answered %d for an unavailable ledger, want 503", status)
		}
		c := newHostRunAuthority(&IPCConfig{BaseURL: down.srv.URL, Token: down.token}, runTestLogger())
		active, err := c.runIsActive(context.Background(), "run-x")
		if err == nil {
			t.Fatal("a 503 was read as a verdict; a database blip would revoke every live run in the crew")
		}
		if !errors.Is(err, errRunAuthorityUnanswered) {
			t.Errorf("err = %v, want it to wrap errRunAuthorityUnanswered", err)
		}
		if active {
			t.Error("active=true out of a 503")
		}
	})
}

// A 503 must not become a durable revocation, and it must not be the thing that
// keeps a run alive either — it routes to the unreachable-host policy. Proven
// through the registry rather than the client, because that is where the two
// could diverge.
func TestRunAuthorityRealHost_UnavailableLedgerDoesNotRevoke(t *testing.T) {
	host := newRealRunAuthorityHost(t)
	host.seedLiveAttempt(t, "wk-live", "run-live", 1)

	stateDir := t.TempDir()
	s, runKey := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	advance := pinClock(s)
	tok := runToken(runKey, "run-live")

	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Fatal("the live attempt was refused before the ledger even broke")
	}

	if err := host.db.Close(); err != nil {
		t.Fatalf("close the ledger: %v", err)
	}
	advance(runAuthorityVerdictTTL + time.Second)
	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Fatal("a 503 revoked a live run; an unavailable ledger is not a verdict")
	}

	// And nothing was written down: a fresh process must not inherit a
	// revocation the host never actually issued.
	after, _ := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	if st, known := after.runs.lookup("run-live"); known && !st.EndedAt.IsZero() {
		t.Error("a 503 was journalled as a revocation; a database blip would outlive the blip")
	}
}

// ---------------------------------------------------------------------------
// Why the flag still ships off.
// ---------------------------------------------------------------------------

// What the switch would do to a run that is NOT in the work ledger, spelled out
// against the real handler — because this, and not the missing route, is what
// now blocks CREWSHIP_SIDECAR_RUN_AUTHORITY=1 by default.
//
// The endpoint's predicate is work_attempts JOIN work_items. A run with no
// attempt row is "no such run in the work ledger", answered 200 active:false —
// correct for that endpoint, and indistinguishable from "this run ended" for a
// caller that has no other source. The sidecar therefore does the only thing it
// can with a negative verdict: revokes, durably.
//
// Today NOTHING in production writes work_attempts. work.Store.Claim is the sole
// writer and has no non-test caller; internal/dispatch is imported by nothing.
// Meanwhile the orchestrator mints a per-run token for every crew run. So with
// the switch on, every such run — including the sidecar's own boot run — is
// locked out of every sidecar route on its first call, for the rest of the run
// and every restart after it.
//
// This test asserts that behaviour rather than wishing it away: the client and
// the handler agree perfectly, and the mismatch is that the ledger is not yet
// the register of who is running. When the dispatcher owns runs, this test's
// premise (a run with no attempt row) stops describing normal operation, and the
// default can flip.
func TestRunAuthorityRealHost_ARunAbsentFromTheLedgerIsDurablyRevoked(t *testing.T) {
	host := newRealRunAuthorityHost(t) // an empty ledger: no work items, no attempts

	stateDir := t.TempDir()
	s, runKey := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	tok := runToken(runKey, "run-not-in-the-ledger")

	if _, _, _, _, ok := s.identityForRunToken(tok); ok {
		t.Fatal("premise changed: a run absent from the work ledger was admitted with the authority on. " +
			"If the ledger is now the register of every run, re-read this test's comment — the flag's " +
			"blocker may be gone")
	}

	// And it is durable, which is what makes it a lockout rather than a blip: a
	// restart refuses it without asking anyone.
	host.srv.Close()
	after, _ := newHostBackedRunServer(t, stateDir, host.srv.URL, host.token)
	if _, _, _, _, ok := after.identityForRunToken(tok); ok {
		t.Error("the revocation did not survive the restart")
	}

	// Same for the sidecar's OWN boot run, which NewServer seeds into the
	// registry — local evidence does not save it, because a verdict outranks it.
	boot := runToken(runKey, "run-boot")
	fresh := newRealRunAuthorityHost(t)
	s2, _ := newHostBackedRunServer(t, t.TempDir(), fresh.srv.URL, fresh.token)
	if _, known := s2.runs.lookup("run-boot"); !known {
		t.Fatal("the boot run is no longer seeded; this sub-assertion no longer tests what it says")
	}
	if _, _, _, _, ok := s2.identityForRunToken(boot); ok {
		t.Error("premise changed: the boot run survived the authority probe against an empty ledger")
	}
}
