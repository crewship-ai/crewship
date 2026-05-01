package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestFetchTopSpenders_Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/paymaster/top-spenders") {
			t.Errorf("path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "5" {
			t.Errorf("limit: %s", got)
		}
		if got := r.URL.Query().Get("range"); got != "24h" {
			t.Errorf("range: %s", got)
		}
		_, _ = w.Write([]byte(`{"rows":[
			{"scope_kind":"agent","scope_id":"a1","cost_usd":1.23,"call_count":10},
			{"scope_kind":"crew","scope_id":"c1","cost_usd":0.45,"call_count":5}
		]}`))
	}))
	defer srv.Close()

	rows, err := fetchTopSpenders(cli.NewClient(srv.URL, "t", ""), "24h", 5)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len: %d", len(rows))
	}
	if rows[0].ScopeKind != "agent" || rows[0].CostUSD != 1.23 {
		t.Errorf("row 0: %+v", rows[0])
	}
}

func TestFetchCrewSpend_Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/paymaster/spend/by-crew" {
			t.Errorf("path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"rows":[
			{"crew_id":"backend","cost_usd":3.21,"call_count":20,"input_tokens":1000,"output_tokens":500}
		]}`))
	}))
	defer srv.Close()

	rows, err := fetchCrewSpend(cli.NewClient(srv.URL, "t", ""), "7d")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len: %d", len(rows))
	}
	if rows[0].CrewID != "backend" || rows[0].InTokens != 1000 || rows[0].OutTokens != 500 {
		t.Errorf("row: %+v", rows[0])
	}
}

func TestFetchSubscriptionUsage_HandlesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rows":[]}`))
	}))
	defer srv.Close()

	rows, err := fetchSubscriptionUsage(cli.NewClient(srv.URL, "t", ""), "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected empty, got %d", len(rows))
	}
}

func TestFetchSubscriptionUsage_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"plan check failed"}`, http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := fetchSubscriptionUsage(cli.NewClient(srv.URL, "t", ""), "")
	if err == nil {
		t.Fatal("expected error")
	}
}
