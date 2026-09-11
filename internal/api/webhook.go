package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
	"github.com/crewship-ai/crewship/internal/untrusted"
	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/work"
	"github.com/crewship-ai/crewship/internal/ws"
)

// webhookIdempotencyKeyCtxKey carries the request's Idempotency-Key header
// from ServeHTTP into trigger (the webhook.Handler.TriggerFunc contract
// doesn't pass headers, and internal/webhook is out of scope to change).
type webhookIdempotencyKeyCtxKey struct{}

// webhookSignatureCtxKey carries the request's X-Signature from ServeHTTP into
// trigger. When present it binds the dedup key to the (secret-signed, therefore
// attacker-unforgeable) signature instead of the client-controlled
// Idempotency-Key — so a replay of a captured signed delivery collapses to one
// run and a sender can't grind distinct keys to bypass the dedup window.
type webhookSignatureCtxKey struct{}

// defaultAgentWebhookRatePerMin caps how many agent-webhook RunAgent
// dispatches a single agent can trigger in a 60s window. R4#3: each
// delivery spawns a 10-min RunAgent, and distinct Idempotency-Keys bypass
// the dedup window, so without a per-agent gate a signed (or distributed)
// sender could fan out unbounded concurrent runs against one agent. The
// pipeline side has the equivalent guard via pipeline.AllowWebhookFire;
// this is its agent-webhook mirror, keyed by agent id instead of token.
//
// Generous on purpose — it throttles abuse, not normal use. 60/min ≈
// one run/second per agent is far above any legitimate single-agent
// webhook cadence; the cap exists to deny a flood, not to rate-shape
// healthy traffic.
const defaultAgentWebhookRatePerMin = 60

// defaultAgentWebhookMaxConcurrent caps how many agent-webhook runs a
// single agent may have IN FLIGHT at once (process-local). RunAgent
// holds the slot for up to its 10-min timeout; the registry frees it on
// completion. This is the second layer of the R4#3 gate: even within the
// per-minute budget, a burst that all lands before any run finishes can't
// pin more than this many concurrent 10-min runs on one agent.
const defaultAgentWebhookMaxConcurrent = 8

// agentWebhookConcurrencyKey is the synthetic concurrency-key prefix for
// agent-webhook runs in the shared RunRegistry. Keyed per agent so the
// in-flight cap is per-agent, independent of pipeline runs.
const agentWebhookConcurrencyKey = "agent-webhook"

// agentRunner is the whole of what the webhook path needs from the thing that
// runs agents: start one, stop one, ask whether one is still there.
//
// It is an interface rather than *orchestrator.Orchestrator because those three
// calls are the boundary between this package's logic and an actual operating
// system process — and that boundary is the one place an end-to-end test has to
// be able to stand in. Everything above it (the route, acceptance, the ledger,
// the dispatcher, cancel, recovery) then runs as production code in the test
// rather than as a re-implementation of it, which is the difference between
// proving the wiring and asserting that a mock was called.
//
// *orchestrator.Orchestrator satisfies it. Nothing else does in production.
type agentRunner interface {
	RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, handler orchestrator.EventHandler) error
	StopRun(ctx context.Context, runID string) (bool, error)
	RunIsAlive(ctx context.Context, runID string) (bool, error)
}

// WebhookHandler receives incoming webhook events and triggers agent runs.
type WebhookHandler struct {
	db        *sql.DB
	handler   *webhook.Handler
	logger    *slog.Logger
	resolver  chatbridge.ChatResolver
	orch      agentRunner
	hub       *ws.Hub
	container provider.ContainerProvider
	logWriter *logcollector.Writer
	// fence neutralizes the untrusted webhook payload before it reaches the
	// prompt (#808). It carries an observer that logs elevated-suspicion
	// ingress so ops can see injection attempts at the chokepoint.
	fence *untrusted.Fence

	// dispatchHint nudges the dispatcher that there may be work now.
	//
	// It is deliberately a plain func and deliberately allowed to be nil. This
	// handler must not be able to make execution depend on it: a hint that
	// could fail, block, or be forgotten would turn an optimisation into a
	// precondition, and the durable ledger is what actually guarantees the work
	// runs. Nil means "nobody is listening", which is a slower system and not a
	// broken one.
	dispatchHint func(workID string)

	// agentRatePerMin / agentMaxConcurrent gate agent-webhook dispatch
	// (R4#3). agentRatePerMin is 0 by default, meaning "follow the runtime
	// ratelimitcfg value" (admin-tunable, default 60/min) — see
	// agentRateLimit(). A test sets a positive value to pin the gate
	// deterministically. agentRuns is the shared in-flight registry backing
	// the concurrency cap.
	agentRatePerMin    int
	agentMaxConcurrent int
	agentRuns          *pipeline.RunRegistry

	// acceptOnce / acceptRunner / workStore are the durable acceptance path:
	// ONE transaction that records the delivery and the work it produces
	// before the sender is told anything (I1). See acceptance().
	acceptOnce   sync.Once
	acceptRunner acceptanceRunner
	acceptCloser func() error
	workStore    *work.Store
}

// acceptanceRunner bounds the single transaction the acceptance path is allowed
// to take. [work.Acceptor] is the real implementation; it holds a dedicated
// database handle whose busy_timeout sits under the answer budget, because a
// context deadline does NOT bound a SQLite write (measured: a 500 ms context
// against a lock held 5 s returned after 5044 ms).
type acceptanceRunner interface {
	Do(ctx context.Context, fn func(context.Context, *sql.Tx) error) error
}

// mainHandleAcceptor is the fallback when no dedicated handle can be opened —
// an in-memory database, or a path the driver will not disclose.
//
// It is deliberately weaker and says so: a context deadline is the only bound
// it has, and modernc.org/sqlite passes context.Background() into Commit, so a
// contended write here can outrun its budget. It exists so a test fixture or an
// unusual DSN degrades to "one transaction, best-effort bound" instead of to
// "no durable acceptance at all".
type mainHandleAcceptor struct {
	db     *sql.DB
	budget time.Duration
}

func (m *mainHandleAcceptor) Do(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, m.budget)
	defer cancel()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin acceptance: %w", work.ErrAcceptanceBudget, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit acceptance: %w", work.ErrAcceptanceBudget, err)
	}
	return nil
}

// SetAcceptor wires an explicitly-constructed acceptance handle, replacing the
// one this handler would otherwise open for itself. Server boot should use it
// so the handle's lifetime is owned somewhere that can close it; a test uses it
// to pin the budget. Call before the first delivery.
func (h *WebhookHandler) SetAcceptor(a *work.Acceptor) {
	h.acceptOnce.Do(func() {
		h.acceptRunner = a
		h.workStore = work.NewStore(h.db)
	})
}

// Close releases the acceptance handle if this handler opened one of its own.
// It is safe to call on a handler that never accepted anything.
func (h *WebhookHandler) Close() error {
	if h.acceptCloser != nil {
		return h.acceptCloser()
	}
	return nil
}

// acceptance resolves the acceptance runner and the work store, opening the
// dedicated handle on first use.
//
// Opening it here rather than at server boot is a compromise with a real cost,
// and it should move: the handle is opened lazily from the main handle's own
// file path, so nothing outside this file has to change, but nothing outside
// this file closes it either — Close exists and the router does not call it.
// The right home is the server's wiring, alongside the main database handle.
func (h *WebhookHandler) acceptance() (acceptanceRunner, *work.Store) {
	h.acceptOnce.Do(func() {
		if h.db == nil {
			return
		}
		h.workStore = work.NewStore(h.db)
		path := sqliteMainFilePath(h.db)
		if path == "" {
			h.logger.Warn("webhook acceptance: no file path for the main database handle; " +
				"falling back to a context-bounded transaction on the shared pool")
			h.acceptRunner = &mainHandleAcceptor{db: h.db, budget: work.DefaultAcceptanceBudget}
			return
		}
		a, err := work.OpenAcceptor(path, work.DefaultAcceptanceBudget)
		if err != nil {
			h.logger.Warn("webhook acceptance: could not open the dedicated handle; "+
				"falling back to a context-bounded transaction on the shared pool", "error", err)
			h.acceptRunner = &mainHandleAcceptor{db: h.db, budget: work.DefaultAcceptanceBudget}
			return
		}
		h.acceptRunner, h.acceptCloser = a, a.Close
	})
	return h.acceptRunner, h.workStore
}

// sqliteMainFilePath asks the driver where the "main" database lives. An empty
// answer means an in-memory or otherwise pathless database.
func sqliteMainFilePath(db *sql.DB) string {
	rows, err := db.Query("PRAGMA database_list")
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var seq int
		var name, file sql.NullString
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return ""
		}
		if name.String == "main" {
			return strings.TrimSpace(file.String)
		}
	}
	return ""
}

// agentRateLimit resolves the effective per-agent per-minute cap: an explicit
// positive field (test override) wins; otherwise the runtime-tunable
// ratelimitcfg value (admin-adjustable, defaults to defaultAgentWebhookRatePerMin).
func (h *WebhookHandler) agentRateLimit() int {
	if h.agentRatePerMin > 0 {
		return h.agentRatePerMin
	}
	if v := ratelimitcfg.Int(ratelimitcfg.KeyWebhookAgentPerMin); v > 0 {
		return v
	}
	return defaultAgentWebhookRatePerMin
}

// NewWebhookHandler creates a WebhookHandler with the given dependencies for webhook verification and dispatch.
func NewWebhookHandler(
	db *sql.DB,
	logger *slog.Logger,
	resolver chatbridge.ChatResolver,
	orch agentRunner,
	hub *ws.Hub,
	container provider.ContainerProvider,
	logWriter *logcollector.Writer,
) *WebhookHandler {
	wh := &WebhookHandler{
		db:                 db,
		logger:             logger,
		resolver:           resolver,
		orch:               orch,
		hub:                hub,
		container:          container,
		logWriter:          logWriter,
		agentRatePerMin:    0, // 0 → follow runtime ratelimitcfg (default defaultAgentWebhookRatePerMin)
		agentMaxConcurrent: defaultAgentWebhookMaxConcurrent,
		agentRuns:          pipeline.NewRunRegistry(),
	}
	// Fence with an observer so a webhook payload the scanner rates medium+ is
	// logged (source + suspicion + finding count) — visibility into injection
	// attempts without changing the annotate-don't-block contract.
	wh.fence = untrusted.New().WithObserver(func(source, suspicion string, findings int) {
		logger.Warn("untrusted webhook ingress flagged for likely injection",
			"source", source, "suspicion", suspicion, "findings", findings)
	})
	// Re-delivery dedup used to borrow the pipeline idempotency table: a
	// reservation written before the run and deleted when the run failed. The
	// delivery ledger replaces it. Identity is (workspace, endpoint, source
	// delivery id) recorded in the SAME transaction as the work, so there is no
	// window where a reservation points at a run that was never created, and no
	// deletion path that lets an already-performed effect be repeated.

	wh.handler = webhook.NewHandler(logger, wh.lookupSecret, wh.trigger)
	// The durable acceptance path. With it set the inner handler answers §5's
	// receipt — {delivery_id, work_id, status, duplicate} after the commit —
	// and calls the TriggerFunc above not at all.
	wh.handler.SetAcceptFunc(wh.acceptWebhook)
	// Per-agent replay policy (#815): the handler asks this before accepting a
	// body-only or plaintext-secret delivery. Read locally from the agents
	// table (this process owns the DB) rather than over the internal IPC hop.
	wh.handler.SetRequireTimestampLookup(wh.lookupRequireTimestamp)
	return wh
}

// lookupRequireTimestamp reports whether the agent mandates the timestamped
// webhook signature scheme (webhook_require_timestamp, #815). A nil db (test
// wiring) or a missing row yields false — the timestamp stays optional.
func (h *WebhookHandler) lookupRequireTimestamp(ctx context.Context, _, agentID string) (bool, error) {
	if h.db == nil {
		return false, nil
	}
	var require sql.NullBool
	err := h.db.QueryRowContext(ctx,
		`SELECT webhook_require_timestamp FROM agents WHERE id = ?`, agentID).Scan(&require)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return require.Valid && require.Bool, nil
}

// ServeHTTP dispatches incoming webhook requests to the underlying webhook handler.
// It stashes the Idempotency-Key header into the request context first so trigger
// (whose webhook.TriggerFunc signature can't carry headers) can read it for dedup.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		r = r.WithContext(context.WithValue(r.Context(), webhookIdempotencyKeyCtxKey{}, key))
	}
	// The signature (verified downstream against the agent's secret) is a
	// stronger dedup identity than the client-controlled Idempotency-Key —
	// stash it so trigger can prefer it (see agentWebhookIdempotencyKey).
	if sig := r.Header.Get("X-Signature"); sig != "" {
		r = r.WithContext(context.WithValue(r.Context(), webhookSignatureCtxKey{}, sig))
	}

	// The slot the acceptance path fills with everything that must NOT happen
	// before the sender is answered. This ordering is W2: a webhook used to
	// block on a container start — an image pull, on a cold crew — while
	// GitHub counted down its ten-second response deadline.
	slot := &webhookDispatchSlot{}
	r = r.WithContext(context.WithValue(r.Context(), webhookDispatchSlotKey{}, slot))

	h.handler.ServeHTTP(w, r)

	if slot.run == nil {
		return
	}
	// Push the receipt out before the dispatch begins. net/http would flush it
	// when this handler returns anyway, but "anyway" is not an ordering: the
	// point of the split is that the sender has its answer first, so make that
	// explicit rather than leaving it to the buffer.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	slot.run()
}

func (h *WebhookHandler) lookupSecret(ctx context.Context, crewID, agentID string) (string, error) {
	// Read locally from the agents table — this process owns the DB (same
	// rationale as lookupRequireTimestamp, #815). The secret used to round-trip
	// the internal IPC endpoint in plaintext JSON (#999); that endpoint is gone
	// and the raw value now never leaves this process. Crew scoping mirrors the
	// (crew, agent) pair the webhook URL named — without it any crew's secret
	// would be fetchable by agent id alone.
	if h.db == nil {
		return "", errors.New("webhook secret lookup unavailable: no database")
	}
	query := `SELECT webhook_secret FROM agents WHERE id = ? AND deleted_at IS NULL`
	args := []any{agentID}
	if crewID != "" {
		query += " AND crew_id = ?"
		args = append(args, crewID)
	}
	var secret sql.NullString
	err := h.db.QueryRowContext(ctx, query, args...).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("webhook secret lookup: agent %s not found", agentID)
	}
	if err != nil {
		return "", fmt.Errorf("webhook secret lookup: %w", err)
	}
	if !secret.Valid || secret.String == "" {
		return "", fmt.Errorf("webhook secret not configured for agent %s", agentID)
	}
	// #1072/#1029: the stored value is AES-256-GCM encrypted at rest when a key
	// is configured. Decrypt an enveloped value; a bare value (legacy/key-less)
	// passes through. A decrypt error is logged but the raw value is returned —
	// a wrong secret simply fails the HMAC comparison, so this never grants
	// access.
	plain, decErr := encryption.DecryptIfEncrypted(secret.String)
	if decErr != nil {
		h.logger.Warn("webhook secret decrypt failed; using stored value as-is",
			"agent_id", agentID, "error", decErr)
	}
	return plain, nil
}

// webhookIdempotencyKey resolves the dedup key for a webhook delivery.
// Preference order:
//  1. The X-Signature (stashed by ServeHTTP) — it is HMAC'd with the agent's
//     secret, so it is attacker-unforgeable. Using it stops a replay of a
//     captured signed webhook from bypassing the dedup window by grinding a
//     fresh Idempotency-Key. KNOWN LIMITATION: for TIMESTAMPED signatures the
//     signature covers "timestamp.body", so a legitimate retry that re-signs
//     with a fresh timestamp produces a different signature and is treated as a
//     NEW delivery (a second run). A captured request replayed verbatim still
//     dedupes (same timestamp+sig). Fully fixing timestamped-retry dedup needs a
//     stable delivery id (Svix webhook-id header) — tracked as a follow-up; not
//     resolved by this PR.
//  2. The caller-supplied Idempotency-Key (legacy secret path, un-migrated
//     senders).
//  3. A deterministic synthetic key over agent id + payload — an external
//     system that re-fires the identical event without a key still collapses to
//     a single run, while distinct events produce distinct keys.
func agentWebhookIdempotencyKey(ctx context.Context, agentID string, payload webhook.WebhookPayload) string {
	if sig, ok := ctx.Value(webhookSignatureCtxKey{}).(string); ok && sig != "" {
		// Canonicalize the hex so equivalent spellings (upper/lower case) can't
		// be ground into distinct dedup identities, and scope by agent so two
		// same-workspace agents can't collide on identical signature material.
		if raw, err := hex.DecodeString(sig); err == nil {
			sig = hex.EncodeToString(raw)
		}
		return "whsig-" + agentID + "-" + sig
	}
	if v, ok := ctx.Value(webhookIdempotencyKeyCtxKey{}).(string); ok && v != "" {
		return v
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%+v", agentID, payload.Event, payload.Source, payload.Data)
	return "wh-" + hex.EncodeToString(h.Sum(nil))
}

// agentWebhookRateKey namespaces the per-agent rate window inside the
// shared pipeline rate limiter so an agent id can never collide with a
// pipeline webhook token (which is an opaque hex string). The "awh:"
// prefix marks the agent-webhook namespace.
func agentWebhookRateKey(agentID string) string {
	return "awh:" + agentID
}

// webhookDispatchSlotKey carries a pointer from ServeHTTP down into the
// acceptance path, so the work that must happen AFTER the response can be
// handed back up without changing webhook.AcceptFunc's signature.
type webhookDispatchSlotKey struct{}

// webhookDispatchSlot holds the post-response dispatch. ServeHTTP runs it only
// once the receipt has been written — that ordering is the whole of W2.
type webhookDispatchSlot struct{ run func() }

// webhookPingEvent is the one event this surface answers without an agent. §5
// requires a valid ping to be a 200 `ignored` with a safe reason, and a ping
// that started a ten-minute container run was the clearest case of the webhook
// surface doing real work for a health check.
const webhookPingEvent = "ping"

// agentEndpointKind is the endpoint_kind recorded for this surface. The three
// surfaces (agent, routine, page) have different legacy contracts and §5
// requires them to stay distinguishable in the ledger.
const agentEndpointKind = "agent"

// agentEndpointProfile records WHICH legacy auth shape verified a delivery.
//
// It is recorded, never used to CHOOSE a scheme. §12 forbids an endpoint's
// signature behaviour changing without an explicit configuration change, and
// §5 forbids a legacy endpoint switching profile because a header appeared —
// so verification stays exactly where it was, inside internal/webhook, with
// exactly the semantics it had. This function only reads back, after the fact,
// which of the three shapes the request actually presented, so an audit of the
// ledger can tell a timestamped HMAC from the deprecated plaintext secret.
func agentEndpointProfile(hdr http.Header) string {
	if hdr == nil {
		return "legacy-agent"
	}
	switch {
	case hdr.Get("X-Signature") != "" && hdr.Get("X-Timestamp") != "":
		return "legacy-agent-ts-hmac"
	case hdr.Get("X-Signature") != "":
		return "legacy-agent-hmac"
	case hdr.Get("X-Webhook-Secret") != "":
		return "legacy-agent-secret"
	}
	return "legacy-agent"
}

// receiptToAcceptance turns a ledger receipt into the HTTP-facing answer.
func receiptToAcceptance(r work.Receipt) webhook.Acceptance {
	return webhook.Acceptance{
		DeliveryID: r.DeliveryID,
		WorkID:     r.WorkID,
		State:      string(r.State),
		Duplicate:  r.Duplicate,
		Ignored:    r.WorkID == "" && r.State == "",
		Reason:     "",
	}
}

// acceptWebhook is the webhook.AcceptFunc: it records the delivery durably and
// hands the post-response dispatch to the slot ServeHTTP put on the context.
//
// When there is no slot — a direct call from a test, or the legacy TriggerFunc
// path — the dispatch runs inline, which is what the code did before. That is
// the only difference between the two entrypoints; the acceptance itself is one
// implementation.
func (h *WebhookHandler) acceptWebhook(ctx context.Context, crewID, agentID string, in webhook.Inbound) (webhook.Acceptance, error) {
	acc, dispatch, err := h.acceptDelivery(ctx, crewID, agentID, in)
	if err != nil {
		return webhook.Acceptance{}, err
	}
	if dispatch == nil {
		return acc, nil
	}
	if slot, ok := ctx.Value(webhookDispatchSlotKey{}).(*webhookDispatchSlot); ok && slot != nil {
		slot.run = dispatch
		return acc, nil
	}
	dispatch()
	return acc, nil
}

// trigger is the legacy webhook.TriggerFunc entrypoint, kept so a caller that
// only wants "run it" — and every existing test that drives this handler
// directly — keeps working. It is the same two phases, back to back.
func (h *WebhookHandler) trigger(ctx context.Context, crewID, agentID string, payload webhook.WebhookPayload) error {
	_, dispatch, err := h.acceptDelivery(ctx, crewID, agentID, webhook.Inbound{Payload: payload})
	if err != nil {
		return err
	}
	if dispatch != nil {
		dispatch()
	}
	return nil
}

// acceptDelivery is the durable acceptance path: it records the delivery and
// the work it produces in ONE transaction and returns the receipt, plus the
// dispatch its caller must run AFTER the response.
//
// What changed, and why the shape looks like this:
//
//   - W2. The container warm used to sit HERE, between the request and its
//     answer: crewstart(...).Start blocking on an image pull while GitHub
//     counted down its ten-second response deadline. Nothing outside the
//     database happens before the receipt now. The returned closure is the
//     container start, the run record and the agent run, and ServeHTTP runs it
//     after the 202 is written.
//
//   - W3. The dedup reservation and the thing it reserved used to be two
//     writes with a crash window between them: a reservation in
//     pipeline_run_idempotency, then a run created inside a goroutine. A crash
//     in between left a reservation pointing at a run that never existed, and
//     the sender's retry was deduped onto that phantom forever. The delivery
//     and its work item are now one commit, so they exist together or not at
//     all, and the ledger — not the reservation table — is what a re-delivery
//     is matched against.
//
//   - W4. "Dispatching anyway" is gone. Recording the acceptance is a
//     precondition for running: if the commit does not happen the sender is
//     told 503 and no agent starts, rather than being told nothing while an
//     unrecorded run goes ahead.
//
// The returned dispatch is nil when there is nothing to run: a duplicate, or a
// delivery the filter ignored.
func (h *WebhookHandler) acceptDelivery(ctx context.Context, crewID, agentID string, in webhook.Inbound) (webhook.Acceptance, func(), error) {
	payload := in.Payload
	h.logger.Info("webhook accept", "crew_id", crewID, "agent_id", agentID, "event", payload.Event)

	// 1. Resolve agent config. workspaceID is "" here: the webhook path has
	// no caller-supplied tenant scope before resolve, and the request was
	// already authenticated against THIS agent's per-agent webhook secret
	// (now also crew-scoped via lookupSecret), so a by-agent resolve is
	// sound. The workspace then comes back on info.WorkspaceID.
	//
	// This is the one lookup that still happens outside the database before
	// the response, and with the IPC resolver it is an HTTP hop to ourselves.
	// It stays because the acceptance cannot be recorded without knowing which
	// workspace it belongs to; it is a policy read, not the container warm and
	// the ten-minute run that W2 is about.
	info, err := h.resolver.ResolveAgent(ctx, agentID, "")
	if err != nil {
		return webhook.Acceptance{}, nil, fmt.Errorf("resolve agent: %w", err)
	}

	runner, store := h.acceptance()
	if runner == nil || store == nil || info.WorkspaceID == "" {
		// No ledger means no way to record the acceptance, and W4 is precisely
		// that running without recording is not allowed. 503 is the honest
		// answer: retryable, and never a false 202.
		h.logger.Error("webhook: durable acceptance unavailable, refusing the delivery",
			"agent_id", agentID, "workspace_id", info.WorkspaceID, "have_runner", runner != nil)
		return webhook.Acceptance{}, nil, fmt.Errorf(
			"%w: no delivery ledger for agent %s", webhook.ErrUnavailable, agentID)
	}

	// Mint the run id ONCE, up front. It is the work item's domain id, the id
	// handed to CreateRun, the id stamped on every journal entry beneath the
	// run, and the id the orchestrator derives this attempt's tmux session and
	// /tmp paths from. generateCUID (not UnixNano) — two webhooks landing in
	// the same nanosecond tick would otherwise mint identical ids and collide
	// on the runs PK.
	runID := generateCUID()

	// Delivery identity is (workspace, endpoint, source delivery id), and for
	// this legacy surface the source delivery id is the same value that used to
	// be the idempotency key: the unforgeable signature when one was sent, the
	// caller's Idempotency-Key when it was not, and a hash over agent + payload
	// otherwise. Identity is deliberately NOT derived from anything new here —
	// changing what counts as "the same delivery" is a configuration change
	// under §12, not a side effect of moving where it is recorded.
	sourceDeliveryID := agentWebhookIdempotencyKey(ctx, agentID, payload)

	rawBody := in.RawBody
	if rawBody == nil {
		// The direct-call path (legacy TriggerFunc) has no wire bytes. Record a
		// canonical re-encoding rather than nothing, so the ledger still has a
		// body hash to detect a same-id-different-body arrival with. It is not
		// the signed bytes and must not be used as if it were.
		rawBody, _ = json.Marshal(payload)
	}
	sum := sha256.Sum256(rawBody)

	// Ingress gates, still in front of acceptance. They are the flood defence
	// this surface has: each delivery becomes a ten-minute run, and a
	// distributed sender with fresh keys can otherwise fan out without bound.
	//
	// §5 says a duplicate of already-accepted work must not be refused for
	// fullness, so a refusal falls back to the ledger first: if this delivery
	// is already recorded, the sender gets its original receipt instead of a
	// 429. A brand-new delivery arriving mid-flood still gets the 429.
	ratePerMin := h.agentRateLimit()
	if !pipeline.AllowWebhookFire(agentWebhookRateKey(agentID), ratePerMin) {
		h.logger.Warn("webhook rate gate: agent over per-minute limit",
			"agent_id", agentID, "limit_per_min", ratePerMin)
		return h.receiptOrRefusal(ctx, store, info.WorkspaceID, agentID, sourceDeliveryID,
			fmt.Errorf("%w: agent %s rate limit exceeded (%d/min)",
				webhook.ErrIngressFull, agentID, ratePerMin))
	}

	// In-flight concurrency cap, acquired UP FRONT so a throttled delivery
	// warms no container and writes no run row. The slot is held for the
	// lifetime of the run and released inside the dispatch closure.
	var releaseSlot func()
	if h.agentRuns != nil {
		_, release, acqErr := h.agentRuns.Acquire(ctx, pipeline.AcquireOpts{
			RunID:          runID,
			WorkspaceID:    info.WorkspaceID,
			ConcurrencyKey: agentWebhookConcurrencyKey + ":" + agentID,
			MaxConcurrent:  h.agentMaxConcurrent,
		})
		if acqErr != nil {
			h.logger.Warn("webhook concurrency gate: agent at in-flight cap",
				"agent_id", agentID, "max_concurrent", h.agentMaxConcurrent)
			return h.receiptOrRefusal(ctx, store, info.WorkspaceID, agentID, sourceDeliveryID,
				fmt.Errorf("%w: agent %s concurrency limit reached (%d)",
					webhook.ErrIngressFull, agentID, h.agentMaxConcurrent))
		}
		releaseSlot = release
	}
	releaseOnce := func() {
		if releaseSlot != nil {
			releaseSlot()
			releaseSlot = nil
		}
	}

	// The filter. A ping is recorded and answered, never run: §5 wants the
	// decision auditable, not invisible, so an ignored delivery still gets a
	// ledger row carrying its reason — it simply produces no work.
	decision, reason := work.FilterAccepted, ""
	if payload.Event == webhookPingEvent {
		decision, reason = work.FilterIgnored, "ping"
	}

	d := work.Delivery{
		WorkspaceID:      info.WorkspaceID,
		EndpointID:       agentID,
		EndpointKind:     agentEndpointKind,
		Profile:          agentEndpointProfile(in.Header),
		SourceDeliveryID: sourceDeliveryID,
		BodySHA256:       hex.EncodeToString(sum[:]),
		BodyBytes:        len(rawBody),
		RawBody:          rawBody,
		EventType:        payload.Event,
		FilterDecision:   decision,
		FilterReason:     reason,
	}
	// Input the dispatch will read back. It is the immutable record of what was
	// accepted, so a replay can be shown the input it would re-run — and it is
	// typed as [webhookRunInput] rather than an ad-hoc map because it is a
	// CONTRACT between two halves that no longer share a process: this half
	// writes it now, the dispatcher's runtime adapter reads it back later,
	// possibly after a restart.
	//
	// It carries the delivery's own content and the ids naming what it is for.
	// It deliberately carries nothing DERIVED about the agent — not its crew
	// config, not its permissions, not its runtime shape — because work waits
	// for capacity and every one of those answers is allowed to change while it
	// waits. Those are resolved fresh at dispatch (§3's I8).
	//
	// The payload is stored rather than read back from the delivery row: the
	// retention sweep may drop a raw body, and a work item that cannot be run
	// without a row that retention is allowed to delete is not durable.
	inputJSON, _ := json.Marshal(webhookRunInput{
		AgentID: agentID,
		CrewID:  info.CrewID,
		Payload: payload,
	})
	req := work.AcceptRequest{
		WorkspaceID: info.WorkspaceID,
		Source:      work.SourceWebhook,
		DomainKind:  work.DomainAgentRun,
		DomainID:    runID,
		AgentID:     agentID,
		CrewID:      info.CrewID,
		SessionID:   fmt.Sprintf("webhook-%s-%s", agentID, runID),
		Class:       work.ClassBackground,
		InputJSON:   string(inputJSON),
		InputSHA256: d.BodySHA256,
	}

	var receipt work.Receipt
	acceptErr := runner.Do(ctx, func(ctx context.Context, tx *sql.Tx) error {
		r, err := store.AcceptDeliveryTx(ctx, tx, d, req)
		if err != nil {
			return err
		}
		receipt = r
		return nil
	})
	if acceptErr != nil {
		releaseOnce()
		switch {
		case errors.Is(acceptErr, work.ErrDeliveryConflict):
			// Same source id, different body. The original record is kept and
			// the sender is told which of the two things it is, without being
			// told anything about the stored body.
			h.logger.Warn("webhook delivery conflict: same source id, different body",
				"agent_id", agentID, "workspace_id", info.WorkspaceID)
			return webhook.Acceptance{}, nil, fmt.Errorf("%w: %w", webhook.ErrDeliveryConflict, acceptErr)
		default:
			// Budget exceeded, database unavailable, constraint fault — all of
			// them 503 and none of them a 202. W4: nothing runs.
			h.logger.Error("webhook acceptance did not commit; no agent started",
				"agent_id", agentID, "workspace_id", info.WorkspaceID, "error", acceptErr)
			return webhook.Acceptance{}, nil, fmt.Errorf("%w: %w", webhook.ErrUnavailable, acceptErr)
		}
	}

	acc := receiptToAcceptance(receipt)
	if decision == work.FilterIgnored {
		acc.Ignored, acc.Reason = true, reason
	}

	// A re-delivery gets the ORIGINAL receipt and the work's CURRENT state, and
	// starts nothing. This is where the "no second attempt merely because the
	// message arrived twice" invariant is actually enforced — inside the same
	// transaction that would have created the work.
	if receipt.Duplicate || decision == work.FilterIgnored {
		releaseOnce()
		return acc, nil, nil
	}

	// Acceptance is finished. It commits the delivery and the work, and then
	// says so — nothing more.
	//
	// The hint is an optimisation: it tells the dispatcher there may be work
	// now, so the common case does not wait for a poll. Losing it costs
	// latency and nothing else, which is the only reason it is allowed to be
	// best-effort. If the execution depended on it, a crash between this commit
	// and the nudge would strand work — precisely the window the durable ledger
	// exists to survive, and one the tests cover by never sending a hint at all.
	//
	// It used to return a closure that started an agent. That closure was the
	// second owner of this work: the ledger said `queued` while a runtime ran,
	// a finished agent need never finish its work item, and a restart resumed
	// nothing. Review finding R3.
	if h.dispatchHint != nil {
		h.dispatchHint(acc.WorkID)
	}
	releaseOnce()
	return acc, nil, nil
}

// receiptOrRefusal answers a gate rejection.
//
// §5: "Duplicate již přijaté práce není odmítnuta kvůli zaplnění" — a delivery
// that has already been accepted is not refused because the ingress is full, it
// gets its receipt back. So a refusal consults the ledger first and only
// returns the refusal when this really is new work.
//
// The lookup is by source delivery id only, so it cannot detect a
// same-id-different-body arrival; that stays the acceptance transaction's job.
// The consequence is narrow and worth naming: a conflicting body arriving
// during a flood is answered as a duplicate rather than as a 409, and the
// sender sees the conflict on its next un-throttled retry.
func (h *WebhookHandler) receiptOrRefusal(
	ctx context.Context, store *work.Store, workspaceID, agentID, sourceDeliveryID string, refusal error,
) (webhook.Acceptance, func(), error) {
	existing, err := store.LookupDelivery(ctx, workspaceID, agentID, sourceDeliveryID)
	if err == nil && existing != nil {
		h.logger.Info("webhook: gate refused a delivery already in the ledger; answering with its receipt",
			"agent_id", agentID, "delivery_id", existing.DeliveryID)
		return receiptToAcceptance(*existing), nil, nil
	}
	return webhook.Acceptance{}, nil, refusal
}

// runWebhookAgent executes ONE webhook-triggered agent run, synchronously.
//
// It used to be fire-and-forget, launched by the handler the moment a delivery
// was accepted — which is exactly what review finding R3 was about: the work
// item said `queued` while an agent ran, and nothing tied the two together.
// The dispatcher owns the goroutine now, so this returns when the run does.
//
// started() is called once, from the first stream event. That is the honest
// moment to say the runtime exists: the process is producing output. Calling it
// before RunAgent would claim a confirmation for something that had not yet
// happened, which is the inference the start protocol exists to avoid.
func (h *WebhookHandler) runWebhookAgent(
	ctx context.Context,
	info *chatbridge.ChatInfo,
	agentID, runID string,
	payload webhook.WebhookPayload,
	releaseSlot func(),
	started func(),
) error {
	// 2. Create a chat session for THIS DELIVERY.
	//
	// It used to be fmt.Sprintf("webhook-%s", agentID) — one constant chat id
	// per agent, "so we don't spam sessions". But h.agentMaxConcurrent (8)
	// permits eight simultaneous runs of that same agent, so eight deliveries
	// in flight all published on `session:webhook-<agentID>` and all appended
	// to one conversation: two run streams merged on nothing but the agent id,
	// which is exactly what the E0 contract forbids ("Dva run streams se nikdy
	// nesloučí jen podle agent slug/ChatID" — IMPLEMENTATION §9).
	//
	// Keyed by runID, which is already unique per delivery AND already
	// deduplicated: a re-delivery short-circuits at the idempotency check well
	// above this line, so a retried webhook does not create a second chat.
	chatID := fmt.Sprintf("webhook-%s-%s", agentID, runID)
	if err := h.resolver.CreateChat(ctx, chatbridge.CreateChatRequest{
		ChatID:      chatID,
		AgentID:     agentID,
		WorkspaceID: info.WorkspaceID,
		Title:       fmt.Sprintf("Webhook: %s", payload.Event),
	}); err != nil {
		h.logger.Warn("failed to create/ensure webhook chat session", "error", err)
	}

	// 3. Ensure container is running
	// Same assembly as chat (chatbridge.ChatInfo.CrewRuntimeConfig), so a crew
	// woken by a webhook gets the container it would have got from a chat
	// message — declared sidecars included (#1708).
	whCfg, whCfgErr := info.CrewRuntimeConfig(0, 0)
	if whCfgErr != nil {
		h.logger.Warn("webhook: crew services unresolved, starting without them",
			"agent_id", agentID, "crew_id", info.CrewID, "error", whCfgErr)
	}
	containerID, err := crewstart.New(h.container, NewCrewConfigCompleter(h.db), h.logger).Start(ctx, whCfg)
	if err != nil {
		if releaseSlot != nil {
			releaseSlot()
		}
		// The delivery and its work item are already committed, so nothing is
		// lost here and nothing is deleted: the ledger never forgets a delivery
		// on failure (that is the whole difference from the reservation this
		// replaced, which was deleted and then silently re-ran already-
		// performed effects). The work stays `queued` and is recoverable by
		// whoever owns dispatch.
		//
		// It is worth being precise about what "recoverable" means today: the
		// work item is durable and correct, and the dispatcher owns retrying
		// it — this failure is reported upward rather than swallowed, so the
		// attempt lands in retry_wait instead of vanishing.
		h.logger.Error("webhook dispatch: crew runtime did not start",
			"agent_id", agentID, "run_id", runID, "error", err)
		// Safe to repeat, and the proof is CONTROL FLOW rather than the error:
		// RunAgent is below this line and was never reached, so no agent turn
		// happened in this attempt and none of its external effects can have.
		// The error itself proves nothing — a provider timeout may well have
		// left a container half-created — but crew startup is idempotent by
		// name, so a retry converges on one container rather than making a
		// second.
		return fmt.Errorf("%w: crew runtime did not start: %w", errWebhookBeforeAgent, err)
	}

	// 4. Create run record. Reuses the runID minted (and idempotency-
	// reserved) at the top of trigger — R6: the run record and the
	// idempotency reservation share one id, so a redelivery resolves to a
	// run that exists.
	runMeta := map[string]interface{}{
		"event":   payload.Event,
		"source":  payload.Source,
		"trigger": "WEBHOOK",
	}
	if err := h.resolver.CreateRun(ctx, runID, agentID, chatID, info.WorkspaceID, "WEBHOOK", runMeta); err != nil {
		// W4: this used to warn and carry on, so an agent could run a full
		// ten-minute turn with no run row anywhere — no journal parent, no
		// status to update at the end, nothing for an operator to find. The
		// run record is the only evidence an unattended run leaves, so its
		// absence stops the run rather than being logged past.
		if releaseSlot != nil {
			releaseSlot()
		}
		h.logger.Error("webhook dispatch: run record not created; not starting the agent",
			"agent_id", agentID, "run_id", runID, "error", err)
		// An error from CreateRun does NOT prove the row is absent: the write
		// may have landed and the response been lost. So look, by the stable
		// run id we already hold. Present, or unreadable, means unclear —
		// retrying a run record that exists would be a second record for one
		// attempt. Only a confirmed absence is safe to repeat.
		if absent, lookupErr := h.runRecordAbsent(ctx, runID); lookupErr != nil {
			return fmt.Errorf("run record write failed and its state could not be established: %w", err)
		} else if !absent {
			return fmt.Errorf("run record write reported an error but the record exists: %w", err)
		}
		return fmt.Errorf("%w: run record not created: %w", errWebhookBeforeAgent, err)
	}

	// 5. Build user message from payload. The payload fields (event/source/
	// data) are attacker-controlled external input, so they are neutralized
	// through the ingress trust fence (issue #808) before reaching the model
	// — nonce-fenced and injection-scanned, never interpolated raw. The
	// "webhook" source label is caller-derived, not read from payload.Source
	// (which is itself untrusted).
	userMsg := "Webhook event received:\n" + h.fence.Wrap("webhook",
		fmt.Sprintf("Event: %s\nSource: %s\nData: %+v", payload.Event, payload.Source, payload.Data))

	// 6. Run agent (async)
	// WithoutCancel preserves the request's OTel trace span + auth values so
	// the async run remains observable, while shedding the request's
	// cancellation -- the webhook library's handler returns to the caller
	// before this goroutine finishes, and the request's ctx is cancelled
	// once the response flushes. The `ctx` parameter is the request ctx
	// threaded in from the webhook handler.
	// ctx already carries WithoutCancel from dispatchAccepted.
	{
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		// Put the run on the context so every journal entry the orchestrator
		// emits beneath it inherits trace_id = runID. Its JournalEntry has no
		// TraceID field and prepareEntry fills trace_id only from
		// RunIDFromContext, so without this the run.session_init alarm — the
		// severity=error entry that says the CLI dropped an MCP server —
		// lands unlinked and `crewship journal --run-id <id>` cannot find it.
		//
		// Every other driver stamps this (chatbridge, scheduler, assignments,
		// query_handler); #1950 fixed the scheduler and missed the neighbour
		// (#1954). A webhook run is unattended by definition, which is the
		// same argument that made it matter for cron.
		runCtx = journal.WithRunID(runCtx, runID)

		// The in-flight concurrency slot was acquired up front (before any
		// container/run work); release it when this run finishes.
		if releaseSlot != nil {
			defer releaseSlot()
		}

		// Build through the ONE request-builder (#810) so the webhook-
		// triggered agent carries its MCP servers, installed skills, persona,
		// and the crew-policy ApprovalMode — not just the fields this literal
		// used to copy by hand.
		req := info.ToAgentRunRequest(chatbridge.AgentRunOverrides{
			ChatID:      chatID,
			ContainerID: containerID,
			UserMessage: userMsg,
			LLMModel:    info.LLMModel,
			TimeoutSecs: info.TimeoutSecs,
			MemoryMB:    info.MemoryMB,
			CPUs:        info.CPUs,
		})
		// E0: hand the orchestrator the run id minted at the top of trigger()
		// — the same one reserved in the idempotency table, written to the run
		// record and stamped on every journal entry beneath this run. Without
		// it the orchestrator would derive this run's tmux session and /tmp
		// files from the agent slug alone, and the eight concurrent webhook
		// runs this handler explicitly allows would overwrite each other.
		req.RunID = runID
		// Externally-triggered runs are forced to restricted egress regardless
		// of the crew's own mode: the payload comes from outside, so it must
		// not be able to drive a `free` crew's agent to arbitrary hosts. The
		// crew's allowed_domains still apply (empty → the sidecar's
		// DefaultAllowedDomains, so LLM/CLI providers keep working).
		req.NetworkMode = "restricted"

		logBuf := logcollector.NewOutputBuffer(h.logWriter, info.CrewID, info.AgentSlug)
		defer logBuf.Close()

		// CaptureResultMeta: the accumulator is what carries the CLI's usage
		// numbers, the model it actually resolved to, and the session-init
		// provenance to the run record below. Discarding it left a
		// webhook-triggered run recording none of the three — and nobody is
		// watching a webhook run, so the record is the only place to read them
		// afterwards (#1934).
		base, acc := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
			LogBuf:            logBuf,
			AgentSlug:         info.AgentSlug,
			CaptureResultMeta: true,
		})
		// The first event means the CLI is alive and emitting. That is what
		// the dispatcher records as a confirmed runtime.
		var confirmOnce sync.Once
		handler := func(event orchestrator.AgentEvent) {
			if started != nil {
				confirmOnce.Do(started)
			}
			base(event)

			broadcastWorkspaceEvent(h.hub, info.WorkspaceID, "agent.log",
				map[string]interface{}{
					"ts":       event.Timestamp,
					"level":    "info",
					"agent":    info.AgentSlug,
					"agent_id": info.AgentID,
					"event":    event.Type,
					"content":  event.Content,
					"metadata": event.Metadata,
				})
		}

		startedAt := time.Now()
		// Guard against running while a backup holds the workspace
		// lock — otherwise this run's state would be written behind
		// the in-flight dump and miss from the bundle.
		var err error
		guardRelease, guardErr := refuseIfBackupInProgress(runCtx, h.db, req.WorkspaceID)
		if guardErr != nil {
			h.logger.Warn("webhook run refused — backup in progress", "workspace_id", req.WorkspaceID)
			err = guardErr
		} else {
			// Defer release so the guard is freed even if RunAgent
			// panics. Matches assignments.go and query_handler.go.
			defer guardRelease()
			err = h.orch.RunAgent(runCtx, req, handler)
		}

		exitCode := 0
		status := "COMPLETED"
		var errMsg *string
		if err != nil {
			status = "FAILED"
			s := err.Error()
			errMsg = &s
			exitCode = 1
		}

		// One metadata map for both outcomes, so the FAILED run carries the
		// same answers the COMPLETED one does. The guard-refused case above
		// never started a CLI, and then the accumulator is simply empty —
		// nothing is invented for it.
		completedMeta := map[string]interface{}{
			"duration_ms": time.Since(startedAt).Milliseconds(),
		}
		// One call decides what a terminal record carries, so a key added
		// later applies to every dispatch path rather than to whichever
		// copies someone remembered to edit (#1949).
		orchestrator.MergeRunAccumulator(completedMeta, acc, "")
		if updateErr := h.resolver.UpdateRun(runCtx, runID, status, &exitCode, errMsg, completedMeta); updateErr != nil {
			h.logger.Warn("failed to update run status", "run_id", runID, "status", status, "error", updateErr)
		}
		return err
	}
}
