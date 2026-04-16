package llmproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripFunc lets us redirect the monitor's outbound calls (which target
// api.anthropic.com) at our test server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// rerouteAnthropic redirects api.anthropic.com requests to a test server.
func rerouteAnthropic(srv *httptest.Server) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "api.anthropic.com") {
			r.URL.Scheme = "http"
			r.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		}
		return http.DefaultTransport.RoundTrip(r)
	})
}

// TestCredentialMonitor_ValidateAnthropic_AllStatusCodes covers the response
// matrix: 200=ACTIVE, 401=EXPIRED, 403=REVOKED, 429=RATE_LIMITED, other=ERROR.
func TestCredentialMonitor_ValidateAnthropic_AllStatusCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code int
		want ConnectionStatus
	}{
		{http.StatusOK, StatusActive},
		{http.StatusUnauthorized, StatusExpired},
		{http.StatusForbidden, StatusRevoked},
		{http.StatusTooManyRequests, StatusRateLimited},
		{http.StatusBadGateway, StatusError},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()

			cm := NewCredentialMonitor(NewTokenPool(testLogger()),
				"http://nextjs", "tok", time.Hour, testLogger())
			cm.client = &http.Client{Transport: rerouteAnthropic(srv), Timeout: 5 * time.Second}

			got, _ := cm.validateAnthropic(context.Background(), ProviderConnection{
				ID: "c", Provider: ProviderAnthropic, AccessToken: "k", Status: StatusActive,
			})
			if got != tc.want {
				t.Errorf("status %d → %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// TestCredentialMonitor_ValidateAnthropic_OAuthHeader: tokens prefixed with
// sk-ant-oat must use Bearer auth instead of x-api-key.
func TestCredentialMonitor_ValidateAnthropic_OAuthHeader(t *testing.T) {
	t.Parallel()
	var seenAuth, seenAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenAPIKey = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cm := NewCredentialMonitor(NewTokenPool(testLogger()),
		"http://nextjs", "tok", time.Hour, testLogger())
	cm.client = &http.Client{Transport: rerouteAnthropic(srv)}

	_, _ = cm.validateAnthropic(context.Background(), ProviderConnection{
		ID: "c", Provider: ProviderAnthropic, AccessToken: "sk-ant-oat-secret",
	})

	if !strings.HasPrefix(seenAuth, "Bearer ") {
		t.Errorf("expected Bearer auth, got %q", seenAuth)
	}
	if seenAPIKey != "" {
		t.Errorf("expected x-api-key empty, got %q", seenAPIKey)
	}
}

// TestCredentialMonitor_CheckAll_SkipsRevokedAndOAuth tests the early-exit
// branches of checkAll: REVOKED creds and AI_CLI_TOKEN creds aren't validated.
func TestCredentialMonitor_CheckAll_SkipsRevokedAndOAuth(t *testing.T) {
	t.Parallel()
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "rev", WorkspaceID: "w", Provider: ProviderAnthropic, AccessToken: "k", Status: StatusRevoked},
		{ID: "oat", WorkspaceID: "w", Provider: ProviderAnthropic, AccessToken: "sk-ant-oat-x", Type: TypeAICLIToken, Status: StatusActive},
		{ID: "ok", WorkspaceID: "w", Provider: ProviderAnthropic, AccessToken: "sk-ant-x", Status: StatusActive},
	})

	cm := NewCredentialMonitor(pool, "http://nextjs", "tok", time.Hour, testLogger())
	cm.client = &http.Client{Transport: rerouteAnthropic(upstream)}

	cm.checkAll(context.Background())
	// Only the API-key entry hits the upstream once; revoked + OAuth are skipped.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 upstream call, got %d", got)
	}
}

// TestCredentialMonitor_CheckAll_ResetsExpiredOAuth: an OAuth token marked
// EXPIRED by an earlier (incorrect) check must be auto-restored to ACTIVE.
func TestCredentialMonitor_CheckAll_ResetsExpiredOAuth(t *testing.T) {
	t.Parallel()
	persistOk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer persistOk.Close()

	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "x", WorkspaceID: "w", Provider: ProviderAnthropic,
			AccessToken: "sk-ant-oat-broken", Status: StatusExpired},
	})

	cm := NewCredentialMonitor(pool, persistOk.URL, "tok", time.Hour, testLogger())
	cm.checkAll(context.Background())

	all := pool.AllConnections()
	if len(all) != 1 || all[0].Status != StatusActive {
		t.Errorf("expected reset to ACTIVE, got %+v", all)
	}
}

// TestCredentialMonitor_CheckOne_FiresOnChange validates the full status
// transition path: checkOne(...) → MarkStatus + persistStatus + onChange.
func TestCredentialMonitor_CheckOne_FiresOnChange(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	persistHits := int32(0)
	persistSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&persistHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer persistSrv.Close()

	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", WorkspaceID: "w", Provider: ProviderAnthropic,
			AccessToken: "sk-ant-old", Status: StatusActive},
	})

	cm := NewCredentialMonitor(pool, persistSrv.URL, "tok", time.Hour, testLogger())
	// Persistence client and validation client share a transport that routes
	// api.anthropic.com to the upstream test server but lets persistSrv.URL
	// pass through normally.
	cm.client = &http.Client{Transport: rerouteAnthropic(upstream), Timeout: 5 * time.Second}

	var mu sync.Mutex
	var changes []string
	cm.SetOnChange(func(connID string, oldStatus, newStatus ConnectionStatus) {
		mu.Lock()
		defer mu.Unlock()
		changes = append(changes, connID+":"+string(oldStatus)+"->"+string(newStatus))
	})

	cm.checkOne(context.Background(), pool.AllConnections()[0])

	mu.Lock()
	got := append([]string{}, changes...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected 1 change, got %v", got)
	}
	if got[0] != "c1:ACTIVE->EXPIRED" {
		t.Errorf("change = %q", got[0])
	}
	if atomic.LoadInt32(&persistHits) != 1 {
		t.Errorf("expected 1 persist hit, got %d", persistHits)
	}
}

// TestCredentialMonitor_CheckOne_NoChangeNoPersist: identical status should
// NOT trigger persistStatus or the onChange callback.
func TestCredentialMonitor_CheckOne_NoChangeNoPersist(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	persistHits := int32(0)
	persistSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&persistHits, 1)
	}))
	defer persistSrv.Close()

	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", WorkspaceID: "w", Provider: ProviderAnthropic,
			AccessToken: "k", Status: StatusActive},
	})
	cm := NewCredentialMonitor(pool, persistSrv.URL, "tok", time.Hour, testLogger())
	cm.client = &http.Client{Transport: rerouteAnthropic(upstream), Timeout: 5 * time.Second}

	called := 0
	cm.SetOnChange(func(string, ConnectionStatus, ConnectionStatus) { called++ })

	cm.checkOne(context.Background(), pool.AllConnections()[0])
	if called != 0 {
		t.Errorf("expected 0 onChange calls, got %d", called)
	}
	if atomic.LoadInt32(&persistHits) != 0 {
		t.Errorf("expected 0 persist hits, got %d", persistHits)
	}
}

// TestCredentialMonitor_Validate_UnknownProvider returns the existing status.
func TestCredentialMonitor_Validate_UnknownProvider(t *testing.T) {
	t.Parallel()
	cm := NewCredentialMonitor(NewTokenPool(testLogger()),
		"http://x", "t", time.Hour, testLogger())
	got, msg := cm.validate(context.Background(), ProviderConnection{
		Provider: ProviderGoogle, Status: StatusActive,
	})
	if got != StatusActive || msg != "" {
		t.Errorf("expected unchanged ACTIVE, got %s msg=%q", got, msg)
	}
}

// TestTokenSyncer_Run_SyncsOnceThenStops verifies Run kicks off an immediate
// sync, then exits cleanly when ctx is cancelled.
func TestTokenSyncer_Run_SyncsOnceThenStops(t *testing.T) {
	t.Parallel()
	hits := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	pool := NewTokenPool(testLogger())
	syncer := NewTokenSyncer(pool, srv.URL, "tok", 50*time.Millisecond, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		syncer.Run(ctx)
		close(done)
	}()
	// Allow at least the initial sync.
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("syncer did not stop")
	}
	if atomic.LoadInt32(&hits) < 1 {
		t.Errorf("expected ≥1 sync calls, got %d", hits)
	}
}

// TestCredentialMonitor_Run_StopsOnCtxCancel guards the goroutine cleanup.
func TestCredentialMonitor_Run_StopsOnCtxCancel(t *testing.T) {
	t.Parallel()
	cm := NewCredentialMonitor(NewTokenPool(testLogger()),
		"http://x", "t", 100*time.Millisecond, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cm.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
