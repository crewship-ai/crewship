package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamNDJSON_DeliversOneLinePerRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, "{\"type\":\"stream.open\"}\n")
		fmt.Fprint(w, "\n") // blank lines are framing noise, not records
		fmt.Fprint(w, "{\"type\":\"text\",\"content\":\"hi\"}\n")
		fmt.Fprint(w, "{\"type\":\"stream.end\"}") // no trailing newline
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws1")
	var got []string
	err := c.StreamNDJSON(context.Background(), "/api/v1/chats/c1/stream", "", func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("StreamNDJSON: %v", err)
	}
	want := []string{`{"type":"stream.open"}`, `{"type":"text","content":"hi"}`, `{"type":"stream.end"}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %v, want %v — a record with no trailing newline must still be delivered", got, want)
	}
}

// A failed handshake must arrive as a *APIError so the status reaches the CLI
// exit-code contract (404 → ExitNotFound). A bare error string would collapse
// every failure into exit 1.
func TestStreamNDJSON_SurfacesHandshakeStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"detail":"chat not found"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws1")
	err := c.StreamNDJSON(context.Background(), "/api/v1/chats/nope/stream", "", func([]byte) error { return nil })
	if err == nil {
		t.Fatal("want an error for a 404 handshake")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v (%T), want a *APIError carrying status 404", err, err)
	}
	if got := ExitCodeFor(err); got != ExitNotFound {
		t.Errorf("ExitCodeFor = %d, want ExitNotFound (%d)", got, ExitNotFound)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("message %q does not name the status", err.Error())
	}
}

// A server answering JSON instead of NDJSON is a version-skew signal (an old
// build with no stream route behind a proxy that 200s). Fail loudly rather
// than feed the caller half a document line by line.
func TestStreamNDJSON_RejectsWrongContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{}")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws1")
	err := c.StreamNDJSON(context.Background(), "/api/v1/chats/c1/stream", "", func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "content-type") {
		t.Fatalf("err = %v, want a content-type mismatch error", err)
	}
}

func TestStreamNDJSON_SendsLastEventIDAndStopsOnCallbackError(t *testing.T) {
	var gotLastEventID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLastEventID = r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, "{\"seq\":1}\n{\"seq\":2}\n")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws1")
	stop := fmt.Errorf("enough")
	seen := 0
	err := c.StreamNDJSON(context.Background(), "/api/v1/chats/c1/stream", "7", func([]byte) error {
		seen++
		return stop
	})
	if err != stop {
		t.Fatalf("err = %v, want the callback's error returned verbatim", err)
	}
	if seen != 1 {
		t.Errorf("callback called %d times, want 1 — an error must stop the read loop", seen)
	}
	if gotLastEventID != "7" {
		t.Errorf("Last-Event-ID = %q, want 7", gotLastEventID)
	}
}
