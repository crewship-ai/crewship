package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
)

func unlockWorkRequest(h http.Handler, ctx context.Context, token string, passwords ...string) *httptest.ResponseRecorder {
	password := "owned-password"
	if len(passwords) > 0 {
		password = passwords[0]
	}
	body, _ := json.Marshal(map[string]string{"password": password})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/public/pages/"+token+"/unlock", strings.NewReader(string(body))).WithContext(ctx)
	h.ServeHTTP(rr, req)
	return rr
}

func awaitPasswordWork(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(30 * time.Second):
		t.Fatal("password work boundary not reached")
	}
}

func TestPublicUnlock_GlobalTargetAndCancellationAdmission(t *testing.T) {
	h, _, _, ws, user := newPagesPublicFixture(t, "work-admission")
	protected := pagesTokenOf(t, pagesPublish(t, h, ws, user, "work-admission", `{"password":"owned-password"}`))
	unprotected := pagesTokenOf(t, pagesPublish(t, h, ws, user, "work-admission", `{}`))
	h.passwordWork = newPagePasswordAdmission(config.PublicPagePasswordsConfig{MaxConcurrent: 2, MaxConcurrentPerTarget: 1})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var workers sync.WaitGroup
	var calls atomic.Int32
	h.comparePassword = func(_ []byte, password []byte) error {
		calls.Add(1)
		if !strings.HasPrefix(string(password), "owned-held-") {
			return context.Canceled
		}
		entered <- struct{}{}
		<-release
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/public/pages/{token}/unlock", h.Unlock)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	released := false
	defer func() {
		if !released {
			close(release)
		}
		workers.Wait()
	}()
	workers.Add(1)
	go func() { defer workers.Done(); unlockWorkRequest(mux, ctx, "owned-target-a", "owned-held-a") }()
	awaitPasswordWork(t, entered)
	busy := func(token string) {
		t.Helper()
		rr := unlockWorkRequest(mux, t.Context(), token)
		if rr.Code != 429 || rr.Header().Get("Retry-After") != "1" {
			t.Fatalf("busy status=%d retry=%q body=%s", rr.Code, rr.Header().Get("Retry-After"), rr.Body.String())
		}
	}
	busy("owned-target-a")
	workers.Add(1)
	go func() { defer workers.Done(); unlockWorkRequest(mux, t.Context(), "owned-target-b", "owned-held-b") }()
	awaitPasswordWork(t, entered)
	busy("owned-target-c")
	busy(protected)
	busy(unprotected)
	cancel()
	busy("owned-target-c")
	if calls.Load() != 2 {
		t.Fatalf("busy request entered comparison; calls=%d", calls.Load())
	}
	close(release)
	released = true
	workers.Wait()
	if rr := unlockWorkRequest(mux, t.Context(), "owned-target-c"); rr.Code != 401 {
		t.Fatalf("released admission not reusable: %d %s", rr.Code, rr.Body.String())
	}
	h.passwordWork.mu.Lock()
	defer h.passwordWork.mu.Unlock()
	if h.passwordWork.active != 0 || len(h.passwordWork.targets) != 0 {
		t.Fatalf("idle targets retained: active%d keys%d", h.passwordWork.active, len(h.passwordWork.targets))
	}
}

func TestPublicUnlock_RouterConfigAndComparisonErrors(t *testing.T) {
	h, _, _, _, _ := newPagesPublicFixture(t, "router-work-admission")
	r, err := NewRouter(h.db, strings.Repeat("owned-test-secret-", 3), newTestLogger(), WithPublicPagePasswords(config.PublicPagePasswordsConfig{MaxConcurrent: 1, MaxConcurrentPerTarget: 1}))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int32
	r.pagePublicHandler.comparePassword = func([]byte, []byte) error {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
		return context.Canceled
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
		awaitPasswordWork(t, done)
	}()
	go func() { defer close(done); unlockWorkRequest(r, t.Context(), "owned-first-router-token") }()
	awaitPasswordWork(t, entered)
	rr := unlockWorkRequest(r, t.Context(), "owned-second-router-token")
	close(release)
	released = true
	awaitPasswordWork(t, done)
	if rr.Code != 429 {
		t.Fatalf("router ignored configured global limit: %d %s", rr.Code, rr.Body.String())
	}
	rr = unlockWorkRequest(r, t.Context(), "owned-third-router-token")
	if rr.Code != 401 {
		t.Fatalf("compare error did not release slot: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := NewRouter(h.db, strings.Repeat("owned-test-secret-", 3), newTestLogger(), WithPublicPagePasswords(config.PublicPagePasswordsConfig{MaxConcurrent: -1})); err == nil {
		t.Fatal("router accepted invalid limits")
	}
}

type blockedPublicPageResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockedPublicPageResponse) WriteHeader(status int) {
	if status == http.StatusOK {
		w.once.Do(func() { close(w.entered); <-w.release })
	}
	w.ResponseRecorder.WriteHeader(status)
}

func TestPublicUnlock_ReleasesWorkBeforeServingPage(t *testing.T) {
	h, _, _, ws, user := newPagesPublicFixture(t, "release-before-page")
	token := pagesTokenOf(t, pagesPublish(t, h, ws, user, "release-before-page", `{"password":"owned-password"}`))
	h.passwordWork = newPagePasswordAdmission(config.PublicPagePasswordsConfig{MaxConcurrent: 1, MaxConcurrentPerTarget: 1})
	h.comparePassword = func([]byte, []byte) error { return nil }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/public/pages/{token}/unlock", h.Unlock)
	w := &blockedPublicPageResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	req := httptest.NewRequest("POST", "/api/v1/public/pages/"+token+"/unlock", strings.NewReader(`{"password":"owned-password"}`))
	go func() { defer close(done); mux.ServeHTTP(w, req) }()
	defer func() { close(w.release); awaitPasswordWork(t, done) }()
	awaitPasswordWork(t, w.entered)
	if rr := unlockWorkRequest(mux, t.Context(), "owned-other-link"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("page response retained bcrypt slot: %d %s", rr.Code, rr.Body.String())
	}
}
