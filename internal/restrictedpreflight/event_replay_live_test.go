//go:build linux && restrictedruntime_live

package restrictedpreflight

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// This fixed replay compares an intentionally forced model poll on every tick
// with the real durable preflight. Both execute the production worker against
// one synthetic provider protocol. It is not a fleet or paid-provider benchmark.
func TestLivePreflightSameEventReplay(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	if image == "" {
		t.Skip("owned production worker image required")
	}
	events := []string{"missing", "projectless", "i2", "i1", "i1", "i1", "missing", "i1"}
	for _, mode := range []string{"forced-model-poll", "durable-preflight"} {
		t.Run(mode, func(t *testing.T) {
			s, e := fixture(t)
			if _, err := s.DB.ExecContext(t.Context(), `UPDATE agents SET llm_model='gpt-5-mini' WHERE id='agent'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('poll-chat','w','agent','h1','private')`); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			calls := 0
			var firstWire time.Time
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "gpt-5-mini" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("incorrect frozen provider request")
					w.WriteHeader(403)
					return
				}
				var pending int
				if err := s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM restricted_cost_reservations WHERE state='pending'`).Scan(&pending); err != nil || pending < 1 {
					t.Error("request without prior hard-budget reservation")
				}
				mu.Lock()
				calls++
				firstWire = time.Now()
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"private answer\"}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":20,\"output_tokens\":5,\"total_tokens\":25,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
			}))
			defer upstream.Close()
			manager, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), restrictedruntime.Docker{Image: image}, e.runner.Authority, e.runner.Authority, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if err = manager.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = restrictedruntime.InstallAcceptanceTLS(manager, upstream); err != nil {
				t.Fatal(err)
			}
			e.runner.StartSession = nil
			e.runner.Manager = manager
			emit := func(string, string) error { return nil }
			var receiptID string
			emptyWakes, duplicateWakes := 0, 0
			var readyToWire time.Duration
			for i, issue := range events {
				ready := time.Now()
				if mode == "forced-model-poll" {
					if err = e.runner.ExecuteRun(t.Context(), "h1", "w", "poll-chat", "Check for currently executable assigned work.", emit); err != nil {
						t.Fatal(err)
					}
					if issue != "i1" {
						emptyWakes++
					} else if i != 3 {
						duplicateWakes++
					}
				} else {
					receipt, claimErr := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", issue, "issue-work")
					if issue != "i1" {
						if !errors.Is(claimErr, ErrNoWork) {
							t.Fatalf("empty/foreign event admitted: %s %v", issue, claimErr)
						}
						continue
					}
					if claimErr != nil {
						t.Fatal(claimErr)
					}
					if receiptID == "" {
						receiptID = receipt.ID
					} else if receipt.ID != receiptID {
						t.Fatal("duplicate obtained a different authority")
					}
					if i == 3 {
						if worked, dispatchErr := s.Workflow.DispatchNext(t.Context()); !worked || dispatchErr != nil {
							t.Fatalf("dispatch: %v %v", worked, dispatchErr)
						}
					}
				}
				if i == 3 {
					mu.Lock()
					readyToWire = firstWire.Sub(ready)
					mu.Unlock()
				}
			}
			var tokens, known int
			if err = s.DB.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(input_tokens+output_tokens),0),COUNT(*) FROM restricted_cost_reservations WHERE state='known'`).Scan(&tokens, &known); err != nil {
				t.Fatal(err)
			}
			wantCalls := len(events)
			if mode == "durable-preflight" {
				wantCalls = 1
				if count(t, s.DB, "restricted_workflow_jobs") != 1 || count(t, s.DB, "restricted_preflight_reservations") != 1 || e.calls != 1 {
					t.Fatal("durable work owner duplicated")
				}
			}
			mu.Lock()
			gotCalls := calls
			mu.Unlock()
			if gotCalls != wantCalls || known != wantCalls || tokens != 25*wantCalls || readyToWire <= 0 {
				t.Fatalf("replay accounting: calls%d known%d tokens%d wait%s", gotCalls, known, tokens, readyToWire)
			}
			t.Logf("same_event_sample=%d model_calls=%d empty_wakes=%d duplicate_wakes=%d settled_tokens=%d ready_to_provider=%s", len(events), gotCalls, emptyWakes, duplicateWakes, tokens, readyToWire)
		})
	}
}
