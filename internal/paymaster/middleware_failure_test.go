package paymaster

import (
	"context"
	"errors"
	"testing"
)

func TestMiddlewareDatabaseFailurePreventsProviderCall(t *testing.T) {
	db := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	middleware := Middleware(CallerFunc(func(context.Context, CallRequest) (CallResponse, error) {
		called = true
		return CallResponse{}, nil
	}), nil, db)
	_, err := middleware.Call(t.Context(), CallRequest{Scope: Scope{WorkspaceID: "ws1"}, Provider: "openai", Model: "gpt-5-mini"})
	if err == nil || called {
		t.Fatalf("database failure must deny upstream spending: err=%v called=%v", err, called)
	}
}

// Uncapped requests still record provider usage on a partial failure. If that
// post-call write also fails, callers must retain the original provider cause.
func TestMiddlewarePartialFailureRecordsUsageAndPreservesCause(t *testing.T) {
	for _, failLedger := range []bool{false, true} {
		t.Run(map[bool]string{false: "record partial usage", true: "ledger also fails"}[failLedger], func(t *testing.T) {
			db, _ := reservationDB(t)
			providerErr := errors.New("provider stopped after partial completion")
			middleware := Middleware(CallerFunc(func(context.Context, CallRequest) (CallResponse, error) {
				if failLedger {
					if _, err := db.Exec(`CREATE TRIGGER reject_cost BEFORE INSERT ON cost_ledger BEGIN SELECT RAISE(ABORT, 'ledger unavailable'); END`); err != nil {
						t.Fatal(err)
					}
				}
				return CallResponse{InputTokens: 80, OutputTokens: 20}, providerErr
			}), nil, db)
			resp, err := middleware.Call(t.Context(), CallRequest{Scope: reservationRequest().Scope, Provider: "openai", Model: "gpt-5-mini"})
			if !errors.Is(err, providerErr) {
				t.Fatalf("provider cause lost: %v", err)
			}
			if resp.CostUSD <= 0 || resp.Confidence != ConfidenceEstimate || resp.CompletedAt.IsZero() {
				t.Fatalf("partial usage was not costed: %+v", resp)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM cost_ledger`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if failLedger {
				if count != 0 {
					t.Fatalf("failed write persisted %d rows", count)
				}
				return
			}
			var input, output int64
			var cost float64
			if err := db.QueryRow(`SELECT input_tokens, output_tokens, cost_usd FROM cost_ledger`).Scan(&input, &output, &cost); err != nil {
				t.Fatal(err)
			}
			if count != 1 || input != 80 || output != 20 || cost != resp.CostUSD {
				t.Fatalf("partial charge missing: rows=%d input=%d output=%d cost=%g", count, input, output, cost)
			}
		})
	}
}

func TestMiddlewareSuccessfulProviderReportsLedgerFailure(t *testing.T) {
	db, _ := reservationDB(t)
	middleware := Middleware(CallerFunc(func(context.Context, CallRequest) (CallResponse, error) {
		if _, err := db.Exec(`CREATE TRIGGER reject_cost BEFORE INSERT ON cost_ledger BEGIN SELECT RAISE(ABORT, 'ledger unavailable'); END`); err != nil {
			t.Fatal(err)
		}
		return CallResponse{Output: "completed", InputTokens: 10, OutputTokens: 20, CostUSD: .005}, nil
	}), nil, db)
	resp, err := middleware.Call(t.Context(), CallRequest{Scope: reservationRequest().Scope, Provider: "openai", Model: "gpt-5-mini"})
	if err == nil || resp.Output != "completed" || resp.CostUSD != .005 || resp.Confidence != ConfidencePrecise {
		t.Fatalf("provider success must retain output and surface audit failure: response=%+v err=%v", resp, err)
	}
}

func TestMiddlewareSubscriptionDoesNotDebitExhaustedDollarBudget(t *testing.T) {
	db, _ := reservationDB(t)
	scope := reservationRequest().Scope
	if _, err := Record(t.Context(), db, nil, Call{Scope: scope, Provider: "openai", Model: "gpt-5-mini", CostUSD: .01}); err != nil {
		t.Fatal(err)
	}
	reservationCap(t, db, .001)
	called := false
	middleware := Middleware(CallerFunc(func(ctx context.Context, _ CallRequest) (CallResponse, error) {
		called = true
		if ReservationActive(ctx) {
			t.Error("subscription acquired a metered reservation")
		}
		return CallResponse{InputTokens: 10, OutputTokens: 20, CostUSD: 15, Confidence: ConfidencePrecise}, nil
	}), nil, db)
	resp, err := middleware.Call(t.Context(), CallRequest{Scope: scope, Provider: "openai", Model: "gpt-5-mini", BillingMode: BillingFlatRate, SubscriptionPlan: "ChatGPT Plus"})
	if err != nil || !called || resp.CostUSD != 0 || resp.Confidence != ConfidenceUnknown {
		t.Fatalf("subscription treated as metered spending: called=%v response=%+v err=%v", called, resp, err)
	}
	var cost float64
	var input, output int64
	var plan string
	if err := db.QueryRow(`SELECT cost_usd,input_tokens,output_tokens,subscription_plan FROM cost_ledger WHERE billing_mode='flat_rate'`).Scan(&cost, &input, &output, &plan); err != nil {
		t.Fatal(err)
	}
	if cost != 0 || input != 10 || output != 20 || plan != "ChatGPT Plus" {
		t.Fatalf("subscription usage misreported: cost=%g input=%d output=%d plan=%q", cost, input, output, plan)
	}
}
