package paymaster

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ReservationRequest comes exclusively from resolved server authority. Scope
// budgets accumulate across attempts, retries, principals and delegated agents.
// MaxInputTokens must be a conservative upper bound, not a chars/4 estimate.
type ReservationRequest struct {
	Scope                                                 Scope
	PrincipalID, AttemptID, CredentialID, Provider, Model string
	MaxInputTokens, MaxOutputTokens                       int64
}

type Reservation struct{ ID string }

// ReservationUsage is accepted only from a validated terminal provider event.
// Unknown usage (including transport failure) charges the entire reservation.
type ReservationUsage struct {
	Known                                        bool
	Conservative                                 bool
	InputTokens, OutputTokens, CachedInputTokens int64
}

var ErrReservationDenied = errors.New("paymaster: reservation denied")

// Reserve durably pre-debits the maximum cost before any upstream request.
// BEGIN IMMEDIATE owns SQLite's write lock before reading shared budgets, so
// concurrent connections and processes cannot admit against the same balance.
// An enabled workspace hard/tiered budget is mandatory for restricted traffic.
// The ledger debit is never automatically refunded, including after restart.
func Reserve(ctx context.Context, db *sql.DB, r ReservationRequest) (Reservation, error) {
	return reserve(ctx, db, r, reservationPolicy{requireWorkspace: true, maxOutput: 32768})
}

type reservationPolicy struct {
	requireWorkspace bool
	maxOutput        int64
	rate             *modelPrice
}

func reserve(ctx context.Context, db *sql.DB, r ReservationRequest, policy reservationPolicy) (Reservation, error) {
	missingRestrictedIdentity := policy.requireWorkspace && (r.Scope.AgentID == "" || r.CredentialID == "")
	if db == nil || r.Scope.WorkspaceID == "" || missingRestrictedIdentity || r.PrincipalID == "" || r.AttemptID == "" || r.MaxInputTokens < 1 || r.MaxInputTokens > 2<<20 || r.MaxOutputTokens < 1 || r.MaxOutputTokens > policy.maxOutput {
		return Reservation{}, ErrReservationDenied
	}
	// A fallback rate cannot prove a maximum for an unknown model.
	provider, model := strings.ToLower(r.Provider), strings.ToLower(r.Model)
	rate, ok := priceTable[provider+"/"+model]
	if !ok {
		rate, ok = catalogPrice(provider, model)
	}
	if policy.rate != nil {
		rate, ok = *policy.rate, true
	}
	if !ok || !positiveFinite(rate.InputPerM) || !positiveFinite(rate.OutputPerM) || rate.CachedInputPerM < 0 || !finite(rate.CachedInputPerM) || rate.CachedInputPerM > rate.InputPerM {
		return Reservation{}, ErrReservationDenied
	}
	cost := float64(r.MaxInputTokens)*rate.InputPerM/1e6 + float64(r.MaxOutputTokens)*rate.OutputPerM/1e6
	if !positiveFinite(cost) {
		return Reservation{}, ErrReservationDenied
	}
	conn, err := beginReservation(ctx, db)
	if err != nil {
		return Reservation{}, err
	}
	defer conn.Close()
	defer rollbackReservation(conn)
	budgets, err := loadApplicableBudgets(ctx, conn, r.Scope)
	if err != nil {
		return Reservation{}, err
	}
	now := time.Now().UTC()
	capped := false
	for _, b := range budgets {
		if b.Mode == ModeSoft {
			continue
		}
		if (b.Mode != ModeHard && b.Mode != ModeTiered) || !positiveFinite(b.LimitUSD) {
			return Reservation{}, ErrReservationDenied
		}
		if !policy.requireWorkspace || b.ScopeKind == ScopeWorkspace {
			capped = true
		}
		spent, err := conservativeSumSpend(ctx, conn, b, r.Scope, now)
		if err != nil {
			return Reservation{}, err
		}
		if !finite(spent) || spent < 0 || spent+cost > b.LimitUSD {
			return Reservation{}, &BudgetExceededError{Statuses: []BudgetStatus{{Budget: b, SpentUSD: spent + cost, LimitUSD: b.LimitUSD, State: StateExceeded}}}
		}
	}
	if !capped {
		return Reservation{}, ErrReservationDenied
	}
	id := newLedgerID()
	tags := `{"restricted_reservation":true}`
	if !policy.requireWorkspace {
		tags = `{"trusted_reservation":true}`
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO cost_ledger(id,workspace_id,crew_id,agent_id,mission_id,ts,provider,model,cost_usd,tags,billing_mode,cost_confidence,credential_id,rate_input_per_m,rate_output_per_m,rate_cached_in_per_m,rate_cache_write_per_m) VALUES(?,?,?,?,?,?,?,?,?,?,'metered','estimate',?,?,?,?,?)`, id, r.Scope.WorkspaceID, nullable(r.Scope.CrewID), nullable(r.Scope.AgentID), nullable(r.Scope.MissionID), tsformat.Format(now), provider, model, cost, tags, nullable(r.CredentialID), rate.InputPerM, rate.OutputPerM, rate.CachedInputPerM, rate.CacheWritePerM)
	if err != nil {
		return Reservation{}, err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO restricted_cost_reservations(id,ledger_id,principal_id,attempt_id,credential_id,max_input_tokens,max_output_tokens,reserved_usd,rate_input_per_m,rate_output_per_m,rate_cached_per_m) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, id, r.PrincipalID, r.AttemptID, r.CredentialID, r.MaxInputTokens, r.MaxOutputTokens, cost, rate.InputPerM, rate.OutputPerM, rate.CachedInputPerM)
	if err != nil {
		return Reservation{}, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Reservation{}, err
	}
	return Reservation{ID: id}, nil
}

// Settle replaces the pre-debit exactly once using the reservation's frozen
// rate snapshot. Unknown, malformed or over-bound usage preserves the debit.
// Repeated identical settlement succeeds; conflicting settlement fails closed.
func Settle(ctx context.Context, db *sql.DB, id string, usage ReservationUsage) error {
	if db == nil || id == "" {
		return ErrReservationDenied
	}
	conn, err := beginReservation(ctx, db)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer rollbackReservation(conn)
	var ledger, state string
	var maxIn, maxOut int64
	var reserved, inRate, outRate, cacheRate float64
	var oldIn, oldOut, oldCache sql.NullInt64
	err = conn.QueryRowContext(ctx, `SELECT ledger_id,state,max_input_tokens,max_output_tokens,reserved_usd,rate_input_per_m,rate_output_per_m,rate_cached_per_m,input_tokens,output_tokens,cached_input_tokens FROM restricted_cost_reservations WHERE id=?`, id).Scan(&ledger, &state, &maxIn, &maxOut, &reserved, &inRate, &outRate, &cacheRate, &oldIn, &oldOut, &oldCache)
	if err != nil {
		return err
	}
	valid := usage.Known && usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.CachedInputTokens >= 0 && usage.CachedInputTokens <= usage.InputTokens && usage.InputTokens <= maxIn && usage.OutputTokens <= maxOut
	want := "unknown"
	cost := reserved
	if valid {
		want = "known"
		cost = float64(usage.InputTokens-usage.CachedInputTokens)*inRate/1e6 + float64(usage.CachedInputTokens)*cacheRate/1e6 + float64(usage.OutputTokens)*outRate/1e6
		if !finite(cost) || cost < 0 || cost > reserved {
			valid = false
			want = "unknown"
			cost = reserved
		}
	}
	if state != "pending" {
		if state == want && (!valid || (oldIn.Valid && oldOut.Valid && oldCache.Valid && oldIn.Int64 == usage.InputTokens && oldOut.Int64 == usage.OutputTokens && oldCache.Int64 == usage.CachedInputTokens)) {
			return nil
		}
		return ErrReservationDenied
	}
	var in, out, cache any
	confidence := ConfidenceEstimate
	if valid {
		in = usage.InputTokens
		out = usage.OutputTokens
		cache = usage.CachedInputTokens
		confidence = ConfidencePrecise
		if usage.Conservative {
			confidence = ConfidenceEstimate
		}
	}
	_, err = conn.ExecContext(ctx, `UPDATE restricted_cost_reservations SET state=?,input_tokens=?,output_tokens=?,cached_input_tokens=? WHERE id=? AND state='pending'`, want, in, out, cache, id)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `UPDATE cost_ledger SET cost_usd=?,cost_confidence=?,input_tokens=?,output_tokens=?,cached_input_tokens=? WHERE id=?`, cost, confidence, usageInput(valid, usage.InputTokens), usageInput(valid, usage.OutputTokens), usageInput(valid, usage.CachedInputTokens), ledger)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func usageInput(valid bool, n int64) int64 {
	if valid {
		return n
	}
	return 0
}
func finite(n float64) bool         { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func positiveFinite(n float64) bool { return finite(n) && n > 0 }
func beginReservation(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("paymaster: reservation transaction: %w", err)
	}
	return conn, nil
}
func rollbackReservation(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
}
