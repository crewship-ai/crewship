package composio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A non-2xx Composio response must surface as a typed *APIError carrying the
// upstream status and a bounded body snippet, so callers (the API handler) can
// classify the failure instead of collapsing everything into an opaque 502
// (issue #1192). Error() keeps the historical "status NNN: <body>" shape that
// CreateMCPServer's invalid-tools retry matches on.
func TestClient_Non2xxReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"API key is invalid","error_code":10001}}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient("ak_bad", srv.URL)
	_, err := c.ListToolkits(context.Background(), "", "", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error is %T (%v), want *APIError", err, err)
	}
	if ae.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", ae.StatusCode)
	}
	if got := ae.Detail(); got != "API key is invalid" {
		t.Errorf("Detail() = %q, want %q", got, "API key is invalid")
	}
	if msg := err.Error(); !strings.Contains(msg, "status 401") || !strings.Contains(msg, "API key is invalid") {
		t.Errorf("Error() = %q, want the historical status+body shape", msg)
	}
}

func TestAPIError_Detail(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"nested error.message", `{"error":{"message":"bad key"}}`, "bad key"},
		{"flat message", `{"message":"nope"}`, "nope"},
		{"string error", `{"error":"denied"}`, "denied"},
		{"non-JSON falls back to sanitized excerpt", "upstream\nexploded\there", "upstream exploded here"},
		{"empty body", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ae := &APIError{StatusCode: 502, Method: "GET", Path: "/x", Snippet: tc.body}
			if got := ae.Detail(); got != tc.want {
				t.Errorf("Detail() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAPIError_DetailTruncatesAndStripsControlChars(t *testing.T) {
	long := strings.Repeat("x", 500)
	ae := &APIError{StatusCode: 500, Snippet: long}
	got := ae.Detail()
	if want := strings.Repeat("x", 200) + "…"; got != want {
		t.Errorf("Detail() len=%d, want 200-rune excerpt with ellipsis", len([]rune(got)))
	}

	ae = &APIError{StatusCode: 500, Snippet: "a\x00b\rc\nd"}
	if got := ae.Detail(); got != "a b c d" {
		t.Errorf("Detail() = %q, want control chars collapsed to spaces", got)
	}
}

// TransportReason must describe the connection-level failure without echoing
// the full request URL (which could carry query params).
func TestTransportReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // now unreachable

	c := NewClient("ak", base)
	_, err := c.ListToolkits(context.Background(), "secret-search-term", "", 1)
	if err == nil {
		t.Fatal("expected transport error against closed server")
	}
	var ae *APIError
	if errors.As(err, &ae) {
		t.Fatalf("transport failure must not be an *APIError, got %v", err)
	}
	reason := TransportReason(err)
	if reason == "" {
		t.Fatal("TransportReason returned empty string")
	}
	if strings.Contains(reason, "secret-search-term") {
		t.Errorf("TransportReason leaked the request query: %q", reason)
	}
	if !strings.Contains(reason, "connection refused") && !strings.Contains(reason, "connect") {
		t.Errorf("TransportReason = %q, want a dial-level description", reason)
	}
}
