package paymaster

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

func TestTrustedAndRestrictedShareAtomicHardCap(t *testing.T) {
	db, path := reservationDB(t)
	reservationCap(t, db, .003)
	other, e := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	request := reservationRequest()
	// Hold trusted work in-flight if it wins. Neither provider invocation nor a
	// restricted admission may rely on a process-local enforcement mutex.
	var group sync.WaitGroup
	start := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, 2)
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		middleware := Middleware(CallerFunc(func(ctx context.Context, _ CallRequest) (CallResponse, error) {
			if !ReservationActive(ctx) {
				return CallResponse{}, errors.New("retry suppression missing")
			}
			<-release
			return CallResponse{}, nil
		}), nil, db)
		_, e := middleware.Call(context.Background(), CallRequest{Scope: request.Scope, Provider: request.Provider, Model: request.Model, Bounds: &CallBounds{1000, 1000}})
		results <- e
	}()
	go func() {
		defer group.Done()
		<-start
		_, e := Reserve(context.Background(), other, request)
		results <- e
		close(release)
	}()
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("admitted %d calls against one-call shared budget", successes)
	}
	var rows int
	var cost float64
	if e = db.QueryRow(`SELECT COUNT(*),SUM(cost_usd) FROM cost_ledger`).Scan(&rows, &cost); e != nil || rows != 1 || cost > .003 || cost <= 0 {
		t.Fatalf("rows=%d cost=%v error=%v", rows, cost, e)
	}
}
func TestTrustedReservationUnknownErrorsAndKnownUsage(t *testing.T) {
	for _, kind := range []string{"missing", "error", "cancel", "negative", "negative-cache", "negative-cache-write", "cache-sum-overflow", "cache-write-sum-overflow", "overbound", "known"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := reservationDB(t)
			reservationCap(t, db, 1)
			request := reservationRequest()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			middleware := Middleware(CallerFunc(func(context.Context, CallRequest) (CallResponse, error) {
				response := CallResponse{InputTokens: 10, OutputTokens: 20, CachedInputTokens: 5, UsageKnown: true}
				switch kind {
				case "missing":
					response = CallResponse{}
				case "error":
					return response, errors.New("provider failed after charge")
				case "cancel":
					cancel()
					return response, context.Canceled
				case "negative":
					response.InputTokens = -1
				case "negative-cache":
					response.CachedInputTokens = -1
				case "negative-cache-write":
					response.CacheCreationTokens = -1
				case "cache-sum-overflow":
					response.InputTokens = math.MaxInt64
				case "cache-write-sum-overflow":
					response.InputTokens = 0
					response.CachedInputTokens = math.MaxInt64
					response.CacheCreationTokens = 1
				case "overbound":
					response.OutputTokens = 1001
				}
				return response, nil
			}), nil, db)
			response, callErr := middleware.Call(ctx, CallRequest{Scope: request.Scope, Provider: request.Provider, Model: request.Model, Bounds: &CallBounds{1000, 1000}})
			if (kind == "error" || kind == "cancel") != (callErr != nil) {
				t.Fatalf("call error %v", callErr)
			}
			var state string
			var cost float64
			var count int
			if e := db.QueryRow(`SELECT state,reserved_usd FROM restricted_cost_reservations`).Scan(&state, &cost); e != nil {
				t.Fatal(e)
			}
			if e := db.QueryRow(`SELECT COUNT(*) FROM cost_ledger`).Scan(&count); e != nil || count != 1 {
				t.Fatal("duplicate ledger row", e, count)
			}
			if kind == "known" {
				if state != "known" || !(response.CostUSD > 0 && response.CostUSD < cost) || response.Confidence != ConfidenceEstimate {
					t.Fatalf("known settlement state=%s response=%+v max=%v", state, response, cost)
				}
			} else if state != "unknown" || math.Abs(response.CostUSD-cost) > 1e-12 {
				t.Fatalf("released unknown usage state=%s cost=%v max=%v", state, response.CostUSD, cost)
			}
		})
	}
}
func TestTrustedHardCapsRejectUnprovedBoundsAndPreserveUncapped(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncapped", true: "hard"}[capped], func(t *testing.T) {
			db, _ := reservationDB(t)
			if capped {
				reservationCap(t, db, 1)
			}
			called := false
			middleware := Middleware(CallerFunc(func(context.Context, CallRequest) (CallResponse, error) { called = true; return CallResponse{}, nil }), nil, db)
			_, e := middleware.Call(t.Context(), CallRequest{Scope: Scope{WorkspaceID: "ws"}, Provider: "custom", Model: "unknown"})
			if capped {
				if called || !errors.Is(e, ErrReservationDenied) {
					t.Fatalf("unproved hard-cap call called=%v error=%v", called, e)
				}
			} else if !called || e != nil {
				t.Fatalf("uncapped behavior changed called=%v error=%v", called, e)
			}
		})
	}
}

func TestTrustedCeilingsCoverEveryChannelAcrossTiers(t *testing.T) {
	read, write := 8.0, 9.0
	model := modelcatalog.Model{Cost: &modelcatalog.Cost{Input: 1, Output: 12, CacheRead: &read, CacheWrite: &write, Tiers: []modelcatalog.CostTier{{Input: 5, Output: 3, Tier: modelcatalog.TierBound{Type: "context", Size: 1000}}}}}
	ceiling, ok := trustedCeilingRates(model, 2000)
	if !ok || ceiling.InputPerM != 9 || ceiling.OutputPerM != 12 || ceiling.CachedInputPerM != 8 || ceiling.CacheWritePerM != 9 {
		t.Fatalf("channel ceiling=%+v valid=%v", ceiling, ok)
	}
	model.Cost.Tiers[0].Output = math.Inf(1)
	if _, ok = trustedCeilingRates(model, 2000); ok {
		t.Fatal("unbounded rate accepted")
	}
}

func TestTrustedCeilingsRejectUnprovablePricing(t *testing.T) {
	negative := -1.0
	for _, tc := range []struct {
		name     string
		cost     *modelcatalog.Cost
		maxInput int64
	}{
		{"missing rates", nil, 1000},
		{"zero context bound", &modelcatalog.Cost{Input: 1, Output: 2}, 0},
		{"excessive context bound", &modelcatalog.Cost{Input: 1, Output: 2}, (2 << 20) + 1},
		{"unknown tier axis", &modelcatalog.Cost{Input: 1, Output: 2, Tiers: []modelcatalog.CostTier{{Input: 3, Output: 4, Tier: modelcatalog.TierBound{Type: "other", Size: 500}}}}, 1000},
		{"negative tier threshold", &modelcatalog.Cost{Input: 1, Output: 2, Tiers: []modelcatalog.CostTier{{Input: 3, Output: 4, Tier: modelcatalog.TierBound{Type: "context", Size: -1}}}}, 1000},
		{"zero input rate", &modelcatalog.Cost{Input: 0, Output: 2}, 1000},
		{"negative output rate", &modelcatalog.Cost{Input: 1, Output: -1}, 1000},
		{"NaN rate", &modelcatalog.Cost{Input: math.NaN(), Output: 2}, 1000},
		{"negative cache read", &modelcatalog.Cost{Input: 1, Output: 2, CacheRead: &negative}, 1000},
		{"negative cache creation", &modelcatalog.Cost{Input: 1, Output: 2, CacheWrite: &negative}, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ceiling, ok := trustedCeilingRates(modelcatalog.Model{Cost: tc.cost}, tc.maxInput); ok {
				t.Fatalf("hard budget accepted an unprovable ceiling: %+v", ceiling)
			}
		})
	}
}
