package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// WebhookPayload is the JSON body received from an external webhook call,
// containing the event type, source identifier, and arbitrary data.
type WebhookPayload struct {
	Event  string    `json:"event"`
	Source string    `json:"source"`
	Data   any       `json:"data,omitempty"`
	RecvAt time.Time `json:"received_at"`
}

// SecretLookup retrieves the expected webhook secret for a given crew and agent pair.
type SecretLookup func(ctx context.Context, crewID, agentID string) (string, error)

// RequireTimestampLookup reports whether the (crew, agent) requires a
// timestamped signature. When it returns true, the replayable auth shapes —
// a body-only HMAC (no X-Timestamp) and the deprecated plaintext secret — are
// rejected, forcing the Stripe/Svix `ts.body` scheme whose freshness window
// bounds replay. Optional: a nil lookup (or one returning false) leaves the
// timestamp optional, preserving backward compatibility for un-migrated
// senders. A lookup ERROR is not a disabled policy: it refuses the delivery
// with a retryable 503, because a policy that cannot be read must not silently
// become the weaker of the two schemes (§5).
type RequireTimestampLookup func(ctx context.Context, crewID, agentID string) (bool, error)

// TriggerFunc is called after a webhook is validated, passing the crew/agent IDs
// and parsed payload to initiate an agent run.
//
// Deprecated in spirit: it can only say "it worked" or "it did not", so it
// cannot carry the delivery and work identifiers §5 requires in a 202, and its
// errors used to collapse into one 500. Prefer [AcceptFunc], which returns an
// [Acceptance]; a Handler with both set uses the AcceptFunc.
type TriggerFunc func(ctx context.Context, crewID, agentID string, payload WebhookPayload) error

// MaxBodyBytes is the ingress body cap from the webhook contract §6 ("Max body
// 1 MiB"). It is enforced with [http.MaxBytesReader], not io.LimitReader: a
// LimitReader stops at the limit and reports success, so an oversized body used
// to be silently TRUNCATED and then handed to HMAC verification, where it
// failed as a signature mismatch. The limit applies whether or not the sender
// declared a Content-Length, because MaxBytesReader counts bytes actually read.
const MaxBodyBytes = 1 << 20

// IngressRetryAfterSeconds is the Retry-After sent with a 429. §5 fixes it at 5.
const IngressRetryAfterSeconds = "5"

// The acceptance path's outcomes that are not "accepted". An AcceptFunc (or a
// TriggerFunc) returns one of these, wrapped or bare, and ServeHTTP maps it to
// the status §5 requires. Anything else is an unexpected fault and stays a 500.
//
// They exist because the handler used to map EVERY trigger error to 500, so the
// per-agent rate and concurrency gates below it could refuse a delivery and the
// sender would be told the server was broken — no Retry-After, no reason to
// retry sooner, and nothing to distinguish it from a real fault.
var (
	// ErrIngressFull is "the queue is full, come back": 429 + Retry-After.
	// §5 adds that a duplicate of ALREADY accepted work is not refused for
	// fullness, so this must be returned only for genuinely new work.
	ErrIngressFull = errors.New("webhook: ingress capacity full")
	// ErrDeliveryConflict is the same source delivery id arriving with a
	// different body: 409, and the original record is kept.
	ErrDeliveryConflict = errors.New("webhook: delivery conflict")
	// ErrUnavailable is the database or the policy being unreachable, or the
	// acceptance budget being exceeded: 503. Never a false 202.
	ErrUnavailable = errors.New("webhook: acceptance unavailable")
	// ErrEndpointUnknown is an unknown or disabled endpoint: 404.
	ErrEndpointUnknown = errors.New("webhook: unknown or disabled endpoint")
	// ErrMalformed is a body that verified but could not be understood: 400.
	ErrMalformed = errors.New("webhook: malformed delivery")
)

// Inbound is one authenticated delivery handed to an [AcceptFunc]. It carries
// the RAW bytes as well as the parsed payload, because the delivery ledger
// records a hash of the exact bytes that were signed and a re-serialised
// payload would hash differently.
type Inbound struct {
	// Payload is the decoded body, for the ordinary agent-webhook shape.
	Payload WebhookPayload
	// RawBody is the exact bytes read off the wire.
	RawBody []byte
	// Header is the request's header set, for the profile identifiers that
	// live outside the body (delivery id, event type).
	Header http.Header
}

// Acceptance is what the durable acceptance path recorded. It is returned to
// the sender as §5's receipt; it is an identifier, not a capability to read the
// work behind it.
type Acceptance struct {
	// DeliveryID and WorkID are the ledger's identifiers. WorkID is empty when
	// the delivery was recorded but filtered out.
	DeliveryID string
	WorkID     string
	// State is the work's CURRENT state, so a re-delivery answers with what
	// actually happened rather than repeating "queued" forever. Empty means
	// "queued".
	State string
	// Duplicate marks a re-delivery inside the retention window: the ids are
	// the ORIGINAL delivery's, and no second attempt was created.
	Duplicate bool
	// Ignored marks a valid ping or an event the endpoint's filter dropped.
	// The answer is 200 with a safe reason and no agent.
	Ignored bool
	// Reason is the safe, machine-readable filter reason for an Ignored
	// delivery. It never contains payload text or key material.
	Reason string
}

// AcceptFunc records a delivery and the work it produces, durably, and returns
// the receipt. Everything it does must be database work: §5's I1 is that no 202
// is written before the commit, and the corollary is that nothing outside the
// database — no container warm, no goroutine, no run record — may happen before
// the response.
type AcceptFunc func(ctx context.Context, crewID, agentID string, in Inbound) (Acceptance, error)

// DefaultTimestampTolerance bounds how far an X-Timestamp may be from now for a
// timestamped signature to be accepted. Matches Stripe's default (5 min) — wide
// enough to absorb clock skew, narrow enough that a captured signed webhook is
// useless once the window passes.
const DefaultTimestampTolerance = 5 * time.Minute

// Handler is an HTTP handler that validates incoming webhook requests using
// a shared secret and triggers agent runs on success.
type Handler struct {
	logger       *slog.Logger
	lookupSecret SecretLookup
	trigger      TriggerFunc
	// requireTimestamp, when set, decides per (crew, agent) whether the
	// timestamped signature scheme is mandatory. nil = never required.
	requireTimestamp RequireTimestampLookup
	// tolerance / now back the optional timestamped-signature replay defense.
	// now is a seam for tests; nil means time.Now.
	tolerance time.Duration
	now       func() time.Time
	// accept is the durable acceptance path. When set it supersedes trigger:
	// the handler records the delivery and answers §5's receipt, and the
	// caller does no work outside the database before that answer.
	accept AcceptFunc
}

// SetAcceptFunc wires the durable acceptance path. With it set, ServeHTTP
// answers §5's receipt shape ({delivery_id, work_id, status, duplicate}) and
// the TriggerFunc is not called. Returns the receiver for chaining.
func (h *Handler) SetAcceptFunc(f AcceptFunc) *Handler {
	h.accept = f
	return h
}

// statusForAcceptError maps an acceptance failure onto §5's status table.
//
// The default is deliberately 500 and not 503: an error nobody classified is a
// fault in this server, and saying 503 would invite a retry storm against a
// server that is not going to behave differently on the next attempt.
func statusForAcceptError(err error) int {
	switch {
	case errors.Is(err, ErrIngressFull):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrDeliveryConflict):
		return http.StatusConflict
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrEndpointUnknown):
		return http.StatusNotFound
	case errors.Is(err, ErrMalformed):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// bodyForAcceptError is the safe text sent with each mapped status. It never
// echoes the error, which may carry an agent id, a query or a driver message.
func bodyForAcceptError(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "ingress capacity full"
	case http.StatusConflict:
		return "delivery_conflict"
	case http.StatusServiceUnavailable:
		return "acceptance unavailable"
	case http.StatusNotFound:
		return "not found"
	case http.StatusBadRequest:
		return "bad request"
	default:
		return "internal error"
	}
}

// SetRequireTimestampLookup wires the optional per-agent require-timestamp
// policy. Without it every agent keeps the backward-compatible default
// (timestamp optional). Returns the receiver for chaining.
func (h *Handler) SetRequireTimestampLookup(f RequireTimestampLookup) *Handler {
	h.requireTimestamp = f
	return h
}

// NewHandler creates a webhook Handler with the given secret lookup and trigger functions.
func NewHandler(logger *slog.Logger, lookup SecretLookup, trigger TriggerFunc) *Handler {
	return &Handler{
		logger:       logger,
		lookupSecret: lookup,
		trigger:      trigger,
		tolerance:    DefaultTimestampTolerance,
		now:          time.Now,
	}
}

// timestampFresh reports whether the unix-seconds timestamp string is within
// tolerance of now. Empty/unparseable → not fresh.
func (h *Handler) timestampFresh(ts string) bool {
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	tol := h.tolerance
	if tol <= 0 {
		tol = DefaultTimestampTolerance
	}
	// Compare in seconds against non-overflowing bounds rather than via
	// time.Sub: an absurd far-future/far-past X-Timestamp (e.g. math.MaxInt64)
	// would overflow the int64-nanosecond Duration (~292y max) and could wrap
	// back inside the tolerance, letting a replay defeat the freshness check.
	// nowSec±tolSec can't overflow (now ~1.7e9, tol ~300s); secs is only
	// compared, never used in arithmetic.
	tolSec := int64(tol / time.Second)
	nowSec := now().Unix()
	return secs >= nowSec-tolSec && secs <= nowSec+tolSec
}

// ServeHTTP handles incoming webhook POST requests by validating the
// X-Webhook-Secret header, parsing the JSON body, and triggering the agent run.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	crewID := r.PathValue("crewId")
	agentID := r.PathValue("agentId")
	if crewID == "" || agentID == "" {
		http.Error(w, "missing team or agent ID", http.StatusBadRequest)
		return
	}

	// Two auth shapes accepted (in order of preference):
	//
	//   X-Signature        hex HMAC-SHA256 of the raw body using the agent's
	//                      shared secret as the HMAC key. This is the same
	//                      shape the pipeline-webhook route uses, and it's
	//                      the model issue #537 wants the agent route to
	//                      converge on — body integrity is covered and a
	//                      leaked log-line capture of the header is useless
	//                      without the body it signed.
	//
	//   X-Webhook-Secret   plaintext shared secret. Pre-HMAC contract;
	//                      kept for one release as a deprecation window so
	//                      external systems already firing webhooks don't
	//                      break at upgrade. Emits a Deprecation response
	//                      header so callers can see they're on the old
	//                      shape. After the deprecation window we'll
	//                      remove this branch and the plain secret will
	//                      get rejected.
	providedSig := r.Header.Get("X-Signature")
	providedSecret := r.Header.Get("X-Webhook-Secret")
	if providedSig == "" && providedSecret == "" {
		h.logger.Warn("webhook missing signature/secret", "crew_id", crewID, "agent_id", agentID)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	expectedSecret, err := h.lookupSecret(r.Context(), crewID, agentID)
	if err != nil {
		h.logger.Error("webhook secret lookup failed", "error", err, "crew_id", crewID, "agent_id", agentID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// §5: an oversized body is 413, and no work. It used to be
	// io.LimitReader(r.Body, 1<<20), which does not fail — it stops at the
	// limit and reports EOF, so a 2 MiB delivery was silently TRUNCATED to its
	// first mebibyte, then failed HMAC verification and came back as a 401
	// "unauthorized". The sender was told its signature was wrong when the
	// truth was that we refused to read its request.
	//
	// MaxBytesReader errors instead, and counts bytes actually read, so the cap
	// holds for a chunked upload that declares no Content-Length. It also marks
	// the connection so the server does not try to reuse it after we answer
	// early.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.logger.Warn("webhook body over the ingress limit",
				"crew_id", crewID, "agent_id", agentID, "limit_bytes", MaxBodyBytes)
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Per-agent policy: does this agent mandate the timestamped scheme? When it
	// does, the two replayable shapes (body-only HMAC, plaintext secret) are
	// rejected below.
	//
	// A lookup error fails CLOSED. It used to fail open — "a transient DB blip
	// can't 400 every delivery" — but that reasoning trades the wrong thing
	// away: a policy that cannot be read is not a policy that says "optional".
	// Under a failing lookup an agent configured to REQUIRE timestamps silently
	// dropped back to accepting an indefinitely replayable body-only HMAC, so
	// anyone who could make the lookup fail could also downgrade the scheme.
	// §5 states it directly: "Chyba lookupu není vypnutá policy", and a
	// database that cannot answer is the 503 row of the response table.
	requireTS := false
	if h.requireTimestamp != nil {
		req, lookErr := h.requireTimestamp(r.Context(), crewID, agentID)
		if lookErr != nil {
			h.logger.Error("webhook require-timestamp lookup failed; refusing the delivery",
				"crew_id", crewID, "agent_id", agentID, "error", lookErr)
			w.Header().Set("Retry-After", IngressRetryAfterSeconds)
			http.Error(w, "policy unavailable", http.StatusServiceUnavailable)
			return
		}
		requireTS = req
	}

	// Auth check after body read so HMAC validation has the bytes it signs.
	switch {
	case providedSig != "":
		// Optional replay defense (Stripe/Svix scheme): when the sender includes
		// X-Timestamp, the signature must cover "timestamp.body" AND the
		// timestamp must be fresh — so a captured signed webhook is useless once
		// the tolerance window passes, and the timestamp itself can't be swapped
		// (it's in the signed material). Absent X-Timestamp, fall back to the
		// body-only HMAC for backward compatibility (senders not yet migrated),
		// UNLESS this agent requires the timestamped scheme.
		if ts := r.Header.Get("X-Timestamp"); ts != "" {
			if !h.timestampFresh(ts) {
				h.logger.Warn("webhook stale or invalid timestamp", "crew_id", crewID, "agent_id", agentID)
				http.Error(w, "stale or invalid timestamp", http.StatusBadRequest)
				return
			}
			signed := append([]byte(ts+"."), body...)
			if !ValidateHMAC(signed, providedSig, expectedSecret) {
				h.logger.Warn("webhook invalid HMAC signature (timestamped)", "crew_id", crewID, "agent_id", agentID)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		} else if requireTS {
			// Body-only HMAC is replayable indefinitely; this agent opted into
			// mandatory timestamping. Reject with a clear pointer at the scheme.
			h.logger.Warn("webhook body-only signature rejected; agent requires timestamped signatures", "crew_id", crewID, "agent_id", agentID)
			http.Error(w, "this agent requires a timestamped signature: send X-Timestamp and sign \"<timestamp>.<body>\"", http.StatusBadRequest)
			return
		} else if !ValidateHMAC(body, providedSig, expectedSecret) {
			h.logger.Warn("webhook invalid HMAC signature", "crew_id", crewID, "agent_id", agentID)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	case providedSecret != "":
		if requireTS {
			// The deprecated plaintext path carries no timestamp and is the most
			// replayable shape of all — refuse it outright when timestamping is
			// mandatory.
			h.logger.Warn("webhook plaintext secret rejected; agent requires timestamped signatures", "crew_id", crewID, "agent_id", agentID)
			http.Error(w, "this agent requires a timestamped signature: use X-Signature over \"<timestamp>.<body>\" with X-Timestamp", http.StatusBadRequest)
			return
		}
		if !ValidateSecret(providedSecret, expectedSecret) {
			h.logger.Warn("webhook invalid secret", "crew_id", crewID, "agent_id", agentID)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Deprecation", `version="X-Webhook-Secret"`)
		w.Header().Set("Sunset", "Thu, 31 Dec 2026 23:59:59 GMT")
		h.logger.Warn("webhook accepted on deprecated X-Webhook-Secret path; migrate to X-Signature (HMAC-SHA256 of body)",
			"crew_id", crewID, "agent_id", agentID)
	}

	var payload WebhookPayload
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	}
	payload.RecvAt = time.Now().UTC()

	if h.accept != nil {
		h.serveAcceptance(w, r, crewID, agentID, body, payload)
		return
	}

	if err := h.trigger(r.Context(), crewID, agentID, payload); err != nil {
		// Not every trigger failure is a fault. The per-agent rate and
		// concurrency gates below this call refuse a delivery on purpose, and
		// collapsing that into a 500 told the sender the server was broken and
		// gave it no Retry-After to act on.
		status := statusForAcceptError(err)
		if status == http.StatusInternalServerError {
			h.logger.Error("webhook trigger failed", "error", err, "crew_id", crewID, "agent_id", agentID)
		} else {
			h.logger.Warn("webhook refused", "error", err, "status", status, "crew_id", crewID, "agent_id", agentID)
		}
		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", IngressRetryAfterSeconds)
		}
		http.Error(w, bodyForAcceptError(status), status)
		return
	}

	h.logger.Info("webhook triggered", "crew_id", crewID, "agent_id", agentID, "event", payload.Event, "source", payload.Source)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "accepted",
	})
}

// serveAcceptance runs the durable acceptance path and writes §5's response.
//
// The ORDER is the contract. The delivery and the work it produces are
// committed before a single byte of the 202 is written (I1), and nothing
// outside the database runs before that: the container warm and the run
// goroutine that used to sit between the request and its answer now happen
// after this function returns, on the caller's side of the AcceptFunc.
func (h *Handler) serveAcceptance(w http.ResponseWriter, r *http.Request, crewID, agentID string, body []byte, payload WebhookPayload) {
	acc, err := h.accept(r.Context(), crewID, agentID, Inbound{
		Payload: payload,
		RawBody: body,
		Header:  r.Header,
	})
	if err != nil {
		status := statusForAcceptError(err)
		if status == http.StatusInternalServerError {
			h.logger.Error("webhook acceptance failed", "error", err, "crew_id", crewID, "agent_id", agentID)
		} else {
			h.logger.Warn("webhook not accepted", "error", err, "status", status,
				"crew_id", crewID, "agent_id", agentID)
		}
		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", IngressRetryAfterSeconds)
		}
		http.Error(w, bodyForAcceptError(status), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// A valid ping, or an event this endpoint's filter dropped: 200, a safe
	// reason, and no agent. The delivery is still recorded — §5 requires the
	// filter decision to be auditable rather than invisible.
	if acc.Ignored {
		w.WriteHeader(http.StatusOK)
		out := map[string]any{"status": "ignored", "delivery_id": acc.DeliveryID}
		if acc.Reason != "" {
			out["reason"] = acc.Reason
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}

	state := acc.State
	if state == "" {
		state = "queued"
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"delivery_id": acc.DeliveryID,
		"work_id":     acc.WorkID,
		"status":      state,
		"duplicate":   acc.Duplicate,
	})
	h.logger.Info("webhook accepted", "crew_id", crewID, "agent_id", agentID,
		"delivery_id", acc.DeliveryID, "work_id", acc.WorkID, "duplicate", acc.Duplicate)
}
