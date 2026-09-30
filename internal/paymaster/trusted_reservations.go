package paymaster

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// CallBounds is supplied by a host provider adapter which has proved the
// official metered endpoint, model context and exact output ceiling. It is not
// an estimate and must not be constructed from worker request metadata.
type CallBounds struct{ MaxInputTokens, MaxOutputTokens int64 }

func trustedReservation(ctx context.Context, db *sql.DB, req CallRequest) (Reservation, bool, error) {
	if db == nil {
		return Reservation{}, false, ErrReservationDenied
	}
	budgets, e := loadApplicableBudgets(ctx, db, req.Scope)
	if e != nil {
		return Reservation{}, false, e
	}
	capped := false
	for _, budget := range budgets {
		if budget.Mode == ModeHard || budget.Mode == ModeTiered {
			capped = true
			break
		}
	}
	if !capped {
		return Reservation{}, false, nil
	}
	if req.Bounds == nil {
		return Reservation{}, false, ErrReservationDenied
	}
	provider, model := strings.ToLower(req.Provider), strings.ToLower(req.Model)
	m, ok := modelcatalog.Default().Lookup(provider, model)
	if !ok {
		return Reservation{}, false, ErrReservationDenied
	}
	rate, ok := trustedCeilingRates(m, req.Bounds.MaxInputTokens)
	if !ok {
		return Reservation{}, false, ErrReservationDenied
	}
	callID := newLedgerID()
	reservation, e := reserve(ctx, db, ReservationRequest{Scope: req.Scope, PrincipalID: "trusted-host", AttemptID: callID, CredentialID: "", Provider: provider, Model: model, MaxInputTokens: req.Bounds.MaxInputTokens, MaxOutputTokens: req.Bounds.MaxOutputTokens}, reservationPolicy{maxOutput: 2 << 20, rate: &rate})
	return reservation, e == nil, e
}

func settleTrusted(ctx context.Context, db *sql.DB, j journal.Emitter, req CallRequest, resp CallResponse, callErr error, reservation Reservation) (CallResponse, error) {
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	total := resp.InputTokens
	valid := resp.InputTokens >= 0 && resp.CachedInputTokens >= 0 && resp.CacheCreationTokens >= 0 && resp.InputTokens <= math.MaxInt64-resp.CachedInputTokens
	if valid {
		total += resp.CachedInputTokens
		valid = total <= math.MaxInt64-resp.CacheCreationTokens
		if valid {
			total += resp.CacheCreationTokens
		}
	}
	usage := ReservationUsage{Known: callErr == nil && resp.UsageKnown && valid, Conservative: true, InputTokens: total, OutputTokens: resp.OutputTokens, CachedInputTokens: resp.CachedInputTokens}
	settlementErr := Settle(clean, db, reservation.ID, usage)
	var cost float64
	var confidence, state string
	readErr := db.QueryRowContext(clean, `SELECT l.cost_usd,l.cost_confidence,r.state FROM cost_ledger l JOIN restricted_cost_reservations r ON r.ledger_id=l.id WHERE l.id=?`, reservation.ID).Scan(&cost, &confidence, &state)
	if readErr == nil && state == "known" {
		// The shared settlement API takes total input (OpenAI wire semantics).
		// Trusted legacy ledger/report fields instead keep fresh input separate
		// from cache reads and writes; correct the channels without another debit.
		_, readErr = db.ExecContext(clean, `UPDATE cost_ledger SET input_tokens=?,cache_creation_tokens=? WHERE id=?`, resp.InputTokens, resp.CacheCreationTokens, reservation.ID)
	}
	if readErr == nil {
		resp.CostUSD = cost
		resp.Confidence = CostConfidence(confidence)
	}
	if resp.CompletedAt.IsZero() {
		resp.CompletedAt = time.Now().UTC()
	}
	if readErr == nil && j != nil {
		call := Call{Scope: req.Scope, Provider: req.Provider, Model: req.Model, InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens, CachedInputTokens: resp.CachedInputTokens, CacheCreationTokens: resp.CacheCreationTokens, CostUSD: resp.CostUSD, Confidence: resp.Confidence, TS: resp.CompletedAt, BillingMode: BillingMetered, Tags: resp.Tags}
		rec := CostRecord{ID: reservation.ID, TS: resp.CompletedAt, Cost: cost}
		emitLLMCall(clean, j, call, rec)
		if cost > 0 {
			emitCostIncurred(clean, j, call, rec)
		}
	}
	if callErr != nil {
		return resp, callErr
	}
	return resp, errors.Join(settlementErr, readErr)
}

type reservationContextKey struct{}

// ReservationActive identifies a single prepaid upstream attempt. Provider
// codecs must disable transparent network retries while this marker is set.
func ReservationActive(ctx context.Context) bool {
	active, _ := ctx.Value(reservationContextKey{}).(bool)
	return active
}

func trustedCeilingRates(model modelcatalog.Model, maxInput int64) (modelPrice, bool) {
	if maxInput < 1 || maxInput > 2<<20 {
		return modelPrice{}, false
	}
	contexts := []int64{0}
	if model.Cost != nil {
		for _, tier := range model.Cost.Tiers {
			if tier.Tier.Type != "context" || tier.Tier.Size < 0 {
				return modelPrice{}, false
			}
			if tier.Tier.Size < maxInput {
				contexts = append(contexts, tier.Tier.Size+1)
			}
		}
	}
	var ceiling modelPrice
	for _, contextSize := range contexts {
		in, out, cache, write, ok := model.RatesAt(contextSize)
		if !ok || !positiveFinite(in) || !positiveFinite(out) || !finite(cache) || cache < 0 || !finite(write) || write < 0 {
			return modelPrice{}, false
		}
		ceiling.InputPerM = math.Max(ceiling.InputPerM, in)
		ceiling.OutputPerM = math.Max(ceiling.OutputPerM, out)
		ceiling.CachedInputPerM = math.Max(ceiling.CachedInputPerM, cache)
		ceiling.CacheWritePerM = math.Max(ceiling.CacheWritePerM, write)
	}
	// All input channels must fit the input debit even if the expensive rate
	// belongs to a different tier than the maximum output/cache-read rate.
	ceiling.InputPerM = math.Max(ceiling.InputPerM, math.Max(ceiling.CacheWritePerM, ceiling.CachedInputPerM))
	return ceiling, true
}
