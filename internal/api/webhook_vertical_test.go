package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/work"
)

// The vertical pass, through the production assembly.
//
// internal/dispatch already proves the loop against a Runtime it controls, and
// internal/work proves the ledger against a real database. Neither can prove
// the thing that actually broke: that the two halves are JOINED — that the
// route the server registers hands work to a dispatcher the server starts, that
// the input one half writes is the input the other half reads, that a hint
// nobody sends still runs, and that one delivery travels one path.
//
// So everything here is real except the operating-system process at the bottom:
// NewRouter registers the routes, a real http.Server serves them, the resolver
// makes real HTTP calls back into this server's internal API, acceptance takes
// the real transaction, StartWebhookDispatcher builds the real dispatcher with
// the real authorizer, and the ledger is real SQLite. Substituted: the
// [agentRunner] — "start a process, stop it, is it there" — because a test
// cannot fork a Claude CLI, and the container provider beneath it.
//
// That boundary is chosen, not convenient. Everything above it is the code that
// ships; the layer below it is covered separately by the mock-crash test in
// internal/dispatch (faster, and able to crash on demand) and by the real-CLI
// runs T06/T07 (slower, and not yet performed). The handoff says which of the
// three any given claim rests on.

// ---------------------------------------------------------------------------
// The one substituted layer: an agent process the test can hold still.
// ---------------------------------------------------------------------------

// fakeAgentProcess stands in for the operating-system process. It implements
// exactly [agentRunner] — the three calls the webhook path makes into the
// orchestrator — and nothing else is faked anywhere in this file.
type fakeAgentProcess struct {
	mu sync.Mutex
	// started records every run id RunAgent was called with, in order. Its
	// length is the count of runtimes that ever existed, which is what most of
	// these tests are really asserting about.
	started []string
	live    map[string]chan struct{}
	stopped map[string]bool
	// refuseStop makes Stop answer "asked, still there" — a process that
	// ignores its signal. §4: that is not a cancellation.
	refuseStop bool
	// hold, when non-nil, blocks every run until it is closed.
	hold chan struct{}
	// failWith is returned by RunAgent once it is released.
	failWith error
	// emitEvent makes the run produce one stream event, which is the FAST
	// confirmation hint. Off by default, so the default run in these tests is a
	// silent one and confirmation has to come from the probe.
	emitEvent bool
}

func newFakeAgentProcess() *fakeAgentProcess {
	return &fakeAgentProcess{live: map[string]chan struct{}{}, stopped: map[string]bool{}}
}

func (f *fakeAgentProcess) RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, handler orchestrator.EventHandler) error {
	f.mu.Lock()
	f.started = append(f.started, req.RunID)
	gone := make(chan struct{})
	f.live[req.RunID] = gone
	hold, emit, fail := f.hold, f.emitEvent, f.failWith
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		delete(f.live, req.RunID)
		f.mu.Unlock()
	}()

	if emit && handler != nil {
		handler(orchestrator.AgentEvent{Type: "text", Content: "working"})
	}
	if hold != nil {
		select {
		case <-hold:
		case <-gone:
			return context.Canceled
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fail
}

func (f *fakeAgentProcess) StopRun(_ context.Context, runID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuseStop {
		return false, nil
	}
	if gone, ok := f.live[runID]; ok {
		delete(f.live, runID)
		close(gone)
	}
	f.stopped[runID] = true
	return true, nil
}

func (f *fakeAgentProcess) RunIsAlive(_ context.Context, runID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.live[runID]
	return ok, nil
}

func (f *fakeAgentProcess) runsStarted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...)
}

func (f *fakeAgentProcess) wasStopped(runID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped[runID]
}

var _ agentRunner = (*fakeAgentProcess)(nil)

// verticalContainer is the crew runtime. crewstart calls EnsureCrewRuntime and
// nothing here is asserted about it — it exists so the production code path
// reaches the agent rather than stopping at "no container provider".
type verticalContainer struct{}

func (verticalContainer) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "container-vertical", nil
}
func (verticalContainer) StopCrewRuntime(context.Context, string) error   { return nil }
func (verticalContainer) RemoveCrewRuntime(context.Context, string) error { return nil }
func (verticalContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (verticalContainer) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (verticalContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (verticalContainer) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (verticalContainer) CrewContainerName(_ string, slug string) string { return "crew-" + slug }
func (verticalContainer) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*verticalContainer)(nil)

// ---------------------------------------------------------------------------
// The rig: one server's worth of the webhook path.
// ---------------------------------------------------------------------------

type verticalRig struct {
	t     *testing.T
	db    *sql.DB
	store *work.Store
	proc  *fakeAgentProcess

	router  *Router
	srv     *http.Server
	ln      net.Listener
	baseURL string

	wsID, crewID, agentID, secret string

	stopDispatcher func()
	closeOnce      sync.Once
}

// newVerticalRig builds and starts the server. The dispatcher is NOT started:
// some of these tests need the window between "accepted" and "claimed", which
// is where a cancel or a revocation actually lands.
func newVerticalRig(t *testing.T) *verticalRig {
	t.Helper()
	setTestEncryptionKey(t)
	db := setupTestDB(t)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// The agent id is derived from the test name because the per-agent ingress
	// rate gate is a process-global map: two tests sharing an agent id would
	// share its budget, and the second one would start seeing 429s that have
	// nothing to do with what it is testing.
	agentID := "vert-agent-" + sanitizeForID(t.Name())
	crewID := "vert-crew-" + sanitizeForID(t.Name())
	secret := "vertical-webhook-secret"
	seedWebhookSecretAgent(t, db, wsID, crewID, agentID, secret)

	// Bind first, so the router can be told the URL of the server that will
	// serve it. The resolver this wires up makes REAL HTTP calls back into this
	// same process's internal API — resolve, create chat, create run, update
	// run all travel the wire the daemon uses.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	baseURL := "http://" + ln.Addr().String()

	rig := &verticalRig{
		t: t, db: db, store: work.NewStore(db), proc: newFakeAgentProcess(),
		ln: ln, baseURL: baseURL,
		wsID: wsID, crewID: crewID, agentID: agentID, secret: secret,
	}

	router, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", quietLogger(),
		WithInternalToken("vertical-internal-token"),
		WithInternalLoopbackURL(baseURL),
		// A non-nil orchestrator is one of the four conditions the production
		// route registration checks. The webhook path's use of it is replaced
		// below; nothing else in this test touches it.
		WithOrchestrator(&orchestrator.Orchestrator{}),
		WithKeeperContainer(verticalContainer{}),
		WithLogWriter(logcollector.NewWriter(t.TempDir(), quietLogger())),
		// The journal is not decoration here: the internal run-create route
		// emits run.started and answers 500 when nothing is wired to take it,
		// which fails the run before the agent is reached. The daemon wires
		// this; a test that did not would be testing a server nobody deploys.
		WithJournal(journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if router.webhookHandler == nil {
		t.Fatal("the production route registration did not build a webhook handler; " +
			"every claim in this file would be vacuous")
	}
	// The ONE substitution, at the process boundary.
	router.webhookHandler.orch = rig.proc
	rig.router = router

	rig.srv = &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = rig.srv.Serve(ln) }()
	t.Cleanup(rig.close)
	return rig
}

func (rig *verticalRig) close() {
	rig.closeOnce.Do(func() {
		if rig.stopDispatcher != nil {
			rig.stopDispatcher()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rig.srv.Shutdown(ctx)
	})
}

// startDispatcher runs the production assembly.
func (rig *verticalRig) startDispatcher() {
	rig.t.Helper()
	stop, err := rig.router.StartWebhookDispatcher(context.Background(), quietLogger())
	if err != nil {
		rig.t.Fatalf("StartWebhookDispatcher: %v", err)
	}
	rig.stopDispatcher = stop
}

type receipt struct {
	Status     int
	DeliveryID string `json:"delivery_id"`
	WorkID     string `json:"work_id"`
	State      string `json:"status"`
	Duplicate  bool   `json:"duplicate"`
}

// deliver posts a signed webhook over real HTTP to the real route.
func (rig *verticalRig) deliver(body string) receipt {
	rig.t.Helper()
	url := fmt.Sprintf("%s/api/v1/webhooks/%s/%s/trigger", rig.baseURL, rig.crewID, rig.agentID)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		rig.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", webhook.ComputeHMAC([]byte(body), rig.secret))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		rig.t.Fatalf("POST the webhook: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		rig.t.Fatalf("receipt %d is not JSON: %s", resp.StatusCode, raw)
	}
	r.Status = resp.StatusCode
	return r
}

func (rig *verticalRig) waitForState(workID string, want ...work.State) *work.Item {
	rig.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last work.State
	for time.Now().Before(deadline) {
		it, err := rig.store.Get(context.Background(), workID)
		if err == nil {
			last = it.State
			for _, w := range want {
				if it.State == w {
					return it
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	rig.t.Fatalf("work %s is %q after 30s, want one of %v", workID, last, want)
	return nil
}

// attemptRunID returns the run id of the work's single attempt.
func (rig *verticalRig) attemptRunID(workID string) string {
	rig.t.Helper()
	var runID string
	if err := rig.db.QueryRow(
		`SELECT run_id FROM work_attempts WHERE work_id = ? ORDER BY attempt DESC LIMIT 1`,
		workID).Scan(&runID); err != nil {
		rig.t.Fatalf("read the attempt for %s: %v", workID, err)
	}
	return runID
}

func (rig *verticalRig) count(query string, args ...any) int {
	rig.t.Helper()
	var n int
	if err := rig.db.QueryRow(query, args...).Scan(&n); err != nil {
		rig.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// runRecords counts the run records for one run id. Post unified-journal there
// is no agent_runs table: a run IS its run.started journal entry, traced by the
// run id, which is the same identity the attempt and the runtime carry.
func (rig *verticalRig) runRecords(runID string) int {
	return rig.count(
		`SELECT COUNT(*) FROM journal_entries WHERE trace_id = ? AND entry_type = 'run.started'`, runID)
}

func (rig *verticalRig) allRunRecords() int {
	return rig.count(`SELECT COUNT(*) FROM journal_entries WHERE entry_type = 'run.started'`)
}

func sanitizeForID(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

const verticalBody = `{"event":"deploy","source":"github","data":{"ref":"main"}}`

// ---------------------------------------------------------------------------
// 3. The whole path: receipt -> claim -> attempt -> runtime -> terminal.
// ---------------------------------------------------------------------------

func TestVerticalServer_OneDeliveryTravelsTheWholePath(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	rig.startDispatcher()

	// Receipt. Written after the commit, never before it.
	rec := rig.deliver(verticalBody)
	if rec.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (receipt: %+v)", rec.Status, rec)
	}
	if rec.WorkID == "" || rec.DeliveryID == "" {
		t.Fatalf("receipt names no work: %+v", rec)
	}
	if rec.Duplicate {
		t.Errorf("a first delivery came back as a duplicate: %+v", rec)
	}

	// The ledger holds the delivery and the work it produced, in the same
	// transaction, before the sender was told anything.
	if n := rig.count(`SELECT COUNT(*) FROM webhook_deliveries`); n != 1 {
		t.Errorf("%d deliveries recorded, want 1", n)
	}
	if n := rig.count(`SELECT COUNT(*) FROM work_items`); n != 1 {
		t.Errorf("%d work items, want 1", n)
	}

	// Claim -> attempt -> runtime. The work becomes `running` only once the
	// runtime is CONFIRMED, and this run is silent — it emits no stream event —
	// so the confirmation can only have come from the provider probe.
	it := rig.waitForState(rec.WorkID, work.StateRunning)
	runID := rig.attemptRunID(rec.WorkID)

	var phase, locator string
	if err := rig.db.QueryRow(
		`SELECT runtime_phase, COALESCE(runtime_locator,'') FROM work_attempts WHERE run_id = ?`,
		runID).Scan(&phase, &locator); err != nil {
		t.Fatalf("read the attempt: %v", err)
	}
	if phase != "confirmed" {
		t.Errorf("runtime phase = %q, want confirmed — a silent process is still a running one", phase)
	}
	if locator != "agent-run:"+runID {
		t.Errorf("locator = %q, want agent-run:%s", locator, runID)
	}

	// The three identities are one chain: the work the receipt named, the
	// attempt the claim opened, and the process that is actually running.
	if got := rig.proc.runsStarted(); len(got) != 1 || got[0] != runID {
		t.Errorf("runtimes started = %v, want exactly [%s]", got, runID)
	}
	if it.Generation < 1 {
		t.Errorf("generation = %d, want at least 1 — the fencing term was never bumped", it.Generation)
	}

	// And the run record the dispatcher wrote over real IPC carries the same id.
	// The record is the run.started journal entry, traced by the run id.
	if n := rig.runRecords(runID); n != 1 {
		t.Errorf("%d run records for run %s, want 1 — the run record and the attempt "+
			"must share one identity", n, runID)
	}

	close(rig.proc.hold)
	rig.waitForState(rec.WorkID, work.StateSucceeded)
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Errorf("%d runtimes over the life of one delivery, want 1", len(got))
	}
}

// The hint is an optimisation; the poll is the guarantee. If losing a wake-up
// could strand work, then a crash between the acceptance commit and the nudge
// would strand it — which is exactly the window the durable ledger exists to
// survive.
func TestVerticalServer_ALostHintIsReplacedByPolling(t *testing.T) {
	rig := newVerticalRig(t)
	rig.startDispatcher()
	// Sever the hint AFTER the dispatcher wired it, which is what a lost nudge
	// looks like from the ledger's side.
	rig.router.webhookHandler.dispatchHint = nil

	rec := rig.deliver(verticalBody)
	if rec.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Status)
	}
	rig.waitForState(rec.WorkID, work.StateSucceeded)
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Errorf("%d runtimes, want 1", len(got))
	}
}

// A re-delivery is one piece of work and one execution.
func TestVerticalServer_ADuplicateDeliveryDoesNotCreateASecondRun(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	rig.startDispatcher()

	first := rig.deliver(verticalBody)
	rig.waitForState(first.WorkID, work.StateRunning)

	// Identical bytes, identical signature: the same delivery, arriving twice.
	second := rig.deliver(verticalBody)
	if !second.Duplicate {
		t.Errorf("the re-delivery was not reported as a duplicate: %+v", second)
	}
	if second.WorkID != first.WorkID {
		t.Errorf("re-delivery named work %s, want the original %s", second.WorkID, first.WorkID)
	}
	if second.State != string(work.StateRunning) {
		t.Errorf("re-delivery answered %q; a duplicate must answer with the work's CURRENT state, "+
			"not repeat \"queued\" forever", second.State)
	}

	close(rig.proc.hold)
	rig.waitForState(first.WorkID, work.StateSucceeded)

	if n := rig.count(`SELECT COUNT(*) FROM work_items`); n != 1 {
		t.Errorf("%d work items for a delivery and its duplicate, want 1", n)
	}
	if n := rig.count(`SELECT COUNT(*) FROM work_attempts`); n != 1 {
		t.Errorf("%d attempts, want 1", n)
	}
	if got := rig.proc.runsStarted(); len(got) != 1 {
		t.Errorf("%d runtimes for a delivery and its duplicate, want 1", len(got))
	}
	if n := rig.runRecords(rig.attemptRunID(first.WorkID)); n != 1 {
		t.Errorf("%d run records, want 1", n)
	}
}

// This dispatcher executes webhook work and nothing else.
//
// Claiming another producer's work would not merely fail it, it would CONSUME
// it: the attempt is burned, the item moves out of `queued`, and the producer
// that could have run it never sees it again.
func TestVerticalServer_TheDispatcherDoesNotClaimAnotherProducersWork(t *testing.T) {
	rig := newVerticalRig(t)
	rig.startDispatcher()

	ctx := context.Background()
	tx, err := rig.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rig.store.AcceptTx(ctx, tx, work.AcceptRequest{
		WorkspaceID: rig.wsID,
		Source:      work.SourceAssignment,
		Class:       work.ClassBackground,
		AgentID:     rig.agentID,
		InputJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("accept assignment work: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Long enough for several poll ticks (PollInterval is 2s in production).
	time.Sleep(5 * time.Second)

	it, err := rig.store.Get(ctx, other.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != work.StateQueued {
		t.Fatalf("assignment work is %q — the webhook dispatcher took work it cannot execute", it.State)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Errorf("%d runtimes created for another producer's work, want 0", len(got))
	}
}

// A dispatcher without an authorizer would run work on permissions nobody
// rechecked. It must not be constructible at all.
func TestVerticalServer_NoRouteMeansNoDispatcher(t *testing.T) {
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", quietLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if _, err := r.StartWebhookDispatcher(context.Background(), quietLogger()); !errors.Is(err, ErrNoWebhookRoute) {
		t.Fatalf("err = %v, want ErrNoWebhookRoute", err)
	}
}

// ---------------------------------------------------------------------------
// 5. Cancel and shutdown, through the same assembly.
// ---------------------------------------------------------------------------

// Nothing was created, so the stop is confirmed rather than requested — and the
// dispatcher that starts afterwards must not execute it.
func TestVerticalServer_CancelBeforeAnyStartPreventsExecution(t *testing.T) {
	rig := newVerticalRig(t) // deliberately no dispatcher yet

	rec := rig.deliver(verticalBody)
	if rec.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Status)
	}

	out, err := rig.store.RequestCancel(context.Background(), rec.WorkID, "operator", "changed my mind")
	if err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}
	if out.Outcome != work.CancelOutcomeCancelled {
		t.Fatalf("cancelling queued work = %q, want %q — nothing had started, so the stop is a fact",
			out.Outcome, work.CancelOutcomeCancelled)
	}

	rig.startDispatcher()
	time.Sleep(5 * time.Second)

	it, err := rig.store.Get(context.Background(), rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != work.StateCancelled {
		t.Errorf("state = %q, want cancelled", it.State)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Errorf("%d runtimes created for cancelled work, want 0", len(got))
	}
	if n := rig.allRunRecords(); n != 0 {
		t.Errorf("%d run records for work that was cancelled before it started, want 0", n)
	}
}

// Cancel during a run stops THIS run's runtime, named by its own locator.
func TestVerticalServer_CancelDuringARunStopsThatRuntime(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	rig.waitForState(rec.WorkID, work.StateRunning)
	runID := rig.attemptRunID(rec.WorkID)

	out, err := rig.store.RequestCancel(context.Background(), rec.WorkID, "operator", "stop it")
	if err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}
	if out.Outcome != work.CancelOutcomeRequested {
		t.Fatalf("cancelling a live run = %q, want %q — a running process can only be ASKED",
			out.Outcome, work.CancelOutcomeRequested)
	}

	rig.waitForState(rec.WorkID, work.StateCancelled)
	if !rig.proc.wasStopped(runID) {
		t.Errorf("run %s was never stopped; some other runtime was signalled", runID)
	}
	close(rig.proc.hold)
}

// A runtime that will not stop is not a cancelled one.
//
// This test waits out the production grace period on purpose: the point is that
// the shipped StopGrace is wired to the shipped escalation, not that some test
// value is.
func TestVerticalServer_AStopThatDoesNotTakeIsNotCalledCancelled(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	rig.proc.refuseStop = true
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	rig.waitForState(rec.WorkID, work.StateRunning)

	if _, err := rig.store.RequestCancel(context.Background(), rec.WorkID, "operator", "stop it"); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}

	it := rig.waitForState(rec.WorkID, work.StateNeedsReconciliation)
	if it.State == work.StateCancelled {
		t.Fatal("the work was called cancelled while its runtime was still alive")
	}
	if it.StateReason == "" {
		t.Error("parked with no reason; an operator has nothing to act on")
	}
	if !strings.Contains(it.StateReason, "agent-run:") {
		t.Errorf("reason = %q; it must name the runtime that would not stop", it.StateReason)
	}
	close(rig.proc.hold)
}

// After shutdown nothing is supervising outside the recorded lifecycle: every
// live attempt is either confirmed stopped or visibly parked, and the stop
// function does not return until that is true.
func TestVerticalServer_ShutdownLeavesNoSupervisorOutsideTheLedger(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	rig.waitForState(rec.WorkID, work.StateRunning)
	runID := rig.attemptRunID(rec.WorkID)

	rig.stopDispatcher()
	rig.stopDispatcher = nil

	// The assertion is made with no sleep and no polling: by the time stop
	// returns, the work must already be out of every state that says "a process
	// is running for this". A dispatcher that returned before draining would
	// leave the ledger claiming a runtime nobody owns.
	it, err := rig.store.Get(context.Background(), rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	switch it.State {
	case work.StateCancelled:
		if !rig.proc.wasStopped(runID) {
			t.Errorf("the work is cancelled but run %s was never stopped", runID)
		}
	case work.StateNeedsReconciliation:
		// Also truthful: the runtime could not be confirmed stopped.
		if it.StateReason == "" {
			t.Error("parked at shutdown with no reason")
		}
	default:
		t.Errorf("after shutdown the work is %q; it must be settled or visibly parked, "+
			"never left claiming a runtime nobody is supervising", it.State)
	}

	if n := rig.count(`SELECT COUNT(*) FROM work_items WHERE state IN ('starting','running')`); n != 0 {
		t.Errorf("%d work items still claim a live runtime after shutdown, want 0", n)
	}
	close(rig.proc.hold)
}

// Permission removed while the work waited: refused at dispatch, which is the
// only place it can be noticed. Acceptance was right to say 202 — the agent was
// still fine when the delivery arrived.
func TestVerticalServer_PermissionRemovedWhileWaitingPreventsTheStart(t *testing.T) {
	rig := newVerticalRig(t) // no dispatcher: the work has to WAIT

	rec := rig.deliver(verticalBody)
	if rec.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 — the agent existed when the delivery arrived", rec.Status)
	}

	if _, err := rig.db.Exec(
		`UPDATE agents SET deleted_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), rig.agentID,
	); err != nil {
		t.Fatalf("delete the agent: %v", err)
	}

	rig.startDispatcher()
	it := rig.waitForState(rec.WorkID, work.StateFailed)
	if !strings.Contains(it.StateReason, "refused at dispatch") {
		t.Errorf("reason = %q, want it to say the dispatch refused it", it.StateReason)
	}
	if got := rig.proc.runsStarted(); len(got) != 0 {
		t.Errorf("%d runtimes created for an agent that no longer exists, want 0", len(got))
	}
	if n := rig.allRunRecords(); n != 0 {
		t.Errorf("%d run records, want 0", n)
	}
}

// The two halves of the contract have to agree about the bytes between them.
//
// Acceptance writes work_items.input_json in one process; the dispatcher's
// runtime adapter reads it back in another, possibly after a restart. They were
// written by different code and there is no compiler check that they match —
// the first version of this pair did not, and the symptom was a dispatcher that
// resolved an empty agent id.
func TestVerticalServer_AcceptanceWritesTheInputTheRuntimeReads(t *testing.T) {
	rig := newVerticalRig(t)
	rec := rig.deliver(verticalBody)

	var inputJSON string
	if err := rig.db.QueryRow(`SELECT input_json FROM work_items WHERE id = ?`, rec.WorkID).
		Scan(&inputJSON); err != nil {
		t.Fatalf("read input_json: %v", err)
	}

	var in webhookRunInput
	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
		t.Fatalf("the dispatcher cannot read what acceptance wrote: %v (%s)", err, inputJSON)
	}
	if in.AgentID != rig.agentID {
		t.Errorf("agent_id = %q, want %q", in.AgentID, rig.agentID)
	}
	if in.CrewID != rig.crewID {
		t.Errorf("crew_id = %q, want %q", in.CrewID, rig.crewID)
	}
	if in.Payload.Event != "deploy" {
		t.Errorf("payload event = %q, want \"deploy\" — the delivery's own content must survive "+
			"into the work item, because retention may drop the delivery's raw body", in.Payload.Event)
	}
}

// The timeline of one delivery, printed from the ledger.
//
// The handoff quotes this output. It is generated rather than narrated so that
// the document cannot drift from the system: if the identities stop lining up,
// or a state disappears from the history, the thing in the document changes
// too. Run it with -v to see it.
func TestVerticalServer_TheTimelineOfOneDelivery(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	rig.startDispatcher()

	rec := rig.deliver(verticalBody)
	rig.waitForState(rec.WorkID, work.StateRunning)
	runID := rig.attemptRunID(rec.WorkID)
	close(rig.proc.hold)
	rig.waitForState(rec.WorkID, work.StateSucceeded)

	var (
		deliveryID, endpointID, bodySHA, filterDecision string
		workID, sessionID, source, class                string
		phase, locator                                  string
		attempt, generation                             int
	)
	if err := rig.db.QueryRow(`
		SELECT id, endpoint_id, body_sha256, filter_decision, COALESCE(work_id,'')
		FROM webhook_deliveries`).
		Scan(&deliveryID, &endpointID, &bodySHA, &filterDecision, &workID); err != nil {
		t.Fatalf("read the delivery: %v", err)
	}
	if err := rig.db.QueryRow(
		`SELECT session_id, source, class FROM work_items WHERE id = ?`, rec.WorkID).
		Scan(&sessionID, &source, &class); err != nil {
		t.Fatalf("read the work item: %v", err)
	}
	if err := rig.db.QueryRow(
		`SELECT attempt, generation, runtime_phase, COALESCE(runtime_locator,'') FROM work_attempts WHERE run_id = ?`, runID).
		Scan(&attempt, &generation, &phase, &locator); err != nil {
		t.Fatalf("read the attempt: %v", err)
	}

	t.Logf("identities")
	t.Logf("  delivery_id  %s   (workspace, endpoint %s, source delivery id) — the sender's identity", deliveryID, endpointID)
	t.Logf("  work_id      %s   — stable across every retry; what the receipt named", rec.WorkID)
	t.Logf("  run_id       %s   — ONE attempt; the same value in work_attempts, the run record, the locator", runID)
	t.Logf("  session_id   %s   — one active turn", sessionID)
	t.Logf("  generation   %d   — the fencing term; every write must present it", generation)
	t.Logf("  locator      %s   — written BEFORE the runtime existed", locator)
	t.Logf("  attempt      %d of %d", attempt, work.MaxAttempts)
	t.Logf("  source/class %s / %s   filter: %s", source, class, filterDecision)
	t.Logf("  body sha256  %s", bodySHA)
	if workID != rec.WorkID {
		t.Errorf("the delivery row names work %q but the receipt named %q", workID, rec.WorkID)
	}

	events, err := rig.store.History(context.Background(), rec.WorkID)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	t.Logf("states")
	var seen []string
	for _, e := range events {
		from := string(e.FromState)
		if from == "" {
			from = "-"
		}
		run := e.RunID
		if run == "" {
			run = "-"
		}
		t.Logf("  %-22s %-10s -> %-10s gen=%d run=%s  %s",
			e.At.UTC().Format("15:04:05.000"), from, e.ToState, e.Generation, run, e.Reason)
		seen = append(seen, string(e.ToState))
	}

	// The path a healthy delivery takes, in order. Asserted rather than merely
	// printed: a timeline in a document is only worth something if something
	// breaks when it stops being true.
	want := []string{"queued", "starting", "running", "succeeded"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("the timeline is %v, want %v", seen, want)
	}
	if n := rig.runRecords(runID); n != 1 {
		t.Errorf("%d run records traced by %s, want 1", n, runID)
	}
}
