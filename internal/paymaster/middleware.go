package paymaster

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// CallRequest is the per-call input that any LLM-shaped middleware sees. The
// shape is deliberately minimal so we can stack telemetry, lookout, caching,
// and paymaster around the same Caller without each layer needing to know
// about the others. Provider/Model are required so the budget pre-check can
// price-estimate when a layer above us wants to short-circuit.
type CallRequest struct {
	Scope    Scope
	Provider string
	Model    string

	// Inputs is opaque to paymaster — it's whatever payload the underlying
	// Caller wants (messages, prompts, tool definitions). Kept as `any`
	// because every provider SDK uses a different request type.
	Inputs any

	// EstimatedInputTokens lets a caller hint at the post-tokenization size
	// before the call. Optional; used only for finer-grained pre-check
	// estimates and ignored if zero. Final ledger row uses CallResponse.
	EstimatedInputTokens int64

	// BillingMode tells Enforce which kind of budget rules apply. Default
	// (empty) is treated as BillingMetered for backwards-compat. Flat-rate
	// requests skip $ enforcement; quota enforcement runs after the call
	// based on response headers (see EnforceQuota).
	BillingMode BillingMode

	// SubscriptionPlan is the human label persisted on the ledger row when
	// BillingMode is BillingFlatRate. Empty for metered.
	SubscriptionPlan string
}

// CallResponse is what the underlying Caller returns. Token counts are
// provider-reported; CostUSD may be zero (middleware will fill it via
// Estimate before recording).
type CallResponse struct {
	Output              any
	InputTokens         int64
	OutputTokens        int64
	CachedInputTokens   int64
	CacheCreationTokens int64
	CostUSD             float64 // may be 0; middleware estimates if so
	Tags                map[string]any
	CompletedAt         time.Time

	// Confidence labels how trustworthy CostUSD is. Empty defaults to
	// ConfidenceEstimate when middleware fills cost via Estimate, or
	// ConfidencePrecise when the underlying Caller computed it from a
	// provider-reported usage block.
	Confidence CostConfidence

	// QuotaRemainingPct (0.0–1.0) carries the live remaining-quota signal
	// that the underlying Caller pulled from response headers. Zero means
	// no header was returned (Google, OAuth tunnels, transport errors).
	QuotaRemainingPct float64

	// QuotaWindow names which axis QuotaRemainingPct refers to. Empty when
	// no header signal is present.
	QuotaWindow QuotaWindow
}

// LLMCaller is the interface every layer in the call stack implements. The
// signature is intentionally identical across paymaster / telemetry / caching
// so layers compose by wrapping. Embeddings and other non-completion calls
// can implement it too — paymaster doesn't care what the call was, only what
// it cost.
type LLMCaller interface {
	Call(ctx context.Context, req CallRequest) (CallResponse, error)
}

// CallerFunc is the func adapter so trivial wrappers don't need a struct.
// Useful in tests — see paymaster_test.go for examples.
type CallerFunc func(ctx context.Context, req CallRequest) (CallResponse, error)

// Call satisfies LLMCaller for CallerFunc.
func (f CallerFunc) Call(ctx context.Context, req CallRequest) (CallResponse, error) {
	return f(ctx, req)
}

// Middleware wraps next with the paymaster control plane:
//
//  1. before — Enforce; if a hard budget is exceeded the call is rejected
//     and never reaches the underlying Caller. The error propagates as a
//     *BudgetExceededError so callers can render a friendly message.
//  2. call — invoke next.Call; record the response timestamp if the caller
//     left it zero so the ledger TS reflects when the round-trip ended.
//  3. after — fill CostUSD via Estimate when the upstream didn't price the
//     call itself (most providers don't bill per-call inline), then Record
//     the ledger row + journal entries.
//
// The middleware does NOT block on Record errors — billing failures bubble
// up the same way provider errors do, so the caller can decide whether to
// retry or surface the error. The provider's response is returned regardless
// because the work has already been done and refusing it would also waste
// the budget.
func Middleware(next LLMCaller, j journal.Emitter, db *sql.DB) LLMCaller {
	return CallerFunc(func(ctx context.Context, req CallRequest) (CallResponse, error) {
		// Flat-rate requests bypass $ enforcement. The subscription is already
		// paid for; the only meaningful "stop" signal is provider quota
		// exhaustion (handled post-call via EnforceQuota when the upstream
		// returns a 429 + rate-limit headers).
		if req.BillingMode != BillingFlatRate {
			if err := Enforce(ctx, db, j, req.Scope); err != nil {
				// Pass BudgetExceededError through unwrapped so callers can errors.As it.
				var bx *BudgetExceededError
				if errors.As(err, &bx) {
					return CallResponse{}, err
				}
				// Other check errors (DB unreachable, etc.) are also fatal — we
				// fail closed because the alternative is uncapped spending.
				return CallResponse{}, err
			}
		}

		resp, callErr := next.Call(ctx, req)
		if callErr != nil {
			// Even on failure, attempt to record what we know — providers bill
			// for partial completions and we want the row in the ledger so the
			// operator sees the cost trail. Token counts default to zero if
			// the provider didn't return anything useful.
			recordErr := recordFromResponse(ctx, db, j, req, resp)
			if recordErr != nil {
				// Don't shadow the call error with a logging error. The audit
				// gap is unfortunate but secondary to the actual failure.
				_ = recordErr
			}
			return resp, callErr
		}

		if err := recordFromResponse(ctx, db, j, req, resp); err != nil {
			return resp, err
		}
		return resp, nil
	})
}

// recordFromResponse builds the Call from CallRequest + CallResponse and
// hands it to Record. Centralised so both the success and the failure paths
// in Middleware use identical logic — partial billing on failure should look
// the same as full billing on success, just with smaller numbers.
func recordFromResponse(ctx context.Context, db *sql.DB, j journal.Emitter, req CallRequest, resp CallResponse) error {
	cost := resp.CostUSD
	confidence := resp.Confidence
	if cost == 0 && req.BillingMode != BillingFlatRate {
		// Backfill via the rate card and label as estimate — caller didn't
		// pre-compute, so we can't claim precision.
		cost = Estimate(req.Provider, req.Model,
			resp.InputTokens, resp.OutputTokens,
			resp.CachedInputTokens, resp.CacheCreationTokens)
		if confidence == "" {
			confidence = ConfidenceEstimate
		}
	}

	ts := resp.CompletedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	_, err := Record(ctx, db, j, Call{
		Scope:               req.Scope,
		Provider:            req.Provider,
		Model:               req.Model,
		InputTokens:         resp.InputTokens,
		OutputTokens:        resp.OutputTokens,
		CachedInputTokens:   resp.CachedInputTokens,
		CacheCreationTokens: resp.CacheCreationTokens,
		CostUSD:             cost,
		Tags:                resp.Tags,
		TS:                  ts,
		BillingMode:         req.BillingMode,
		Confidence:          confidence,
		SubscriptionPlan:    req.SubscriptionPlan,
		QuotaRemainingPct:   resp.QuotaRemainingPct,
		QuotaWindow:         resp.QuotaWindow,
	})
	return err
}
