package llmproxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestTokenPool_SelectToken_RoundRobin(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "tok1", Status: StatusActive},
		{ID: "c2", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "tok2", Status: StatusActive},
		{ID: "c3", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "tok3", Status: StatusActive},
	})

	tokens := make([]string, 3)
	for i := range tokens {
		conn := pool.SelectToken("org1", ProviderAnthropic)
		if conn == nil {
			t.Fatal("expected connection, got nil")
		}
		tokens[i] = conn.AccessToken
	}

	if tokens[0] != "tok1" || tokens[1] != "tok2" || tokens[2] != "tok3" {
		t.Errorf("expected round-robin tok1,tok2,tok3, got %v", tokens)
	}
}

func TestTokenPool_SelectToken_SkipsInactive(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "tok1", Status: StatusExpired},
		{ID: "c2", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "tok2", Status: StatusActive},
	})

	conn := pool.SelectToken("org1", ProviderAnthropic)
	if conn == nil {
		t.Fatal("expected connection, got nil")
	}
	if conn.AccessToken != "tok2" {
		t.Errorf("expected tok2, got %s", conn.AccessToken)
	}
}

func TestTokenPool_SelectToken_NoActive(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "tok1", Status: StatusExpired},
	})

	conn := pool.SelectToken("org1", ProviderAnthropic)
	if conn != nil {
		t.Errorf("expected nil, got %+v", conn)
	}
}

func TestTokenPool_SelectToken_FiltersByOrg(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "org1-tok", Status: StatusActive},
		{ID: "c2", OrgID: "org2", Provider: ProviderAnthropic, AccessToken: "org2-tok", Status: StatusActive},
	})

	conn := pool.SelectToken("org2", ProviderAnthropic)
	if conn == nil || conn.AccessToken != "org2-tok" {
		t.Errorf("expected org2-tok, got %v", conn)
	}
}

func TestTokenPool_MarkStatus(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "tok1", Status: StatusActive},
	})

	pool.MarkStatus("c1", StatusRateLimited)

	conn := pool.SelectToken("org1", ProviderAnthropic)
	if conn != nil {
		t.Errorf("expected nil after marking rate limited, got %+v", conn)
	}

	if count := pool.ActiveCount("org1", ProviderAnthropic); count != 0 {
		t.Errorf("expected 0 active, got %d", count)
	}
}

func TestTokenPool_ActiveCount(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", OrgID: "org1", Provider: ProviderAnthropic, Status: StatusActive},
		{ID: "c2", OrgID: "org1", Provider: ProviderAnthropic, Status: StatusActive},
		{ID: "c3", OrgID: "org1", Provider: ProviderAnthropic, Status: StatusExpired},
	})

	if count := pool.ActiveCount("org1", ProviderAnthropic); count != 2 {
		t.Errorf("expected 2 active, got %d", count)
	}
}

func TestTokenSyncer_Sync(t *testing.T) {
	connections := []ProviderConnection{
		{ID: "c1", OrgID: "org1", Provider: ProviderAnthropic, AccessToken: "test-token", Status: StatusActive},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/credentials" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Internal-Token") != "test-secret" {
			t.Error("missing internal token")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(connections)
	}))
	defer srv.Close()

	pool := NewTokenPool(testLogger())
	syncer := NewTokenSyncer(pool, srv.URL, "test-secret", time.Hour, testLogger())

	if err := syncer.SyncNow(context.Background()); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	conn := pool.SelectToken("org1", ProviderAnthropic)
	if conn == nil || conn.AccessToken != "test-token" {
		t.Errorf("expected test-token after sync, got %v", conn)
	}
}

func TestTokenSyncer_SyncError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	pool := NewTokenPool(testLogger())
	syncer := NewTokenSyncer(pool, srv.URL, "test-secret", time.Hour, testLogger())

	err := syncer.SyncNow(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
