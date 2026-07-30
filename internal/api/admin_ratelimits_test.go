package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

func newRateLimitsTestHandler(t *testing.T) (*AdminRateLimitsHandler, *ratelimitcfg.Store) {
	t.Helper()
	db := setupTestDB(t)
	store := ratelimitcfg.New(db)
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load store: %v", err)
	}
	return NewAdminRateLimitsHandler(store, newTestLogger()), store
}

func manageReq(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(req.Context(), ctxRole, "OWNER")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "admin-1"})
	return req.WithContext(ctx)
}

func TestAdminRateLimits_List_RequiresManageRole(t *testing.T) {
	h, _ := newRateLimitsTestHandler(t)

	// A viewer (non-manage) is refused even though authedAdmin would already
	// gate the route — defence in depth.
	req := httptest.NewRequest("GET", "/api/v1/admin/rate-limits", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxRole, "VIEWER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("viewer got %d, want 403", rr.Code)
	}
}

func TestAdminRateLimits_List_ReturnsRegistryWithDefaults(t *testing.T) {
	h, _ := newRateLimitsTestHandler(t)

	rr := httptest.NewRecorder()
	h.List(rr, manageReq("GET", "/api/v1/admin/rate-limits", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("List got %d, want 200", rr.Code)
	}
	var resp struct {
		Limiters []ratelimitcfg.State `json:"limiters"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Limiters) == 0 {
		t.Fatal("empty limiter list")
	}
	var auth *ratelimitcfg.State
	for i := range resp.Limiters {
		if resp.Limiters[i].Key == ratelimitcfg.KeyHTTPAuthPerMin {
			auth = &resp.Limiters[i]
		}
	}
	if auth == nil {
		t.Fatal("auth limiter missing from list")
	}
	wantAuth := ratelimitcfg.DefaultFor(ratelimitcfg.KeyHTTPAuthPerMin)
	if auth.Value != wantAuth || auth.Default != wantAuth || auth.Overridden {
		t.Errorf("auth limiter = %+v, want value/default 10, overridden=false", *auth)
	}
}

func TestAdminRateLimits_Set_UpdatesValue(t *testing.T) {
	h, store := newRateLimitsTestHandler(t)

	rr := httptest.NewRecorder()
	req := manageReq("PUT", "/api/v1/admin/rate-limits/"+ratelimitcfg.KeyHTTPAuthPerMin, `{"value":40}`)
	req.SetPathValue("key", ratelimitcfg.KeyHTTPAuthPerMin)
	h.Set(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Set got %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if got := store.Value(ratelimitcfg.KeyHTTPAuthPerMin); got != 40 {
		t.Errorf("store value = %d, want 40", got)
	}
	var st ratelimitcfg.State
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Value != 40 || !st.Overridden {
		t.Errorf("response state = %+v, want value 40 overridden=true", st)
	}
}

func TestAdminRateLimits_Set_RejectsBadInput(t *testing.T) {
	h, _ := newRateLimitsTestHandler(t)

	cases := []struct {
		name, key, body string
		want            int
	}{
		{"unknown key", "no.such.key", `{"value":5}`, http.StatusNotFound},
		{"below min", ratelimitcfg.KeyHTTPAuthPerMin, `{"value":0}`, http.StatusBadRequest},
		{"above max", ratelimitcfg.KeyLoginLockoutDurSec, `{"value":999999}`, http.StatusBadRequest},
		{"malformed body", ratelimitcfg.KeyHTTPAuthPerMin, `not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := manageReq("PUT", "/api/v1/admin/rate-limits/"+tc.key, tc.body)
			req.SetPathValue("key", tc.key)
			h.Set(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d (body %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestAdminRateLimits_Reset_RevertsToDefault(t *testing.T) {
	h, store := newRateLimitsTestHandler(t)
	if err := store.Set(context.Background(), ratelimitcfg.KeyHTTPAPIPerMin, 500, "seed"); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	rr := httptest.NewRecorder()
	req := manageReq("DELETE", "/api/v1/admin/rate-limits/"+ratelimitcfg.KeyHTTPAPIPerMin, "")
	req.SetPathValue("key", ratelimitcfg.KeyHTTPAPIPerMin)
	h.Reset(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Reset got %d, want 200", rr.Code)
	}
	if got, want := store.Value(ratelimitcfg.KeyHTTPAPIPerMin), ratelimitcfg.DefaultFor(ratelimitcfg.KeyHTTPAPIPerMin); got != want {
		t.Errorf("after reset, value = %d, want default %d", got, want)
	}
}

// TestRouter_HTTPRateLimitOverride_AppliesLive is the end-to-end proof that an
// admin override reaches the running per-IP limiter without a restart: the
// router reads the store at construction AND retunes the live limiter on the
// store's OnChange hook.
func TestRouter_HTTPRateLimitOverride_AppliesLive(t *testing.T) {
	db := setupTestDB(t)
	store := ratelimitcfg.New(db)
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithRateLimitStore(store))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// hammerAuth fires n credential-submitting POSTs from one IP and reports
	// whether any 429'd. Each call must use a FRESH IP — the bucket is per-IP
	// and a reused address carries state from the previous phase.
	hammerAuth := func(ip string, n int) bool {
		saw429 := false
		for i := 0; i < n; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/callback/credentials", nil)
			req.RemoteAddr = ip
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code == http.StatusTooManyRequests {
				saw429 = true
			}
		}
		return saw429
	}

	// The subject here is "an override reaches the live limiter", in both
	// directions. It used to prove that by leaning on the shipped default
	// being tight (12 requests past a 10/min bucket) — which coupled a
	// mechanism test to a product decision, and broke the moment the default
	// was raised to something that does not throttle real users. Drive it
	// from an explicit override instead: deterministic, and it holds whatever
	// we ship.
	if err := store.Set(context.Background(), ratelimitcfg.KeyHTTPAuthPerMin, 5, "test"); err != nil {
		t.Fatalf("tighten: %v", err)
	}
	if !hammerAuth("127.0.1.1:1", 12) {
		t.Fatal("a 5/min override must 429 within 12 requests — the override never reached the limiter")
	}

	// Loosen: a fresh IP must no longer trip on the same traffic.
	if err := store.Set(context.Background(), ratelimitcfg.KeyHTTPAuthPerMin, 1000, "test"); err != nil {
		t.Fatalf("set override: %v", err)
	}
	if hammerAuth("127.0.1.2:1", 12) {
		t.Error("after raising the auth limit to 1000/min, a fresh IP must not 429")
	}

	// Reset restores the shipped default...
	if err := store.Reset(context.Background(), ratelimitcfg.KeyHTTPAuthPerMin, "test"); err != nil {
		t.Fatalf("reset override: %v", err)
	}
	if got, want := store.Value(ratelimitcfg.KeyHTTPAuthPerMin), ratelimitcfg.DefaultFor(ratelimitcfg.KeyHTTPAuthPerMin); got != want {
		t.Errorf("after reset, value = %d, want default %d", got, want)
	}
	// ...and the limiter is still following the store, not stuck on the last
	// override it happened to see.
	if err := store.Set(context.Background(), ratelimitcfg.KeyHTTPAuthPerMin, 5, "test"); err != nil {
		t.Fatalf("re-tighten: %v", err)
	}
	if !hammerAuth("127.0.1.3:1", 12) {
		t.Error("the limiter stopped tracking the store after a reset")
	}
}
