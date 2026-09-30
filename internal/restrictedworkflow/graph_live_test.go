//go:build linux && restrictedruntime_live

package restrictedworkflow

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// This acceptance path uses the production worker image and real durable broker
// accounting. The only upstream is a synthetic TLS server; no paid request runs.
func TestLiveNestedGraphProductionTextWorker(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	if image == "" {
		t.Skip("owned production worker image required")
	}
	s, runner := graphFixture(t)
	for _, q := range []string{
		`UPDATE agents SET llm_model='gpt-5-mini' WHERE id IN ('agent','other')`,
		`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('graph-live-cap','w','workspace','w','month',100,'hard')`,
	} {
		if _, err := s.db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	calls := map[string]int{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "gpt-5-mini" {
			t.Error("invalid frozen request")
			w.WriteHeader(403)
			return
		}
		user := "h1"
		if strings.Contains(body.Input, "h2_PRIVATE_CANARY") {
			user = "h2"
		}
		other := "h2"
		if user == "h2" {
			other = "h1"
		}
		if strings.Contains(body.Input, other+"_PRIVATE_CANARY") || strings.Contains(body.Input, "answer-"+other) {
			t.Error("foreign context reached real worker")
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch key {
		case "synthetic-workflow-key", "synthetic-independent-workflow-key":
		default:
			t.Error("unapproved payer")
		}
		if key == "synthetic-independent-workflow-key" && !strings.Contains(body.Input, "answer-"+user) {
			t.Error("B missing classified parent output")
		}
		var pending int
		if err := s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM restricted_cost_reservations WHERE state='pending'`).Scan(&pending); err != nil || pending < 1 {
			t.Error("upstream preceded durable reservation")
		}
		mu.Lock()
		calls[user+":"+key]++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", "answer-"+user)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":20,\"output_tokens\":5,\"total_tokens\":25,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
	}))
	defer upstream.Close()
	manager, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), restrictedruntime.Docker{Image: image}, runner.Authority, runner.Authority, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
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
	runner.StartSession = nil
	runner.Manager = manager
	receipts := map[string]Receipt{}
	for _, user := range []string{"h1", "h2"} {
		receipts[user], err = s.AdmitManual(t.Context(), user, "w", "private-work", map[string]any{"task": user + "_PRIVATE_CANARY"}, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if worked, e := s.DispatchNext(t.Context()); !worked || e != nil {
			t.Fatal("real graph failed", worked, e)
		}
		result, e := s.Result(t.Context(), user, "w", receipts[user].ID)
		if e != nil || result.State != "completed" || result.Outputs["return"] != "answer-"+user {
			t.Fatalf("missing own real result: %+v %v", result, e)
		}
	}
	mu.Lock()
	for _, user := range []string{"h1", "h2"} {
		if calls[user+":synthetic-workflow-key"] != 2 || calls[user+":synthetic-independent-workflow-key"] != 1 {
			t.Error("independent provider slots not used", calls)
		}
	}
	mu.Unlock()
	var settled int
	if err = s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_cost_reservations WHERE state='known'`).Scan(&settled); err != nil || settled != 6 {
		t.Fatal("six leaf costs not settled", settled, err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE access_attempts SET revoked_at='2026-09-30T13:00:00Z' WHERE principal_id='h1' AND agent_id='other' AND completed_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Result(t.Context(), "h1", "w", receipts["h1"].ID); err == nil {
		t.Fatal("revoked B output remained readable")
	}
	if _, err = s.Result(t.Context(), "h2", "w", receipts["h2"].ID); err != nil {
		t.Fatal("H1 source revoke affected H2", err)
	}
}
