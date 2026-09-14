package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// R7 — a run token's revocation must survive a restart of the sidecar that
// revoked it.
//
// The defect these tests exist for: the registry was in-memory only, an unknown
// run was ACCEPTED, and the crew run key is a deterministic derivation with no
// expiry in the token. End run A, restart the sidecar, and the fresh registry
// had never heard of A — so A's still-valid token was admitted again and the
// revocation was undone by a process restart. The host's guaranteed memory API
// was never the issue; it has its own work-ledger check. Every OTHER sidecar
// operation had only this registry between a finished run and a live identity.

// newDurableRunServer builds a sidecar that has a container identity, which is
// what turns the durable journal on (runJournalPath). Two calls with the same
// stateDir model a RESTART: two independent Servers, two independent
// runRegistry maps, one shared state directory — exactly what happens when the
// sidecar process is relaunched inside a long-lived crew container.
func newDurableRunServer(t *testing.T, stateDir string) (*Server, string) {
	t.Helper()
	t.Setenv(runStateDirEnv, stateDir)
	runKey := internaltoken.DeriveAgentRunKey(runTestMaster, "ws-1", "crew-durable")
	if runKey == "" {
		t.Fatal("run key derivation produced nothing")
	}
	s := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     "http://127.0.0.1:1", // never dialled unless an authority is wired
			Token:       "internal-token",
			AgentID:     "agent-boot",
			AgentSlug:   "boot",
			CrewID:      "crew-durable",
			WorkspaceID: "ws-1",
			ChatID:      "chat-boot",
			ContainerID: "container-abc123",
			AgentToken:  internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot"),
			AgentRunKey: runKey,
			RunID:       "run-boot",
			RunChatID:   "chat-boot",
		},
		Logger: runTestLogger(),
	})
	return s, runKey
}

func runToken(runKey, runID string) string {
	return internaltoken.DeriveAgentRunToken(runKey, "ws-1", "agent-boot", runID)
}

// postRunEnd drives the real /agent/run/end handler, which is how a run is
// revoked in production (the orchestrator curls it on an exec's stdin).
func postRunEnd(t *testing.T, s *Server, tok string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, runEndPath, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.handleRunEnd(rec, req)
	return rec.Code
}

// THE acceptance case: end A → restart the sidecar with the same crew
// configuration → A's token is refused and B still works.
//
// The restart is a real one. The second server is built by NewServer from the
// same configuration, so it gets a brand-new runRegistry with an empty map —
// there is no "forget everything" helper in the loop that only a test would
// call. The only thing the two processes share is the state directory, which is
// the thing under test.
func TestRunRevocation_SurvivesSidecarRestart(t *testing.T) {
	stateDir := t.TempDir()

	before, runKey := newDurableRunServer(t, stateDir)
	tokA := runToken(runKey, "run-a")
	tokB := runToken(runKey, "run-b")

	// Both runs are live and both tokens work.
	if _, _, _, _, ok := before.identityForRunToken(tokA); !ok {
		t.Fatal("run A's token must work while A is live")
	}
	if _, _, _, _, ok := before.identityForRunToken(tokB); !ok {
		t.Fatal("run B's token must work while B is live")
	}

	if code := postRunEnd(t, before, tokA); code != http.StatusOK {
		t.Fatalf("run end for A returned %d, want 200", code)
	}
	if _, _, _, _, ok := before.identityForRunToken(tokA); ok {
		t.Fatal("A's token still authenticates in the process that ended it")
	}

	// --- restart ---------------------------------------------------------
	after, _ := newDurableRunServer(t, stateDir)
	if len(after.runs.runs) != 1 {
		// Only the boot run is seeded by NewServer. If this is ever more, the
		// "fresh registry" premise of this test has quietly stopped holding.
		t.Fatalf("a freshly constructed server already knows %d runs; the restart is not being modelled",
			len(after.runs.runs))
	}

	if _, _, _, _, ok := after.identityForRunToken(tokA); ok {
		t.Error("R7: run A's token authenticates again after a sidecar restart — the revocation " +
			"lived only in the heap of the process that performed it, and A's token is still " +
			"cryptographically valid forever")
	}
	if _, _, _, _, ok := after.identityForRunToken(tokB); !ok {
		t.Error("run B was disconnected by the restart; revoking A must not revoke anyone else")
	}

	// And B can still be ended afterwards, i.e. the restored registry is a
	// working registry and not just a deny list.
	if code := postRunEnd(t, after, tokB); code != http.StatusOK {
		t.Errorf("ending B after the restart returned %d, want 200", code)
	}
	if _, _, _, _, ok := after.identityForRunToken(tokB); ok {
		t.Error("B's token still authenticates after B ended")
	}
}

// The journal is a file. If it cannot be written, the sidecar must still work
// and must still revoke in-memory — degrading to the pre-R7 behaviour for the
// NEXT process is bad, refusing to revoke in THIS one would be worse.
func TestRunRevocation_UnwritableJournalStillRevokesInMemory(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "blocked")
	// A regular file where the state directory should be: MkdirAll fails.
	if err := os.WriteFile(stateDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("set up unwritable state dir: %v", err)
	}

	s, runKey := newDurableRunServer(t, stateDir)
	tok := runToken(runKey, "run-a")
	if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
		t.Fatal("a live run's token must work even when the journal is unavailable")
	}
	if code := postRunEnd(t, s, tok); code != http.StatusOK {
		t.Fatalf("run end returned %d, want 200", code)
	}
	if _, _, _, _, ok := s.identityForRunToken(tok); ok {
		t.Error("an unwritable journal disabled in-memory revocation as well")
	}
}

// A process killed mid-append leaves a torn final line. Replay must skip it and
// keep every revocation written before it.
func TestRunJournal_TornFinalLineDoesNotLoseEarlierRevocations(t *testing.T) {
	stateDir := t.TempDir()

	before, runKey := newDurableRunServer(t, stateDir)
	tokA := runToken(runKey, "run-a")
	before.identityForRunToken(tokA)
	if code := postRunEnd(t, before, tokA); code != http.StatusOK {
		t.Fatalf("run end returned %d, want 200", code)
	}

	path := runJournalPath(before.ipc)
	if path == "" {
		t.Fatal("no journal path for a server that has a container and a crew")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := f.WriteString(`{"op":"start","run":"run-tor`); err != nil {
		t.Fatalf("write torn line: %v", err)
	}
	f.Close()

	after, _ := newDurableRunServer(t, stateDir)
	if _, _, _, _, ok := after.identityForRunToken(tokA); ok {
		t.Error("a torn trailing line threw away the revocation recorded before it")
	}
}

// Without a container identity there is no filesystem whose lifetime matches
// the runs, so the journal stays off and nothing is written. This is what keeps
// every other sidecar test — and any embedded instance — on the in-memory
// registry rather than sharing one file in /tmp.
func TestRunJournal_OffWithoutContainerIdentity(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(runStateDirEnv, stateDir)
	for _, c := range []struct {
		name string
		ipc  *IPCConfig
	}{
		{"nil ipc", nil},
		{"no container", &IPCConfig{CrewID: "crew-1"}},
		{"no crew", &IPCConfig{ContainerID: "container-1"}},
	} {
		if got := runJournalPath(c.ipc); got != "" {
			t.Errorf("%s: journal path = %q, want none", c.name, got)
		}
	}
	// And an id that tries to escape the state directory is reduced to a name.
	p := runJournalPath(&IPCConfig{ContainerID: "../../etc/passwd", CrewID: "crew/../x"})
	if filepath.Dir(p) != stateDir || strings.Contains(p, "..") {
		t.Errorf("path traversal survived into the journal path: %q", p)
	}
}

// ---------------------------------------------------------------------------
// Authority
// ---------------------------------------------------------------------------

// fakeAuthority is a runAuthority whose answer the test controls. It counts
// calls so "cached, not re-asked" is an assertion rather than an assumption.
type fakeAuthority struct {
	mu     sync.Mutex
	active bool
	err    error
	calls  int
}

func (f *fakeAuthority) runIsActive(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.active, f.err
}

func (f *fakeAuthority) set(active bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.err = active, err
}

func (f *fakeAuthority) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// A lost run-end notification is the case nothing local can see: the notice is
// delivered best-effort (`curl … || true`) and a dropped one leaves the run
// looking live forever. The authority is what catches it — within the verdict
// TTL — and the revocation it produces is durable, so the next process refuses
// without asking again.
func TestRunAuthority_LostEndNotificationIsCaughtAndRecorded(t *testing.T) {
	stateDir := t.TempDir()
	before, runKey := newDurableRunServer(t, stateDir)

	auth := &fakeAuthority{active: true}
	before.runs.authority = auth
	clock := time.Now()
	before.runs.now = func() time.Time { return clock }

	tok := runToken(runKey, "run-a")
	if _, _, _, _, ok := before.identityForRunToken(tok); !ok {
		t.Fatal("the authority said the run is active; it must be admitted")
	}
	if auth.count() != 1 {
		t.Fatalf("authority consulted %d times for the first sight of a run, want 1", auth.count())
	}

	// Inside the verdict's validity window the answer is reused.
	clock = clock.Add(runAuthorityVerdictTTL / 2)
	if _, _, _, _, ok := before.identityForRunToken(tok); !ok {
		t.Fatal("a cached positive verdict must keep admitting the run")
	}
	if auth.count() != 1 {
		t.Errorf("authority re-consulted inside the %s validity window (%d calls)",
			runAuthorityVerdictTTL, auth.count())
	}

	// The run ends. Its end notification never arrives — nothing calls end().
	auth.set(false, nil)
	clock = clock.Add(runAuthorityVerdictTTL)
	if _, _, _, _, ok := before.identityForRunToken(tok); ok {
		t.Error("the ledger no longer lists the run as an active attempt, but its token still works")
	}

	// The refusal was recorded durably, so a restart does not have to re-derive
	// it — and refuses even with no authority reachable at all.
	after, _ := newDurableRunServer(t, stateDir)
	if _, _, _, _, ok := after.identityForRunToken(tok); ok {
		t.Error("the authority-derived revocation did not survive the restart")
	}
}

// The unreachable-host policy, both directions, in one table.
//
// The split is by EVIDENCE, not by time alone: a run this registry has seen
// live keeps working through a host outage of any length (fail-closed there
// would kill live work on a blip), while a run with nothing behind it is
// admitted only inside the grace window (fail-open there is exactly the R7
// bug, just slower).
func TestRunAuthority_UnreachableHostPolicy(t *testing.T) {
	down := errors.New("dial tcp: connection refused")

	t.Run("known live run survives an outage longer than the grace window", func(t *testing.T) {
		s, runKey := newDurableRunServer(t, t.TempDir())
		auth := &fakeAuthority{active: true}
		s.runs.authority = auth
		clock := time.Now()
		s.runs.now = func() time.Time { return clock }

		tok := runToken(runKey, "run-a")
		if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
			t.Fatal("run must be admitted while the authority confirms it")
		}

		auth.set(false, down)
		clock = clock.Add(runAuthorityVerdictTTL + time.Second)
		if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
			t.Fatal("a transient authority failure terminated a run with local evidence of being live")
		}
		clock = clock.Add(runAuthorityGrace * 10)
		if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
			t.Error("a sustained authority outage terminated a live run; a host outage must not " +
				"stop work that is demonstrably running")
		}
	})

	t.Run("unverified run is admitted inside the grace window and refused after it", func(t *testing.T) {
		s, runKey := newDurableRunServer(t, t.TempDir())
		auth := &fakeAuthority{err: down}
		s.runs.authority = auth
		clock := time.Now()
		s.runs.now = func() time.Time { return clock }

		early := runToken(runKey, "run-early")
		if _, _, _, _, ok := s.identityForRunToken(early); !ok {
			t.Fatal("a blip must not strand a run that is starting")
		}

		clock = clock.Add(runAuthorityGrace + time.Second)
		late := runToken(runKey, "run-late")
		if _, _, _, _, ok := s.identityForRunToken(late); ok {
			t.Error("after the grace window an unverifiable run was still admitted — that is the " +
				"R7 default (`unknown means current`) with extra steps")
		}
	})

	t.Run("an ended run stays refused no matter what the authority says", func(t *testing.T) {
		s, runKey := newDurableRunServer(t, t.TempDir())
		tok := runToken(runKey, "run-a")
		if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
			t.Fatal("the run must work before it ends")
		}
		if code := postRunEnd(t, s, tok); code != http.StatusOK {
			t.Fatalf("run end returned %d, want 200", code)
		}
		// An authority that would happily re-admit it, and one that cannot
		// answer at all. Neither may override a local revocation.
		for _, a := range []*fakeAuthority{{active: true}, {err: down}} {
			s.runs.authority = a
			if _, _, _, _, ok := s.identityForRunToken(tok); ok {
				t.Error("a local revocation was overridden by the authority path")
			}
			if a.count() != 0 {
				t.Error("the authority was consulted about an already-revoked run; revocation must " +
					"not depend on anything that can fail")
			}
		}
	})

	t.Run("no authority configured keeps the E0 behaviour", func(t *testing.T) {
		s, runKey := newDurableRunServer(t, t.TempDir())
		if s.runs.authority != nil {
			t.Fatal("the host authority must be off unless explicitly enabled")
		}
		tok := runToken(runKey, "run-unannounced")
		if _, _, _, _, ok := s.identityForRunToken(tok); !ok {
			t.Error("with no authority, a dropped start notification must not look like a forgery")
		}
		// …but the admission is now RECORDED, which is what makes its later
		// revocation durable.
		if _, known := s.runs.lookup("run-unannounced"); !known {
			t.Error("an admitted run was not recorded, so nothing can revoke it durably")
		}
	})
}

// The host client, against a real HTTP server. Every non-200 is "could not
// answer" — including 404, which internal/api deliberately makes
// indistinguishable between "no such route" and "caller refused".
func TestHostRunAuthority_AnswerContract(t *testing.T) {
	var (
		status int
		body   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Internal-Token"); got != "internal-token" {
			t.Errorf("authority probe presented token %q", got)
		}
		if want := "/api/v1/internal/runs/run-a/status"; r.URL.Path != want {
			t.Errorf("authority probe path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	t.Setenv(runAuthorityEnv, "1")
	a := newHostRunAuthority(&IPCConfig{BaseURL: srv.URL, Token: "internal-token"}, runTestLogger())
	if a == nil {
		t.Fatal("authority not built for a sidecar that has an IPC channel and the switch on")
	}

	for _, c := range []struct {
		name       string
		status     int
		body       string
		wantActive bool
		wantErr    bool
	}{
		{"active", 200, `{"run_id":"run-a","active":true}`, true, false},
		{"not an active attempt", 200, `{"run_id":"run-a","active":false}`, false, false},
		{"404 is not an answer", 404, `{"error":"Not Found"}`, false, true},
		{"500 is not an answer", 500, `{"error":"boom"}`, false, true},
		{"unparsable body", 200, `not json`, false, true},
		{"answer about another run", 200, `{"run_id":"run-z","active":true}`, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, body = c.status, c.body
			active, err := a.runIsActive(context.Background(), "run-a")
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if err != nil && !errors.Is(err, errRunAuthorityUnanswered) {
				t.Errorf("a non-answer must be distinguishable from a negative verdict; got %v", err)
			}
			if active != c.wantActive {
				t.Errorf("active = %v, want %v", active, c.wantActive)
			}
		})
	}
}

// The switch is off by default and that is load-bearing. The route it probes
// now EXISTS (internal/api's RunStatusHandler) and the two halves are proven to
// fit in run_authority_host_test.go — what still keeps the switch off is that
// nothing in production writes work_attempts, so the ledger would answer
// "no such run" for every live run and the sidecar would revoke it. See
// hostRunAuthority's comment and
// TestRunAuthorityRealHost_ARunAbsentFromTheLedgerIsDurablyRevoked.
func TestHostRunAuthority_OffUnlessEnabled(t *testing.T) {
	ipc := &IPCConfig{BaseURL: "http://127.0.0.1:1", Token: "internal-token"}
	if a := newHostRunAuthority(ipc, runTestLogger()); a != nil {
		t.Error("the host authority is enabled without the explicit switch")
	}
	t.Setenv(runAuthorityEnv, "1")
	if a := newHostRunAuthority(&IPCConfig{}, runTestLogger()); a != nil {
		t.Error("an authority was built for a sidecar with no IPC channel to ask")
	}
	if a := newHostRunAuthority(ipc, runTestLogger()); a == nil {
		t.Error("the switch is on and there is an IPC channel, but no authority was built")
	}
}

// Compaction must not lose the state it is compacting: after it runs, the file
// still describes exactly what the registry holds.
func TestRunJournal_CompactionPreservesState(t *testing.T) {
	stateDir := t.TempDir()
	s, runKey := newDurableRunServer(t, stateDir)

	// Force the journal open and attached.
	live := runToken(runKey, "run-live")
	if _, _, _, _, ok := s.identityForRunToken(live); !ok {
		t.Fatal("run-live must be admitted")
	}
	dead := runToken(runKey, "run-dead")
	s.identityForRunToken(dead)
	if code := postRunEnd(t, s, dead); code != http.StatusOK {
		t.Fatalf("run end returned %d, want 200", code)
	}

	// Pretend the file is long, then compact.
	s.runs.journal.mu.Lock()
	s.runs.journal.records = runJournalMaxRecords + 1
	s.runs.journal.mu.Unlock()
	s.runs.compact()

	recs, err := s.runs.journal.load()
	if err != nil {
		t.Fatalf("load compacted journal: %v", err)
	}
	ended := map[string]bool{}
	started := map[string]bool{}
	for _, r := range recs {
		switch r.Op {
		case runRecordStart:
			started[r.RunID] = true
		case runRecordEnd:
			ended[r.RunID] = true
		}
	}
	if !started["run-live"] || ended["run-live"] {
		t.Error("compaction lost the live run or invented an end for it")
	}
	if !ended["run-dead"] {
		t.Error("compaction dropped a revocation")
	}

	after, _ := newDurableRunServer(t, stateDir)
	if _, _, _, _, ok := after.identityForRunToken(dead); ok {
		t.Error("a revocation did not survive compaction plus a restart")
	}
	if _, _, _, _, ok := after.identityForRunToken(live); !ok {
		t.Error("compaction plus a restart disconnected a live run")
	}
}

// A journal record is a durable credential decision, so its file must not be
// readable — or removable — by the agent sharing the container (UID 1001).
func TestRunJournal_PermissionsAreRestrictive(t *testing.T) {
	// A directory the sidecar has to CREATE: t.TempDir() already exists with
	// the test harness's own permissions, which are not the ones under test.
	stateDir := filepath.Join(t.TempDir(), "state")
	s, runKey := newDurableRunServer(t, stateDir)
	s.identityForRunToken(runToken(runKey, "run-a"))

	path := runJournalPath(s.ipc)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("journal mode = %o, want 600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("state dir mode = %o, want no group/other access", perm)
	}
}

// The record format is a contract with the next process, so it is asserted
// rather than left to whatever json.Marshal happens to produce.
func TestRunJournal_RecordFormat(t *testing.T) {
	stateDir := t.TempDir()
	s, runKey := newDurableRunServer(t, stateDir)
	tok := runToken(runKey, "run-a")
	s.identityForRunToken(tok)
	if code := postRunEnd(t, s, tok); code != http.StatusOK {
		t.Fatalf("run end returned %d, want 200", code)
	}

	raw, err := os.ReadFile(runJournalPath(s.ipc))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var sawEnd bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec runRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("journal line is not a record: %q (%v)", line, err)
		}
		if rec.RunID == "run-a" && rec.Op == runRecordEnd {
			sawEnd = true
			if rec.At.IsZero() {
				t.Error("an end record carries no timestamp; the sweep orders revocations by it")
			}
		}
	}
	if !sawEnd {
		t.Error("no end record for the run that was ended")
	}
}
