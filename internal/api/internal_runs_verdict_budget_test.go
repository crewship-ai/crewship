package api

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

// The property this file exists for (#1615): the per-call budget an operator
// sets on the run_summary slot — Judge models card, or `crewship keeper aux set
// run_summary --timeout` — is the deadline the post-run verdict's model call
// actually runs under.
//
// It was storage only, exactly as keeper_aux_settings.timeout_ms had been for
// the four Keeper Reviews evaluators before #1601: settable, validated,
// rendered with its provenance next to four neighbours where the same control
// worked. Router.RunVerdict resolved the slot and then dropped the budget on
// the floor, and both production call sites hand the verdict a background
// context — so the number bounded nothing at all.
//
// This is the end-to-end half: the wiring the server actually performs
// (SetRunVerdict(router.RunVerdict)), a real terminating run through UpdateRun,
// and an assertion on the deadline the provider is called with. Nothing here
// dials anything; "ollama" is the provider under test only because
// llm.BuildAuxProviderAt can build it with no API key present.

// deadlineRecordingProvider is a stubVerdictProvider that also records the
// deadline (if any) on the context its Complete was called with — the only
// place a caller-imposed budget is observable from inside the call.
type deadlineRecordingProvider struct {
	stubVerdictProvider
	remaining   time.Duration // ctx budget seen by Complete; 0 = no deadline
	hadDeadline bool
}

func (p *deadlineRecordingProvider) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if dl, ok := ctx.Deadline(); ok {
		p.hadDeadline = true
		p.remaining = time.Until(dl)
	}
	return p.stubVerdictProvider.Complete(ctx, req)
}

// pinAuxProvider makes the Router hand back p for the run_summary slot without
// building a real client, by seeding the cache with the fingerprint the wiring
// under test resolves to. Keeping the real resolution path (rather than
// injecting a resolver) is the point: the budget has to survive a CACHE HIT,
// which is the branch where a provider-shaped memoisation pins the timeout.
func pinAuxProvider(cache *auxProviderCache, provider, model string, p llm.Provider) {
	// Mirrors auxProvider's key: provider|model|ollama-endpoint|credential.
	// A drift here shows up as the fake never being called, which every test
	// below asserts on.
	cache.fpr = provider + "|" + model + "|" + "" + "|" + ""
	cache.provider = p
	cache.model = model
}

func TestUpdateRun_BoundsTheVerdictCallByTheRunSummaryBudget(t *testing.T) {
	cases := []struct {
		name string
		// slotTimeout is the run_summary slot's inherited (config/env) budget.
		slotTimeout time.Duration
		// overrideMS, when non-zero, is what the operator sets on the slot at
		// runtime — the keeper_aux_settings row the card writes.
		overrideMS int64
		want       time.Duration
	}{
		{
			name:        "the slot's configured budget bounds the call",
			slotTimeout: 4 * time.Second,
			want:        4 * time.Second,
		},
		{
			name:        "a different configured budget, so the number is read and not a constant",
			slotTimeout: 9 * time.Second,
			want:        9 * time.Second,
		},
		{
			name:        "the operator's runtime override wins over the inherited value",
			slotTimeout: 4 * time.Second,
			overrideMS:  7000,
			want:        7 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			logger := newTestLogger()

			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a-budget', ?, 'Bot', 'bot', 'IDLE')`, wsID); err != nil {
				t.Fatalf("insert agent: %v", err)
			}

			r := auxRouterFor(t, llm.AuxiliaryModels{
				RunSummary: llm.AuxModel{Provider: "ollama", Model: "m", Timeout: tc.slotTimeout},
			})
			if tc.overrideMS != 0 {
				ms := tc.overrideMS
				if _, err := r.KeeperAuxSettings().Apply(context.Background(), "run_summary",
					keepercfg.AuxPatch{TimeoutMS: &ms}, ""); err != nil {
					t.Fatalf("apply run_summary budget: %v", err)
				}
			}
			provider := &deadlineRecordingProvider{stubVerdictProvider: stubVerdictProvider{content: stubVerdictJSON}}
			pinAuxProvider(&r.runVerdictCache, "ollama", "m", provider)

			h := NewInternalHandler(db, "test-token", logger)
			_ = wireTestJournalForHandler(t, db, h)
			// The production wiring, verbatim (router_internal.go): the method,
			// not its result.
			h.SetRunVerdict(r.RunVerdict)

			createAndCompleteRun(t, h, db, wsID, "a-budget", "run-budget-1", "COMPLETED")

			if provider.calls != 1 {
				t.Fatalf("provider.Complete calls = %d, want 1 (the pinned provider was not the one used)", provider.calls)
			}
			if !provider.hadDeadline {
				t.Fatalf("the verdict call ran with NO deadline; want the run_summary slot's %s — "+
					"the operator's budget reached nothing", tc.want)
			}
			// The deadline is set immediately before Complete, so the remaining
			// budget is the configured one minus test scheduling noise.
			if provider.remaining > tc.want || provider.remaining < tc.want/2 {
				t.Errorf("verdict call deadline = %s remaining, want ~%s (the run_summary budget)",
					provider.remaining, tc.want)
			}
		})
	}
}
