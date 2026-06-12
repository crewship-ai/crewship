package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// validCUID is a workspace ID that passes looksLikeCUID (starts with 'c',
// lowercase alnum, >= 20 chars) so GetWorkspaceID never issues a resolve
// HTTP call during these tests.
const validCUID = "c1234567890123456789"

func TestStreamSSE_HappyPath(t *testing.T) {
	var gotAuth, gotLastID, gotAccept, gotWS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotLastID = r.Header.Get("Last-Event-ID")
		gotAccept = r.Header.Get("Accept")
		gotWS = r.URL.Query().Get("workspace_id")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 1\nevent: entry\ndata: first\n\nid: 2\ndata: second\n\n"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok-123", validCUID)
	var events []SSEEvent
	err := c.StreamSSE(context.Background(), "/api/v1/runs/r1/events", "ev-41", func(e SSEEvent) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamSSE: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
	if gotLastID != "ev-41" {
		t.Errorf("Last-Event-ID = %q, want ev-41", gotLastID)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotWS != validCUID {
		t.Errorf("workspace_id = %q, want %q", gotWS, validCUID)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(events), events)
	}
	if events[0].ID != "1" || events[0].Event != "entry" || events[0].Data != "first" {
		t.Errorf("event 0 = %+v", events[0])
	}
	if events[1].ID != "2" || events[1].Data != "second" {
		t.Errorf("event 1 = %+v", events[1])
	}
}

func TestStreamSSE_DoesNotOverrideExplicitWorkspaceID(t *testing.T) {
	var gotWS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWS = r.URL.Query().Get("workspace_id")
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", validCUID)
	err := c.StreamSSE(context.Background(), "/api/v1/events?workspace_id=explicit", "", func(SSEEvent) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StreamSSE: %v", err)
	}
	if gotWS != "explicit" {
		t.Errorf("workspace_id = %q, want explicit (must not be overridden)", gotWS)
	}
}

func TestStreamSSE_NoTokenNoWorkspace(t *testing.T) {
	var gotAuth, gotLastID string
	var hasWS bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotLastID = r.Header.Get("Last-Event-ID")
		_, hasWS = r.URL.Query()["workspace_id"]
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	if err := c.StreamSSE(context.Background(), "/api/v1/events", "", func(SSEEvent) error { return nil }); err != nil {
		t.Fatalf("StreamSSE: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization should be empty, got %q", gotAuth)
	}
	if gotLastID != "" {
		t.Errorf("Last-Event-ID should be empty, got %q", gotLastID)
	}
	if hasWS {
		t.Error("workspace_id should not be set when client has none")
	}
}

func TestStreamSSE_HandshakeNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	err := c.StreamSSE(context.Background(), "/api/v1/events", "", func(SSEEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "SSE handshake: status 500") {
		t.Errorf("err = %v, want handshake status 500", err)
	}
}

func TestStreamSSE_WrongContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	err := c.StreamSSE(context.Background(), "/api/v1/events", "", func(SSEEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unexpected content-type") {
		t.Errorf("err = %v, want unexpected content-type error", err)
	}
}

func TestStreamSSE_BadBaseURL(t *testing.T) {
	c := NewClient("http://example.com/\x00", "", "")
	err := c.StreamSSE(context.Background(), "/events", "", func(SSEEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "parse URL") {
		t.Errorf("err = %v, want parse URL error", err)
	}
}

func TestStreamSSE_ConnectError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused from here on

	c := NewClient(url, "", "")
	err := c.StreamSSE(context.Background(), "/events", "", func(SSEEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "connect:") {
		t.Errorf("err = %v, want connect error", err)
	}
}

func TestStreamSSE_OnEventErrorStopsStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: a\n\ndata: b\n\n"))
	}))
	defer srv.Close()

	boom := errors.New("stop here")
	calls := 0
	c := NewClient(srv.URL, "", "")
	err := c.StreamSSE(context.Background(), "/events", "", func(SSEEvent) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if calls != 1 {
		t.Errorf("onEvent called %d times, want 1", calls)
	}
}

func TestStreamSSE_InheritsCustomTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer srv.Close()

	used := false
	c := NewClient(srv.URL, "", "")
	c.HTTPClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultTransport.RoundTrip(r)
	})
	var got []SSEEvent
	if err := c.StreamSSE(context.Background(), "/events", "", func(e SSEEvent) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("StreamSSE: %v", err)
	}
	if !used {
		t.Error("custom transport was not used by the streaming client")
	}
	if len(got) != 1 || got[0].Data != "ok" {
		t.Errorf("events = %+v", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
