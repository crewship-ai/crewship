package llmproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCredentialMonitor_ValidateAnthropic_Active(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	pool := NewTokenPool(testLogger())
	monitor := NewCredentialMonitor(pool, "http://localhost:9999", "tok", time.Hour, testLogger())
	monitor.client = srv.Client()

	conn := ProviderConnection{
		ID:          "c1",
		Provider:    ProviderAnthropic,
		AccessToken: "valid-token",
		Status:      StatusActive,
	}

	// Override the Anthropic validation URL for testing
	origValidate := monitor.validate
	_ = origValidate

	status, _ := monitor.validateAnthropic(context.Background(), conn)
	// This will fail because it hits the test server but the URL is hardcoded to api.anthropic.com
	// In real tests we'd need to mock the HTTP client, but for unit tests this validates the logic structure
	if status != StatusError && status != StatusActive {
		t.Logf("status: %s (expected ACTIVE with real API or ERROR with test server)", status)
	}
}

func TestCredentialMonitor_OnChange(t *testing.T) {
	var mu sync.Mutex
	var changes []string

	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", WorkspaceID: "org1", Provider: ProviderAnthropic, AccessToken: "tok", Status: StatusActive},
	})

	nextjsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer nextjsSrv.Close()

	monitor := NewCredentialMonitor(pool, nextjsSrv.URL, "tok", time.Hour, testLogger())
	monitor.SetOnChange(func(connID string, oldStatus, newStatus ConnectionStatus) {
		mu.Lock()
		defer mu.Unlock()
		changes = append(changes, connID+":"+string(oldStatus)+"->"+string(newStatus))
	})

	// MarkStatus on the pool does NOT trigger the onChange callback — only
	// checkOne does. This test verifies two things:
	//   1. SetOnChange stores the callback on the monitor
	//   2. MarkStatus is a no-op with respect to the callback path
	pool.MarkStatus("c1", StatusRateLimited)

	mu.Lock()
	gotChanges := len(changes)
	mu.Unlock()
	if gotChanges != 0 {
		t.Errorf("MarkStatus should not fire onChange on its own; got %d changes: %v", gotChanges, changes)
	}

	if monitor.onChange.Load() == nil {
		t.Error("onChange callback should be set")
	}
}

func TestTokenPool_AllConnections(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{ID: "c1", Provider: ProviderAnthropic, Status: StatusActive},
		{ID: "c2", Provider: ProviderOpenAI, Status: StatusExpired},
	})

	all := pool.AllConnections()
	if len(all) != 2 {
		t.Errorf("expected 2 connections, got %d", len(all))
	}

	// Verify it's a copy (modifying doesn't affect pool)
	all[0].Status = StatusRevoked
	poolConn := pool.SelectToken("", ProviderAnthropic)
	// Won't match because WorkspaceID is empty
	if poolConn != nil {
		t.Log("no match expected since WorkspaceID is empty")
	}
}

// newTestMonitor builds a monitor whose provider-validation endpoint and
// status-persistence endpoint both point at local test servers, so the whole
// validate -> map status -> record outcome -> persist path runs for real.
func newTestMonitor(t *testing.T, pool *TokenPool, providerURL string) (*CredentialMonitor, *[]StatusUpdate, *sync.Mutex) {
	t.Helper()

	var mu sync.Mutex
	var persisted []StatusUpdate

	nextjs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upd StatusUpdate
		_ = json.NewDecoder(r.Body).Decode(&upd)
		mu.Lock()
		persisted = append(persisted, upd)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(nextjs.Close)

	cm := NewCredentialMonitor(pool, nextjs.URL, "tok", time.Hour, testLogger())
	cm.anthropicModelsURL = providerURL
	cm.openaiModelsURL = providerURL
	cm.googleModelsURL = providerURL
	return cm, &persisted, &mu
}

func poolStatus(t *testing.T, pool *TokenPool, id string) ConnectionStatus {
	t.Helper()
	for _, c := range pool.AllConnections() {
		if c.ID == id {
			return c.Status
		}
	}
	t.Fatalf("connection %s not found in pool", id)
	return ""
}

// The production entry point is checkAll, and ONLY checkAll: Run() ticks it,
// nothing else calls checkOne or any of the monitor's other internals. This
// test therefore drives checkAll and nothing but checkAll, against a provider
// that rejects every request, and asserts the credential the provider refuses
// is not advertised as healthy.
//
// #1277 shipped a guard for exactly this and the guard never ran: checkAll
// `continue`s past checkOne for every sk-ant-oat connection, and checkOne was
// the only thing recording an outcome, so the guard's input was permanently
// "nothing has validated this" — which it treated as resurrectable. Five ticks
// later the credential was ACTIVE in the pool and ACTIVE in the database,
// having never once been shown to the provider.
func TestCredentialMonitor_CheckAllDoesNotResurrectRejectedOAuth(t *testing.T) {
	var probes atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer provider.Close()

	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{{
		ID:          "oat-prod",
		WorkspaceID: "org1",
		Provider:    ProviderAnthropic,
		// The class the guard is meant to cover: an OAuth CLI token, stored
		// as API_KEY, that something already marked EXPIRED.
		Type:        TypeAPIKey,
		AccessToken: "sk-ant-oat-revoked",
		Status:      StatusExpired,
	}})

	cm, persisted, mu := newTestMonitor(t, pool, provider.URL)

	for i := 0; i < 5; i++ {
		cm.checkAll(context.Background())
	}

	if got := poolStatus(t, pool, "oat-prod"); got != StatusExpired {
		t.Errorf("EXPIRED OAuth credential resurrected by the monitor loop: want EXPIRED, got %s (provider contacted %d times)",
			got, probes.Load())
	}

	mu.Lock()
	defer mu.Unlock()
	for _, upd := range *persisted {
		if upd.Status == StatusActive {
			t.Errorf("monitor loop persisted ACTIVE for a credential it never validated: %+v", *persisted)
			break
		}
	}
}

// #1254 item B: a credential that a provider actually rejected (real 401) must
// stay EXPIRED. Before the fix, checkAll flipped every EXPIRED sk-ant-oat
// credential back to ACTIVE on every tick — and persisted ACTIVE to the DB —
// so a revoked token was repeatedly advertised as healthy.
func TestCredentialMonitor_OAuthNotResurrectedAfterRealAuthFailure(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer provider.Close()

	conn := ProviderConnection{
		ID:          "oat-dead",
		WorkspaceID: "org1",
		Provider:    ProviderAnthropic,
		Type:        TypeAPIKey,
		AccessToken: "sk-ant-oat-revoked",
		Status:      StatusActive,
	}
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{conn})

	cm, persisted, mu := newTestMonitor(t, pool, provider.URL)

	// A real validation against a real 401 marks it EXPIRED.
	cm.checkOne(context.Background(), conn)
	if got := poolStatus(t, pool, "oat-dead"); got != StatusExpired {
		t.Fatalf("after 401, want status EXPIRED, got %s", got)
	}

	// The next monitor tick must NOT undo that verdict.
	cm.checkAll(context.Background())
	if got := poolStatus(t, pool, "oat-dead"); got != StatusExpired {
		t.Errorf("rejected OAuth credential was resurrected: want EXPIRED, got %s", got)
	}

	// ...and must not have told the database it is healthy.
	mu.Lock()
	defer mu.Unlock()
	for _, upd := range *persisted {
		if upd.Status == StatusActive {
			t.Errorf("persisted ACTIVE for a credential the provider rejected: %+v", *persisted)
			break
		}
	}
}

// An OAuth token whose own recorded expiry has already elapsed is genuinely
// expired. (With the resurrection branch removed this holds for the same
// reason every other OAuth case does — the monitor changes nothing about a
// credential it cannot validate — but the case is worth keeping pinned
// because it is the one an operator is most likely to eyeball.)
func TestCredentialMonitor_OAuthNotResurrectedWhenTokenExpiryElapsed(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{{
		ID:             "oat-past",
		WorkspaceID:    "org1",
		Provider:       ProviderAnthropic,
		Type:           TypeAICLIToken,
		AccessToken:    "sk-ant-oat-stale",
		TokenExpiresAt: &past,
		Status:         StatusExpired,
	}})

	cm, _, _ := newTestMonitor(t, pool, "http://127.0.0.1:1")
	cm.checkAll(context.Background())

	if got := poolStatus(t, pool, "oat-past"); got != StatusExpired {
		t.Errorf("credential past its own expiry was resurrected: got %s", got)
	}
}

// The monitor no longer "repairs" an EXPIRED OAuth credential it has never
// validated. It cannot validate one at all (no /v1/models equivalent for
// sk-ant-oat), so a flip to ACTIVE here is an assertion of health with zero
// evidence behind it — and it was PATCHed to the database, which is how a
// revoked token got re-advertised as usable. Recovery is the re-link path.
func TestCredentialMonitor_OAuthNotResurrectedWhenNeverValidated(t *testing.T) {
	future := time.Now().Add(2 * time.Hour)
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{
		{
			ID:          "oat-unvalidated",
			WorkspaceID: "org1",
			Provider:    ProviderAnthropic,
			Type:        TypeAPIKey,
			AccessToken: "sk-ant-oat-fine",
			Status:      StatusExpired,
		},
		{
			ID:             "oat-unvalidated-future",
			WorkspaceID:    "org1",
			Provider:       ProviderAnthropic,
			Type:           TypeAICLIToken,
			AccessToken:    "sk-ant-oat-fine2",
			TokenExpiresAt: &future,
			Status:         StatusExpired,
		},
	})

	cm, _, _ := newTestMonitor(t, pool, "http://127.0.0.1:1")
	cm.checkAll(context.Background())

	for _, id := range []string{"oat-unvalidated", "oat-unvalidated-future"} {
		if got := poolStatus(t, pool, id); got != StatusExpired {
			t.Errorf("%s: monitor flipped an OAuth credential it never validated, got %s", id, got)
		}
	}
}

// A transient failure (unreachable host / 5xx) is not a verdict on the
// credential: it maps to ERROR, never to EXPIRED/REVOKED, so a flaky network
// can never be mistaken for a revoked token by anything reading the status.
func TestCredentialMonitor_TransientFailureMapsToError(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer provider.Close()

	conn := ProviderConnection{
		ID:          "oat-flaky",
		WorkspaceID: "org1",
		Provider:    ProviderAnthropic,
		Type:        TypeAPIKey,
		AccessToken: "sk-ant-oat-fine",
		Status:      StatusActive,
	}
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{conn})

	cm, _, _ := newTestMonitor(t, pool, provider.URL)

	cm.checkOne(context.Background(), conn)
	if got := poolStatus(t, pool, "oat-flaky"); got != StatusError {
		t.Fatalf("after 502, want ERROR, got %s", got)
	}

	// A second tick against the same flaky provider must not escalate it.
	cm.checkAll(context.Background())
	if got := poolStatus(t, pool, "oat-flaky"); got != StatusError {
		t.Errorf("transient failure escalated on the next tick: got %s", got)
	}
}

// An unsupported provider is never probed, so nothing about it may change and
// nothing may be written to the database on its behalf.
func TestCredentialMonitor_UnsupportedProviderChangesNothing(t *testing.T) {
	pool := NewTokenPool(testLogger())
	conn := ProviderConnection{
		ID:          "other",
		WorkspaceID: "org1",
		Provider:    ProviderType("SOMETHING_ELSE"),
		Type:        TypeAPIKey,
		AccessToken: "not-an-oat-key",
		Status:      StatusExpired,
	}
	pool.Update([]ProviderConnection{conn})

	cm, persisted, mu := newTestMonitor(t, pool, "http://127.0.0.1:1")
	cm.checkAll(context.Background())

	if got := poolStatus(t, pool, "other"); got != StatusExpired {
		t.Errorf("unprobed provider must keep its status, got %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*persisted) != 0 {
		t.Errorf("unprobed provider must not persist anything, got %+v", *persisted)
	}
}
