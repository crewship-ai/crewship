package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

func TestQuiesceGate(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	gate := quiesceGate(ok)
	cases := []struct {
		name     string
		window   bool
		holds    []string
		method   string
		path     string
		wantCode int
	}{
		{name: "no window: writes pass", method: "POST", path: "/api/v1/missions", wantCode: http.StatusTeapot},
		{name: "window: reads pass", window: true, method: "GET", path: "/api/v1/missions", wantCode: http.StatusTeapot},
		{name: "window: writes held", window: true, method: "PATCH", path: "/api/v1/missions/m1", wantCode: http.StatusServiceUnavailable},
		{name: "window: uploads held", window: true, method: "POST", path: "/api/v1/workspaces/w/attachments", wantCode: http.StatusServiceUnavailable},
		{name: "window: sidecar writes held", window: true, method: "POST", path: "/api/v1/internal/journal/emit", wantCode: http.StatusServiceUnavailable},
		{name: "window: backup endpoints open", window: true, method: "POST", path: "/api/v1/admin/instance/backups/bundles/pin", wantCode: http.StatusTeapot},
		{name: "window: resume open", window: true, method: "POST", path: "/api/v1/admin/instance/holds/resume", wantCode: http.StatusTeapot},
		{name: "window: sign-in open", window: true, method: "POST", path: "/api/auth/callback/credentials", wantCode: http.StatusTeapot},
		{name: "window: traversal past the allowlist is held", window: true, method: "POST", path: "/api/v1/admin/instance/backups/../../../credentials", wantCode: http.StatusServiceUnavailable},
		{name: "window: webhooks held", window: true, method: "POST", path: "/api/v1/webhooks/tok", wantCode: http.StatusServiceUnavailable},
		{name: "webhooks hold: webhook refused", holds: []string{quiesce.HoldWebhooks}, method: "POST", path: "/api/v1/page-webhooks/tok", wantCode: http.StatusServiceUnavailable},
		{name: "webhooks hold: other writes pass", holds: []string{quiesce.HoldWebhooks}, method: "POST", path: "/api/v1/missions", wantCode: http.StatusTeapot},
		{name: "routines hold: webhooks pass", holds: []string{quiesce.HoldRoutines}, method: "POST", path: "/api/v1/webhooks/tok", wantCode: http.StatusTeapot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hs []quiesce.Hold
			for _, k := range tc.holds {
				hs = append(hs, quiesce.Hold{Key: k})
			}
			quiesce.DefaultHolds().Replace(hs)
			defer quiesce.DefaultHolds().Replace(nil)
			if tc.window {
				w, err := quiesce.Default().Begin(context.Background(), quiesce.Options{HoldCap: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				defer w.Release()
			}
			rr := httptest.NewRecorder()
			gate.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rr.Code, tc.wantCode, rr.Body.String())
			}
			if rr.Code == http.StatusServiceUnavailable && rr.Header().Get("Retry-After") == "" {
				t.Fatal("503 without Retry-After")
			}
		})
	}
}

// waitForQuietWindow polls until the default controller reports a window
// closing or held. A wait for another goroutine's state, not a sleep any
// assertion depends on.
func waitForQuietWindow(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !quiesce.Default().Holding() {
		if time.Now().After(deadline) {
			t.Fatal("the quiet window never started closing")
		}
		time.Sleep(time.Millisecond)
	}
}

// B5 (harbor backups review 2026-09-30): the gate used to check the window
// only when a request entered, so a POST admitted a moment before Begin kept
// writing while the copy ran. Opening the window must wait for it.
func TestQuietWindowWaitsForAdmittedHTTPWrite(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	events := make(chan string, 2)
	heldInsideHandler := make(chan bool, 1)
	gate := quiesceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-resume
		heldInsideHandler <- quiesce.WritesHeld()
		events <- "write finished"
		w.WriteHeader(http.StatusNoContent)
	}))
	served := make(chan struct{})
	go func() {
		defer close(served)
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/workspaces/w/attachments", nil))
	}()
	<-entered

	type result struct {
		w   *quiesce.Window
		err error
	}
	begun := make(chan result, 1)
	go func() {
		w, err := quiesce.Default().Begin(context.Background(), quiesce.Options{HoldCap: time.Minute, BusyWait: time.Second})
		events <- "window held"
		begun <- result{w, err}
	}()
	waitForQuietWindow(t)
	close(resume)
	<-served
	r := <-begun
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.w.Release()
	if <-heldInsideHandler {
		t.Fatal("a mutating request admitted before Begin kept writing inside the held window")
	}
	if first := <-events; first != "write finished" {
		t.Fatalf("first event = %q: the window held before the in-flight write finished", first)
	}
	if !quiesce.WritesHeld() {
		t.Fatal("the window never held after the write drained")
	}
}

func TestQuiesceGateCountsWriters(t *testing.T) {
	upload := func() (*bytes.Buffer, string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", "a.txt")
		_, _ = fw.Write([]byte("hello"))
		_ = mw.Close()
		return &b, mw.FormDataContentType()
	}
	cases := []struct {
		name      string
		method    string
		path      string
		multipart bool
		wantCount bool
	}{
		{name: "PATCH is a writer", method: "PATCH", path: "/api/v1/missions/m1", wantCount: true},
		{name: "multipart upload is a writer", method: "POST", path: "/api/v1/workspaces/w/attachments", multipart: true, wantCount: true},
		{name: "inbound webhook is a writer", method: "POST", path: "/api/v1/webhooks/tok", wantCount: true},
		{name: "GET stream is not", method: "GET", path: "/api/v1/journal/stream"},
		{name: "HEAD download is not", method: "HEAD", path: "/api/v1/workspaces/w/attachments/a1"},
		{name: "OPTIONS is not", method: "OPTIONS", path: "/api/v1/missions"},
		{name: "backup endpoint is not", method: "POST", path: "/api/v1/admin/instance/backups/bundles/pin"},
		{name: "hold resume is not", method: "POST", path: "/api/v1/admin/instance/holds/resume"},
		{name: "sign-in is not", method: "POST", path: "/api/auth/callback/credentials"},
		{name: "traversal past the allowlist is", method: "POST", path: "/api/v1/admin/instance/backups/../../../credentials", wantCount: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := quiesce.Default().Writers()
			var inside int
			gate := quiesceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				inside = quiesce.Default().Writers()
				if tc.multipart {
					if _, _, err := r.FormFile("file"); err != nil {
						t.Errorf("upload body not readable through the gate: %v", err)
					}
				}
				w.WriteHeader(http.StatusTeapot)
			}))
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.multipart {
				b, ct := upload()
				req = httptest.NewRequest(tc.method, tc.path, b)
				req.Header.Set("Content-Type", ct)
			}
			rr := httptest.NewRecorder()
			gate.ServeHTTP(rr, req)
			if rr.Code != http.StatusTeapot {
				t.Fatalf("code = %d", rr.Code)
			}
			want := before
			if tc.wantCount {
				want = before + 1
			}
			if inside != want {
				t.Fatalf("writers inside the handler = %d, want %d", inside, want)
			}
			if after := quiesce.Default().Writers(); after != before {
				t.Fatalf("writers after the request = %d, want %d (leaked)", after, before)
			}
		})
	}
}

// A GET that streams for as long as it likes (SSE, websocket, a download)
// never holds a window's drain up.
func TestQuietWindowIgnoresOpenStreams(t *testing.T) {
	streaming := make(chan struct{})
	stop := make(chan struct{})
	gate := quiesceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(streaming)
		<-stop
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/journal/stream", nil))
	}()
	<-streaming
	w, err := quiesce.Default().Begin(context.Background(), quiesce.Options{HoldCap: time.Minute, DrainTimeout: 5 * time.Second})
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("an open GET stream blocked the window: %v", err)
	}
	w.Release()
}
