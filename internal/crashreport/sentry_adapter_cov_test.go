package crashreport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// TestSentryBackend_InitCaptureFlush_DeliversToDSNHost drives the real
// sentry-go SDK end to end against a local httptest server standing in
// for the Sentry ingest endpoint. Covers the Init happy path, the
// initialized Capture path (scope + tags + CaptureException), and the
// initialized Flush path.
func TestSentryBackend_InitCaptureFlush_DeliversToDSNHost(t *testing.T) {
	var received atomic.Int32
	var gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		gotPath.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	dsn := "http://publickey@" + host + "/42"

	b := &sentryBackend{}
	if err := b.Init(dsn, "install-abc", "v1.2.3"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !b.initialized {
		t.Fatal("initialized flag not set after successful Init")
	}

	b.Capture(errors.New("boom for coverage"), map[string]string{"area": "test"})
	// Give Flush generous headroom: on a loaded CI runner the async ingest
	// POST can take longer than a tight window, and Flush can also return
	// just before the httptest handler runs. Flush with a wide timeout, then
	// poll for the request to land so the assertion isn't racing delivery.
	b.Flush(10 * time.Second)

	deadline := time.Now().Add(5 * time.Second)
	for received.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Fatal("Sentry ingest server received no requests after Capture+Flush")
	}
	if p, _ := gotPath.Load().(string); !strings.Contains(p, "/api/42/") {
		t.Errorf("ingest path = %q, want project 42 endpoint", p)
	}
}

func TestSentryBackend_Init_InvalidDSN(t *testing.T) {
	b := &sentryBackend{}
	err := b.Init("://not-a-dsn", "id", "v1")
	if err == nil {
		t.Fatal("Init accepted an unparseable DSN")
	}
	if b.initialized {
		t.Error("initialized flag set despite Init failure")
	}
}

func TestScrubRequest_RedactsSensitiveSurfaces(t *testing.T) {
	req := &sentry.Request{
		Data: `{"password":"hunter2"}`,
		Headers: map[string]string{
			"Authorization": "Bearer secret-token",
			"Cookie":        "session=abc",
			"Accept":        "application/json",
		},
		URL:         "https://crew.example/api/v1/login?token=abc123&page=2",
		QueryString: "token=abc123&page=2",
	}
	scrubRequest(req)

	if req.Data != "" {
		t.Errorf("Data = %q, want dropped wholesale", req.Data)
	}
	if req.Headers["Authorization"] != "[redacted]" {
		t.Errorf("Authorization = %q, want [redacted]", req.Headers["Authorization"])
	}
	if req.Headers["Cookie"] != "[redacted]" {
		t.Errorf("Cookie = %q, want [redacted]", req.Headers["Cookie"])
	}
	if req.Headers["Accept"] != "application/json" {
		t.Errorf("Accept = %q, want preserved", req.Headers["Accept"])
	}
	if strings.Contains(req.URL, "abc123") {
		t.Errorf("URL still leaks the token value: %q", req.URL)
	}
	if !strings.Contains(req.URL, "page=2") {
		t.Errorf("URL lost the harmless query param: %q", req.URL)
	}
	if strings.Contains(req.QueryString, "abc123") {
		t.Errorf("QueryString still leaks the token: %q", req.QueryString)
	}
}

func TestScrubRequest_MalformedURLLeftAlone(t *testing.T) {
	req := &sentry.Request{URL: "://%%%bad"}
	scrubRequest(req) // must not panic; unparseable URL stays as-is
	if req.URL != "://%%%bad" {
		t.Errorf("URL = %q, want untouched on parse failure", req.URL)
	}
}

func TestScrubEvent_NilEventReturnsNil(t *testing.T) {
	if got := scrubEvent(nil, nil); got != nil {
		t.Errorf("scrubEvent(nil) = %v, want nil", got)
	}
}

func TestScrubEvent_RedactsEverythingSensitive(t *testing.T) {
	ev := &sentry.Event{
		User: sentry.User{ID: "user-1", Email: "x@example.com", IPAddress: "10.0.0.1"},
		Request: &sentry.Request{
			Data:    "body",
			Headers: map[string]string{"X-Api-Key": "k"},
		},
		Contexts: map[string]sentry.Context{
			"device":  {"arch": "arm64"},
			"runtime": {"name": "go"},
			"culture": {"locale": "en"},
			"os":      {"name": "macOS"},
		},
		Breadcrumbs: []*sentry.Breadcrumb{
			{Message: "clicked", Data: map[string]interface{}{"path": "/secret"}},
			nil, // nil entries must be tolerated
		},
		Modules: map[string]string{"github.com/some/dep": "v1.0.0"},
	}

	got := scrubEvent(ev, nil)
	if got == nil {
		t.Fatal("scrubEvent dropped a real event")
	}
	if got.User.ID != "" || got.User.Email != "" || got.User.IPAddress != "" {
		t.Errorf("User not zeroed: %+v", got.User)
	}
	if got.Request.Data != "" {
		t.Errorf("Request.Data = %q, want dropped", got.Request.Data)
	}
	if got.Request.Headers["X-Api-Key"] != "[redacted]" {
		t.Errorf("X-Api-Key = %q, want [redacted]", got.Request.Headers["X-Api-Key"])
	}
	for _, k := range []string{"device", "runtime", "culture"} {
		if _, ok := got.Contexts[k]; ok {
			t.Errorf("context %q survived the scrub", k)
		}
	}
	if _, ok := got.Contexts["os"]; !ok {
		t.Error("os context was dropped, want preserved for platform triage")
	}
	if got.Breadcrumbs[0].Data != nil {
		t.Errorf("breadcrumb Data survived: %v", got.Breadcrumbs[0].Data)
	}
	if got.Breadcrumbs[0].Message != "clicked" {
		t.Errorf("breadcrumb Message = %q, want preserved", got.Breadcrumbs[0].Message)
	}
	if got.Modules != nil {
		t.Errorf("Modules = %v, want nil", got.Modules)
	}
}
