package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func cachedGuardFixture(identity string, extraDelete bool, methodless bool) *routeMux {
	m := newRouteMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fixture", identity)
		w.WriteHeader(http.StatusOK)
	}
	m.HandleFunc("GET /cache-things/count", handler)
	if methodless {
		m.HandleFunc("/cache-things/{id}", handler)
	} else {
		m.HandleFunc("DELETE /cache-things/{id}", handler)
	}
	if extraDelete {
		m.HandleFunc("DELETE /cache-things/count", handler)
	}
	return m
}

func assertCachedGuardResponse(t *testing.T, m *routeMux, method, path string, status int, allow, identity string) {
	t.Helper()
	r := httptest.NewRecorder()
	m.ServeHTTP(r, httptest.NewRequest(method, path, nil))
	if r.Code != status || r.Header().Get("Allow") != allow || r.Header().Get("X-Fixture") != identity {
		t.Fatalf("%s %s: status=%d allow=%q fixture=%q; want %d %q %q", method, path, r.Code, r.Header().Get("Allow"), r.Header().Get("X-Fixture"), status, allow, identity)
	}
}

func TestRouteMuxCachedGuardsKeepEachRoutersHandlers(t *testing.T) {
	first := cachedGuardFixture("first", false, false)
	first.sealMethodGuards()
	second := cachedGuardFixture("second", false, false)
	second.sealMethodGuards()
	for _, fixture := range []struct {
		mux      *routeMux
		identity string
	}{{first, "first"}, {second, "second"}} {
		assertCachedGuardResponse(t, fixture.mux, "DELETE", "/cache-things/count", 405, "GET, HEAD", "")
		assertCachedGuardResponse(t, fixture.mux, "GET", "/cache-things/count", 200, "", fixture.identity)
		assertCachedGuardResponse(t, fixture.mux, "HEAD", "/cache-things/count", 200, "", fixture.identity)
		assertCachedGuardResponse(t, fixture.mux, "DELETE", "/cache-things/other", 200, "", fixture.identity)
	}
}

func TestRouteMuxCachedGuardsRecomputeForDeclaredMethods(t *testing.T) {
	first := cachedGuardFixture("first", false, false)
	first.sealMethodGuards()
	second := cachedGuardFixture("second", true, false)
	second.sealMethodGuards()
	assertCachedGuardResponse(t, first, "DELETE", "/cache-things/count", 405, "GET, HEAD", "")
	assertCachedGuardResponse(t, second, "DELETE", "/cache-things/count", 200, "", "second")
	third := cachedGuardFixture("third", false, false)
	third.sealMethodGuards()
	assertCachedGuardResponse(t, third, "DELETE", "/cache-things/count", 405, "GET, HEAD", "")
}

func TestRouteMuxCachedGuardsPreserveMethodlessMounts(t *testing.T) {
	first := cachedGuardFixture("first", false, false)
	first.sealMethodGuards()
	second := cachedGuardFixture("second", false, true)
	second.sealMethodGuards()
	assertCachedGuardResponse(t, second, "DELETE", "/cache-things/count", 200, "", "second")
	assertCachedGuardResponse(t, second, "POST", "/cache-things/count", 200, "", "second")
	assertCachedGuardResponse(t, first, "DELETE", "/cache-things/count", 405, "GET, HEAD", "")
}

func TestRouteMuxCachedGuardsSeparateWildcardSpellingsAndOrder(t *testing.T) {
	makeMux := func(reverse bool) *routeMux {
		m := newRouteMux()
		handler := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }
		if reverse {
			m.HandleFunc("POST /cache-a/{agentId}/x", handler)
			m.HandleFunc("GET /cache-a/{id}/x", handler)
		} else {
			m.HandleFunc("GET /cache-a/{id}/x", handler)
			m.HandleFunc("POST /cache-a/{agentId}/x", handler)
		}
		m.HandleFunc("DELETE /cache-a/{id}/{rest}", handler)
		m.sealMethodGuards()
		return m
	}
	first, second := makeMux(false), makeMux(true)
	for _, m := range []*routeMux{first, second} {
		assertCachedGuardResponse(t, m, "DELETE", "/cache-a/1/x", 405, "GET, HEAD, POST", "")
	}
}

func TestRouteMuxCachedGuardsAreSafeAcrossConcurrentRouters(t *testing.T) {
	var wg sync.WaitGroup
	errors := make(chan string, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			identity := fmt.Sprint(i)
			m := cachedGuardFixture(identity, i%2 == 0, i%3 == 0)
			m.sealMethodGuards()
			for _, method := range []string{"GET", "DELETE"} {
				r := httptest.NewRecorder()
				m.ServeHTTP(r, httptest.NewRequest(method, "/cache-things/count", nil))
				status, gotIdentity, allow := 200, identity, ""
				if method == "DELETE" && i%2 != 0 && i%3 != 0 {
					status, gotIdentity, allow = 405, "", "GET, HEAD"
				}
				if r.Code != status || r.Header().Get("X-Fixture") != gotIdentity || r.Header().Get("Allow") != allow {
					errors <- fmt.Sprintf("router %d method %s: code=%d fixture=%q allow=%q", i, method, r.Code, r.Header().Get("X-Fixture"), r.Header().Get("Allow"))
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
