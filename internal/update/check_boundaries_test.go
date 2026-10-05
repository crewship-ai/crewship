package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type localUpdateTransport struct {
	destination *url.URL
	transport   http.RoundTripper
}

func (r localUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.URL.Scheme, copy.URL.Host = r.destination.Scheme, r.destination.Host
	return r.transport.RoundTrip(copy)
}

func localReleaseAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	destination, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: localUpdateTransport{destination, server.Client().Transport}}
	t.Cleanup(func() { http.DefaultClient = previous })
	t.Setenv("GITHUB_TOKEN", "")
}

func TestExplicitCheckBypassesPassiveOptOutAndCachedTarget(t *testing.T) {
	for _, tc := range []struct{ current, latest, path, query string }{
		{"1.0.0", "v2.0.0", "/repos/crewship-ai/crewship/releases/latest", ""},
		{"1.0.0-beta.1", "v1.0.0-beta.2", "/repos/crewship-ai/crewship/releases", "per_page=5"},
	} {
		t.Run(tc.current, func(t *testing.T) {
			withTempHome(t)
			t.Setenv("CREWSHIP_SKIP_UPDATE_CHECK", "1")
			writeCache(&Result{Current: tc.current, Latest: "v8.0.0", CheckedAt: time.Now()})
			var hits atomic.Int32
			localReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path != tc.path || r.URL.RawQuery != tc.query || r.Header.Get("User-Agent") != "crewship-update-check" {
					t.Errorf("wrong update request: %s %v", r.URL, r.Header)
				}
				_, _ = w.Write([]byte(`{"tag_name":"` + tc.latest + `","body":"release notes","html_url":"https://example.test/release"}`))
			})
			result, err := CheckExplicit(t.Context(), tc.current)
			if err != nil || result == nil || result.Latest != tc.latest || !result.Newer || result.Current != tc.current || result.Notes != "release notes" || result.URL != "https://example.test/release" || result.CheckedAt.IsZero() {
				t.Fatalf("explicit result: %#v %v", result, err)
			}
			if hits.Load() != 1 {
				t.Fatalf("explicit check ignored network: %d", hits.Load())
			}
			if result, err := Check(t.Context(), tc.current); err != nil || result != nil || hits.Load() != 1 {
				t.Fatalf("passive opt-out ignored: %#v %v", result, err)
			}
		})
	}
}

func TestPassiveCheckFetchesAndReusesCurrentChannelCache(t *testing.T) {
	for _, current := range []string{"1.0.0", "1.0.0-beta.1"} {
		t.Run(current, func(t *testing.T) {
			withTempHome(t)
			t.Setenv("CREWSHIP_SKIP_UPDATE_CHECK", "")
			var hits atomic.Int32
			localReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = w.Write([]byte(`{"tag_name":"v2.0.0","html_url":"https://example.test/release"}`))
			})
			first, err := Check(t.Context(), current)
			if err != nil || first == nil || !first.Newer || first.Latest != "v2.0.0" {
				t.Fatalf("initial check: %#v %v", first, err)
			}
			second, err := Check(t.Context(), "2.0.0")
			if err != nil || second == nil || second.Newer || second.Current != "2.0.0" || hits.Load() != 1 {
				t.Fatalf("cache did not adapt after upgrade: %#v %v; hits %d", second, err, hits.Load())
			}
		})
	}
}

func TestExplicitCheckRejectsInvalidRemoteTagAndUnavailableAPI(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"invalid target", `{"tag_name":"not-a-version"}`, 200, "not valid semver"},
		{"unavailable", `{"message":"offline"}`, 503, "503"},
		{"malformed", `{`, 200, "JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localReleaseAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			if result, err := CheckExplicit(t.Context(), "1.0.0"); err == nil || result != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe target offered: %#v %v", result, err)
			}
		})
	}
}

func TestUpdateChecksPropagateCancellationAndIncomparableVersions(t *testing.T) {
	withTempHome(t)
	t.Setenv("CREWSHIP_SKIP_UPDATE_CHECK", "")
	localReleaseAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("cancelled or invalid check reached server")
		w.WriteHeader(500)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, check := range []func(context.Context, string) (*Result, error){Check, CheckExplicit} {
		if got, err := check(ctx, "1.0.0"); got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled check: %#v %v", got, err)
		}
		var incomparable *IncomparableVersionError
		if got, err := check(t.Context(), "local-custom-build"); got != nil || !errors.As(err, &incomparable) {
			t.Fatalf("uncomparable check: %#v %v", got, err)
		}
	}
}
