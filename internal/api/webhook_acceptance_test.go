package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/work"
)

// The agent webhook surface on the durable acceptance path.
//
// Every case here goes through the real HTTP handler against a real migrated
// SQLite database, because the thing under test is an ORDER — read the body
// under a limit, verify, filter, commit, answer — and a test that calls the
// pieces directly cannot observe an order.

// acceptRig is one wired agent webhook endpoint: a migrated database with a
// crew + agent row carrying a webhook secret, a resolver that answers for that
// agent, and a container provider that counts how often something tried to
// start a container.
type acceptRig struct {
	h         *WebhookHandler
	db        *sql.DB
	container *countingWebhookContainer
	resolver  *lockingRecordingResolver
	crewID    string
	agentID   string
	secret    string
}

// lockingRecordingResolver is runIDRecordingResolver with a lock around the
// fields it records into.
//
// The concurrency case drives two dozen deliveries at one handler, and the
// shared fake writes resolveCalledWithAgentID and appends to createdRunIDs
// without synchronisation — a data race in the TEST that -race would report as
// though the handler had one. Locking here keeps the race detector pointed at
// the code under test.
type lockingRecordingResolver struct {
	mu sync.Mutex
	runIDRecordingResolver
}

func (r *lockingRecordingResolver) ResolveAgent(ctx context.Context, agentID, workspaceID string) (*chatbridge.ChatInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runIDRecordingResolver.ResolveAgent(ctx, agentID, workspaceID)
}

func (r *lockingRecordingResolver) CreateRun(ctx context.Context, runID, agentID, chatID, wsID, trigger string, meta map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runIDRecordingResolver.CreateRun(ctx, runID, agentID, chatID, wsID, trigger, meta)
}

// runsCreated is the recorded count, read under the same lock.
func (r *lockingRecordingResolver) runsCreated() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.createdRunIDs)
}

// countingWebhookContainer counts EnsureCrewRuntime calls and refuses them.
//
// Refusing is what makes the "nothing before the response" assertion clean: the
// dispatch stops at the container start, so no run record is written and no
// orchestrator is touched, and the only thing the test has to watch is the
// counter.
type countingWebhookContainer struct {
	secWebhook2Container
	starts atomic.Int32
}

func (c *countingWebhookContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	c.starts.Add(1)
	return "", fmt.Errorf("container start refused by the test")
}

func newAcceptRig(t *testing.T, name string) *acceptRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID, agentID := "crew-"+name, "agent-"+name
	const secret = "acceptance-secret"
	seedWebhookSecretAgent(t, db, wsID, crewID, agentID, secret)

	resolver := &lockingRecordingResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: agentID, AgentSlug: "s-" + agentID,
		CrewID: crewID, CrewSlug: "c-" + crewID, WorkspaceID: wsID,
	}
	container := &countingWebhookContainer{}
	h := NewWebhookHandler(db, newTestLogger(), resolver, nil, nil, container, nil)
	// Pin the ingress gates well clear of every case here: the gates are not
	// what these tests are about, and they are process-global, so a neighbouring
	// test's burst must not be able to turn a 202 into a 429.
	h.agentRatePerMin = 10000
	t.Cleanup(func() { _ = h.Close() })

	return &acceptRig{h: h, db: db, container: container, resolver: resolver,
		crewID: crewID, agentID: agentID, secret: secret}
}

// signedRequest builds a delivery signed with the body-only HMAC shape — the
// legacy agent contract, unchanged.
func (rig *acceptRig) signedRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/webhooks/"+rig.crewID+"/"+rig.agentID+"/trigger", strings.NewReader(body))
	req.SetPathValue("crewId", rig.crewID)
	req.SetPathValue("agentId", rig.agentID)
	req.Header.Set("X-Signature", webhook.ComputeHMAC([]byte(body), rig.secret))
	return req
}

// keyedRequest authenticates with the deprecated plaintext secret and carries
// an explicit Idempotency-Key, so the delivery identity is the key rather than
// the signature. That is the only way to present the SAME source delivery id
// with a DIFFERENT body, which is what §5's 409 row is about.
func (rig *acceptRig) keyedRequest(body, key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/webhooks/"+rig.crewID+"/"+rig.agentID+"/trigger", strings.NewReader(body))
	req.SetPathValue("crewId", rig.crewID)
	req.SetPathValue("agentId", rig.agentID)
	req.Header.Set("X-Webhook-Secret", rig.secret)
	req.Header.Set("Idempotency-Key", key)
	return req
}

func (rig *acceptRig) serve(req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	rig.h.ServeHTTP(rr, req)
	return rr
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	return out
}

func countWebhookRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestAgentWebhookAcceptance_NewDeliveryIsRecordedBeforeThe202 is §5's first
// row: a new accepted delivery answers 202 with the delivery and work ids, and
// the rows exist by the time the sender is told about them.
func TestAgentWebhookAcceptance_NewDeliveryIsRecordedBeforeThe202(t *testing.T) {
	rig := newAcceptRig(t, "newdel")

	rr := rig.serve(rig.signedRequest(`{"event":"deploy","source":"gh"}`))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	got := decodeBody(t, rr)
	deliveryID, _ := got["delivery_id"].(string)
	workID, _ := got["work_id"].(string)
	if deliveryID == "" || workID == "" {
		t.Fatalf("202 must carry both ids, got %v", got)
	}
	if got["status"] != "queued" {
		t.Errorf("status = %v, want %q", got["status"], "queued")
	}
	if got["duplicate"] != false {
		t.Errorf("duplicate = %v, want false", got["duplicate"])
	}

	if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM webhook_deliveries WHERE id = ?`, deliveryID); n != 1 {
		t.Errorf("delivery rows = %d, want 1 — the 202 named a delivery that is not in the ledger", n)
	}
	if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM work_items WHERE id = ? AND state = 'queued'`, workID); n != 1 {
		t.Errorf("queued work rows = %d, want 1", n)
	}
	// The delivery must point at the work, in the same row: that link is what
	// makes "accepted = queued + active + terminal" a balance rather than a hope.
	var linked sql.NullString
	if err := rig.db.QueryRow(`SELECT work_id FROM webhook_deliveries WHERE id = ?`, deliveryID).Scan(&linked); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if linked.String != workID {
		t.Errorf("delivery.work_id = %q, want %q", linked.String, workID)
	}
}

// TestAgentWebhookAcceptance_NothingRunsBeforeTheResponse is W2.
//
// The old handler called crewstart(...).Start — an image pull, on a cold
// crew — from inside the acceptance path, so a provider with a ten-second
// response deadline was waiting on Docker. The assertion is made at the moment
// WriteHeader is called, not afterwards: a check after ServeHTTP returns would
// pass just as happily for code that started the container first and answered
// second.
func TestAgentWebhookAcceptance_NothingRunsBeforeTheResponse(t *testing.T) {
	rig := newAcceptRig(t, "nothingbefore")

	probe := &statusProbeRecorder{ResponseRecorder: httptest.NewRecorder()}
	probe.at = func() int32 { return rig.container.starts.Load() }

	rig.h.ServeHTTP(probe, rig.signedRequest(`{"event":"deploy","source":"gh"}`))

	if probe.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", probe.Code, probe.Body.String())
	}
	if probe.startsAtHeader != 0 {
		t.Errorf("%d container start(s) had already happened when the 202 was written; "+
			"the acceptance path must do nothing outside the database before it answers",
			probe.startsAtHeader)
	}
	if n := rig.resolver.runsCreated(); n != 0 {
		t.Errorf("%d run record(s) written before the response, want 0", n)
	}
	// And nothing runs AFTERWARDS either, which is the stronger claim this
	// path can now make. Acceptance commits the delivery and the work and then
	// hints; the dispatcher is the only thing that starts a runtime, and there
	// is none running in this test. Before R3 this assertion read "want 1",
	// because the handler dispatched directly — which is exactly the second
	// owner that made `queued` and "an agent is running" the same state.
	if got := rig.container.starts.Load(); got != 0 {
		t.Errorf("container starts after the response = %d, want 0 — acceptance started a runtime "+
			"without claiming the work", got)
	}
	if n := rig.resolver.runsCreated(); n != 0 {
		t.Errorf("%d run record(s) written by acceptance, want 0", n)
	}
}

// statusProbeRecorder samples a counter at the instant the status line is
// written, so an ordering claim is checked at the point the order matters.
type statusProbeRecorder struct {
	*httptest.ResponseRecorder
	at             func() int32
	startsAtHeader int32
	sampled        bool
}

func (p *statusProbeRecorder) WriteHeader(code int) {
	if !p.sampled {
		p.startsAtHeader, p.sampled = p.at(), true
	}
	p.ResponseRecorder.WriteHeader(code)
}

func (p *statusProbeRecorder) Write(b []byte) (int, error) {
	if !p.sampled {
		p.startsAtHeader, p.sampled = p.at(), true
	}
	return p.ResponseRecorder.Write(b)
}

// TestAgentWebhookAcceptance_ResponseTable walks the rows of §5 that a single
// delivery can reach on its own.
func TestAgentWebhookAcceptance_ResponseTable(t *testing.T) {
	oversized := strings.Repeat("x", webhook.MaxBodyBytes+1024)

	tests := []struct {
		name       string
		build      func(rig *acceptRig) *http.Request
		wantStatus int
		wantBody   map[string]any
		wantWork   int
	}{
		{
			name:       "valid ping is ignored and starts no agent",
			build:      func(rig *acceptRig) *http.Request { return rig.signedRequest(`{"event":"ping"}`) },
			wantStatus: http.StatusOK,
			wantBody:   map[string]any{"status": "ignored", "reason": "ping"},
			wantWork:   0,
		},
		{
			name: "bad signature is 401 and records nothing",
			build: func(rig *acceptRig) *http.Request {
				req := rig.signedRequest(`{"event":"deploy"}`)
				req.Header.Set("X-Signature", strings.Repeat("ab", 32))
				return req
			},
			wantStatus: http.StatusUnauthorized,
			wantWork:   0,
		},
		{
			name: "oversized body is 413, not a truncated 401",
			build: func(rig *acceptRig) *http.Request {
				// Signed over the FULL body, so if the reader truncated
				// silently the signature would fail and the answer would be
				// 401 — which is exactly what it used to be.
				return rig.signedRequest(oversized)
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantWork:   0,
		},
		{
			name: "unknown endpoint is 404",
			build: func(rig *acceptRig) *http.Request {
				req := rig.signedRequest(`{"event":"deploy"}`)
				req.SetPathValue("agentId", "agent-does-not-exist")
				return req
			},
			wantStatus: http.StatusNotFound,
			wantWork:   0,
		},
		{
			name: "malformed JSON is 400",
			build: func(rig *acceptRig) *http.Request {
				return rig.signedRequest(`{"event":`)
			},
			wantStatus: http.StatusBadRequest,
			wantWork:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAcceptRig(t, strings.ReplaceAll(tc.name, " ", "-"))
			rr := rig.serve(tc.build(rig))

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			for k, want := range tc.wantBody {
				got := decodeBody(t, rr)
				if got[k] != want {
					t.Errorf("body[%q] = %v, want %v (full: %v)", k, got[k], want, got)
				}
			}
			if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM work_items`); n != tc.wantWork {
				t.Errorf("work items = %d, want %d", n, tc.wantWork)
			}
			if got := rig.container.starts.Load(); got != 0 {
				t.Errorf("%d container start(s) on a refused/ignored delivery, want 0", got)
			}
			// Nothing here may reveal what the expected signature was.
			if strings.Contains(rr.Body.String(), webhook.ComputeHMAC([]byte(`{"event":"deploy"}`), rig.secret)) {
				t.Error("response body leaked the expected signature")
			}
		})
	}
}

// TestAgentWebhookAcceptance_IgnoredDeliveryIsStillAudited: §5 wants the filter
// decision recoverable, so a ping is a ledger row with a reason and no work —
// not a request that vanished.
func TestAgentWebhookAcceptance_IgnoredDeliveryIsStillAudited(t *testing.T) {
	rig := newAcceptRig(t, "pingaudit")

	rr := rig.serve(rig.signedRequest(`{"event":"ping"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var decision, reason string
	var workID sql.NullString
	err := rig.db.QueryRow(
		`SELECT filter_decision, filter_reason, work_id FROM webhook_deliveries`,
	).Scan(&decision, &reason, &workID)
	if err != nil {
		t.Fatalf("read the ignored delivery: %v", err)
	}
	if decision != "ignored" || reason != "ping" {
		t.Errorf("filter = (%q, %q), want (ignored, ping)", decision, reason)
	}
	if workID.Valid && workID.String != "" {
		t.Errorf("an ignored delivery produced work %q", workID.String)
	}
}

// TestAgentWebhookAcceptance_Duplicate is §5's second row: a re-delivery in the
// retention window gets the ORIGINAL ids back and creates no second attempt.
func TestAgentWebhookAcceptance_Duplicate(t *testing.T) {
	rig := newAcceptRig(t, "dup")
	const body = `{"event":"deploy","source":"gh"}`

	first := decodeBody(t, rig.serve(rig.signedRequest(body)))
	second := decodeBody(t, rig.serve(rig.signedRequest(body)))

	if second["duplicate"] != true {
		t.Errorf("duplicate = %v, want true (got %v)", second["duplicate"], second)
	}
	if second["delivery_id"] != first["delivery_id"] || second["work_id"] != first["work_id"] {
		t.Errorf("re-delivery answered with new ids %v, want the original %v", second, first)
	}
	if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM work_items`); n != 1 {
		t.Errorf("work items = %d, want 1 — a re-delivery must not create a second piece of work", n)
	}
	if got := rig.container.starts.Load(); got != 0 {
		t.Errorf("container starts = %d, want 0 — acceptance must not start a runtime at all, "+
			"for the first delivery or its duplicate", got)
	}
}

// TestAgentWebhookAcceptance_SameIDDifferentBodyIsConflict is §5's 409 row. The
// original record is kept: something changed underneath an identifier that was
// supposed to be stable, and guessing which of the two is right is worse than
// saying so.
func TestAgentWebhookAcceptance_SameIDDifferentBodyIsConflict(t *testing.T) {
	rig := newAcceptRig(t, "conflict")

	firstRR := rig.serve(rig.keyedRequest(`{"event":"deploy","source":"a"}`, "evt-77"))
	if firstRR.Code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want 202; body=%s", firstRR.Code, firstRR.Body.String())
	}
	first := decodeBody(t, firstRR)

	second := rig.serve(rig.keyedRequest(`{"event":"deploy","source":"DIFFERENT"}`, "evt-77"))
	if second.Code != http.StatusConflict {
		t.Fatalf("same id + different body status = %d, want 409; body=%s", second.Code, second.Body.String())
	}
	if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM webhook_deliveries`); n != 1 {
		t.Errorf("delivery rows = %d, want 1 — the original record must be kept", n)
	}
	var storedBody []byte
	if err := rig.db.QueryRow(`SELECT raw_body FROM webhook_deliveries WHERE id = ?`,
		first["delivery_id"]).Scan(&storedBody); err != nil {
		t.Fatalf("read the original: %v", err)
	}
	if !strings.Contains(string(storedBody), `"source":"a"`) {
		t.Errorf("stored body = %q, want the ORIGINAL body", storedBody)
	}
}

// TestAgentWebhookAcceptance_ConcurrentDuplicatesProduceOneWork is the race the
// response table has to survive: a hundred copies of one delivery, all in
// flight, answering with one stable receipt and creating one piece of work.
func TestAgentWebhookAcceptance_ConcurrentDuplicatesProduceOneWork(t *testing.T) {
	rig := newAcceptRig(t, "concurrentdup")
	const body = `{"event":"deploy","source":"gh"}`
	const copies = 24

	type answer struct {
		code       int
		deliveryID string
		workID     string
	}
	answers := make([]answer, copies)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rr := rig.serve(rig.signedRequest(body))
			a := answer{code: rr.Code}
			var got map[string]any
			if json.Unmarshal(rr.Body.Bytes(), &got) == nil {
				a.deliveryID, _ = got["delivery_id"].(string)
				a.workID, _ = got["work_id"].(string)
			}
			answers[i] = a
		}(i)
	}
	close(start)
	wg.Wait()

	if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM work_items`); n != 1 {
		t.Fatalf("work items = %d, want exactly 1 from %d copies of one delivery", n, copies)
	}
	if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM webhook_deliveries`); n != 1 {
		t.Fatalf("delivery rows = %d, want exactly 1", n)
	}

	var wantDelivery, wantWork string
	for i, a := range answers {
		if a.code != http.StatusAccepted {
			t.Fatalf("copy %d answered %d, want 202 for every copy of an accepted delivery", i, a.code)
		}
		if wantDelivery == "" {
			wantDelivery, wantWork = a.deliveryID, a.workID
			continue
		}
		if a.deliveryID != wantDelivery || a.workID != wantWork {
			t.Fatalf("copy %d answered (%s, %s), want the one stable receipt (%s, %s)",
				i, a.deliveryID, a.workID, wantDelivery, wantWork)
		}
	}
	if got := rig.container.starts.Load(); got != 0 {
		t.Errorf("container starts = %d, want 0 — acceptance starts nothing; the one stable receipt above "+
			"is what concurrency has to produce", got)
	}
}

// TestAgentWebhookAcceptance_BudgetFailureIsNeverA202 is §5's last row. A
// database that cannot commit answers 503, and no agent starts — the failure
// mode the old code got wrong in the other direction, by logging "dispatching
// anyway" and running without a record.
func TestAgentWebhookAcceptance_BudgetFailureIsNeverA202(t *testing.T) {
	rig := newAcceptRig(t, "budget")
	// Force the acceptance transaction to fail by taking away the table it
	// writes to. Nothing subtler is needed: the assertion is about what the
	// handler does when the commit does not happen, not about which fault
	// prevented it.
	if _, err := rig.db.Exec(`ALTER TABLE work_items RENAME TO work_items_hidden`); err != nil {
		t.Fatalf("hide work_items: %v", err)
	}

	rr := rig.serve(rig.signedRequest(`{"event":"deploy","source":"gh"}`))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != webhook.IngressRetryAfterSeconds {
		t.Errorf("Retry-After = %q, want %q", got, webhook.IngressRetryAfterSeconds)
	}
	if got := rig.container.starts.Load(); got != 0 {
		t.Errorf("%d container start(s) after a failed acceptance, want 0 — "+
			"recording the acceptance is a precondition for running", got)
	}
	if n := rig.resolver.runsCreated(); n != 0 {
		t.Errorf("%d run record(s) after a failed acceptance, want 0", n)
	}
}

// TestAgentWebhookAcceptance_IngressFullIsA429WithRetryAfter: the per-agent
// gates are a refusal, not a fault. They used to come back as a bare 500,
// because the handler mapped every trigger error to one status.
func TestAgentWebhookAcceptance_IngressFullIsA429WithRetryAfter(t *testing.T) {
	rig := newAcceptRig(t, "ingressfull")
	rig.h.agentRatePerMin = 1

	if rr := rig.serve(rig.signedRequest(`{"event":"a"}`)); rr.Code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	rr := rig.serve(rig.signedRequest(`{"event":"b"}`))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled delivery status = %d, want 429; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want %q", got, "5")
	}
	if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM work_items`); n != 1 {
		t.Errorf("work items = %d, want 1 — a 429 must take on nothing new", n)
	}
}

// TestAgentWebhookAcceptance_ThrottledDuplicateStillGetsItsReceipt: §5 says a
// duplicate of already-accepted work is not refused because the ingress is
// full. It has its answer already; making it retry to hear it again is the
// wrong side of the trade.
func TestAgentWebhookAcceptance_ThrottledDuplicateStillGetsItsReceipt(t *testing.T) {
	rig := newAcceptRig(t, "throttleddup")
	const body = `{"event":"deploy","source":"gh"}`

	first := decodeBody(t, rig.serve(rig.signedRequest(body)))
	rig.h.agentRatePerMin = 1 // the window is already spent by the first delivery

	rr := rig.serve(rig.signedRequest(body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("throttled DUPLICATE status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	got := decodeBody(t, rr)
	if got["duplicate"] != true || got["work_id"] != first["work_id"] {
		t.Errorf("throttled duplicate answered %v, want the original receipt %v", got, first)
	}
}

func TestAgentWebhookAcceptance_ThrottleStillChecksPayloadAndLookupFailure(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "conflicting body"
		want := http.StatusConflict
		if unavailable {
			name = "lookup unavailable"
			want = http.StatusServiceUnavailable
		}
		t.Run(name, func(t *testing.T) {
			rig := newAcceptRig(t, "throttle-review-"+strings.ReplaceAll(name, " ", "-"))
			rig.h.agentRatePerMin = 1
			first := rig.serve(rig.keyedRequest(`{"event":"deploy","source":"a"}`, "same-id"))
			if first.Code != http.StatusAccepted {
				t.Fatalf("first HTTP %d: %s", first.Code, first.Body.String())
			}
			if unavailable {
				if _, err := rig.db.Exec("DROP TABLE webhook_deliveries"); err != nil {
					t.Fatal(err)
				}
			}
			rr := rig.serve(rig.keyedRequest(`{"event":"deploy","source":"different"}`, "same-id"))
			if rr.Code != want {
				t.Fatalf("throttled HTTP %d, want %d: %s", rr.Code, want, rr.Body.String())
			}
		})
	}
}

func TestAgentWebhookAcceptance_DurableIngressCapacityKeepsExistingReceipts(t *testing.T) {
	for _, byBytes := range []bool{false, true} {
		t.Run(fmt.Sprint("bytes-", byBytes), func(t *testing.T) {
			rig := newAcceptRig(t, fmt.Sprint("capacity-", byBytes))
			_, store := rig.h.acceptance()
			const body = `{"event":"a"}`
			limits := work.IngressLimits{EndpointNonTerminal: 1}
			if byBytes {
				limits = work.IngressLimits{WorkspaceRawBytes: int64(len(body))}
			}
			rig.h.workStore = store.WithIngressLimits(limits)
			first := rig.serve(rig.keyedRequest(body, "first"))
			if first.Code != 202 {
				t.Fatalf("first: %d %s", first.Code, first.Body.String())
			}
			for _, tc := range []struct {
				body, key string
				want      int
			}{
				{body, "first", 202},
				{`{"event":"changed"}`, "first", 409},
				{body, "second", 429},
			} {
				rr := rig.serve(rig.keyedRequest(tc.body, tc.key))
				if rr.Code != tc.want {
					t.Fatalf("key %s: HTTP %d want %d: %s", tc.key, rr.Code, tc.want, rr.Body.String())
				}
				if tc.want == 429 && rr.Header().Get("Retry-After") == "" {
					t.Fatal("capacity refusal has no Retry-After")
				}
			}
			if n := countWebhookRows(t, rig.db, `SELECT COUNT(*) FROM work_items`); n != 1 {
				t.Fatalf("accepted %d items past capacity", n)
			}
		})
	}
}
