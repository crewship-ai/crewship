//go:build linux && restrictedruntime_live

package restricteddispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// This acceptance runs the actual production responses worker, migrated
// application authority and host accounting against owned synthetic TLS data.
// It neither changes live data nor connects to a paid provider.
func TestLiveProductionWorkerDurableAccounting(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	if os.Getenv("CREWSHIP_RESTRICTED_LIVE") != "1" || image == "" {
		t.Fatal("run owned acceptance with pinned CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	}
	a := providerFixture(t)
	if _, err := a.Store.DB.Exec(`UPDATE agents SET llm_model='gpt-5-mini' WHERE id='a'; INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('worker-cap','w','workspace','w','month',.02,'hard')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	m, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), restrictedruntime.Docker{Image: image}, a, a, restrictedruntime.Limits{MemoryBytes: 64 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	var unknown atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "gpt-5-mini" || body["max_output_tokens"] != float64(128) || body["store"] != false || body["stream"] != true || r.Header.Get("Authorization") != "Bearer synthetic-pinned-key" || r.Header.Get("OpenAI-Project") != "" || r.Header.Get("ChatGPT-Account-ID") != "" {
			t.Error("worker/provider pinned contract lost")
		}
		var pending int
		if err := a.Store.DB.QueryRow(`SELECT COUNT(*) FROM restricted_cost_reservations WHERE state='pending'`).Scan(&pending); err != nil || pending != 1 {
			t.Errorf("upstream without durable debit: %d %v", pending, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"LIVE_WORKER_ACCOUNTED\"}\n\n")
		w.(http.Flusher).Flush()
		if calls.Load() == 1 {
			started <- struct{}{}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if unknown.Load() {
			io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5-mini\",\"status\":\"completed\"}}\n\n")
		} else {
			io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5-mini\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n")
		}
	}))
	t.Cleanup(server.Close)
	if err = restrictedruntime.InstallAcceptanceTLS(m, server); err != nil {
		t.Fatal(err)
	}
	start := func(user, chat string) (string, *restrictedruntime.Session) {
		h, _, err := a.PrepareResponses(ctx, user, "w", "a", chat, "", nil, 128, func(ctx context.Context, attempt access.Attempt) ([]string, error) {
			prompt, err := a.Store.BuildContext(ctx, attempt, "owned-accounting-fixture")
			if err != nil {
				return nil, err
			}
			body, err := json.Marshal(map[string]any{"model": "gpt-5-mini", "instructions": prompt.System, "input": prompt.Input, "max_output_tokens": 128, "store": false, "stream": true})
			return []string{"/opt/crewship-runner", "responses", string(body)}, err
		})
		if err != nil {
			t.Fatal(err)
		}
		session, err := m.Start(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		return h, session
	}
	finish := func(s *restrictedruntime.Session) {
		select {
		case <-s.Done():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	h1, first := start("h1", "c1")
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var principal, payer string
	if err = a.Store.DB.QueryRow(`SELECT r.principal_id,r.credential_id FROM restricted_cost_reservations r WHERE state='pending'`).Scan(&principal, &payer); err != nil || principal != "h1" || payer != "key" {
		t.Fatalf("untrusted payer %s %s %v", principal, payer, err)
	}
	_, parallel := start("h2", "c2")
	finish(parallel)
	if calls.Load() != 1 {
		t.Fatal("parallel production workers overspent shared reservation")
	}
	close(gate)
	finish(first)
	out, err := first.Output(ctx)
	if err != nil || !strings.Contains(out, "LIVE_WORKER_ACCOUNTED") || !strings.Contains(out, `"done"`) {
		t.Fatalf("production worker positive output missing %q %v", out, err)
	}
	var state string
	var debit float64
	if err = a.Store.DB.QueryRow(`SELECT r.state,l.cost_usd FROM restricted_cost_reservations r JOIN cost_ledger l ON l.id=r.ledger_id`).Scan(&state, &debit); err != nil || state != "known" || debit >= .001 {
		t.Fatalf("terminal usage did not settle %s %f %v", state, debit, err)
	}
	if err = a.Store.CompleteAttempt(ctx, h1); err != nil {
		t.Fatal(err)
	}
	unknown.Store(true)
	_, third := start("h2", "c2")
	finish(third)
	if calls.Load() != 2 {
		t.Fatal("known settlement did not permit fresh admission")
	}
	_, fourth := start("h1", "c1")
	finish(fourth)
	if calls.Load() != 2 {
		t.Fatal("missing provider usage refunded production debit")
	}
	if err = a.Store.DB.QueryRow(`SELECT state FROM restricted_cost_reservations WHERE state='unknown'`).Scan(&state); err != nil {
		t.Fatal("unknown usage not durable", err)
	}
	t.Log("production responses worker: real Docker H1/H2, scoped frozen request, synthetic host-only payer, shared concurrent cap, known usage settlement and unknown full debit; no paid call")
}
