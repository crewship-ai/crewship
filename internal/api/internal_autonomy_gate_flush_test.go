package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCapturedResponse_FlushPinsTheContentType covers the two things a
// response replayed from a captured handler must not do.
//
// The first is a duplicate header. flush used to Add every captured key, but
// the outer middleware has already written its own onto the real writer by the
// time a handler runs — so a captured Content-Type produced two of them, and
// which one a client honours is not a decision to leave to a client.
//
// The second is the reason CodeQL reads this function as reflected XSS: the
// body is bytes that trace back to a request, so a Content-Type the captured
// handler could influence is the only thing between them and a browser parsing
// them as markup. These routes are internal JSON and unreachable from a
// browser, and SecurityHeaders sets nosniff on every API route — but this
// function should not depend on middleware ORDER for that, which is exactly
// the class of assumption this change exists to stop relying on.
func TestCapturedResponse_FlushPinsTheContentType(t *testing.T) {
	tests := []struct {
		name     string
		captured string // Content-Type the inner handler set
		preset   string // Content-Type already on the real writer
	}{
		{name: "handler set none", captured: "", preset: ""},
		{name: "handler set json", captured: "application/json", preset: ""},
		{
			// The one that matters: a captured html Content-Type must not
			// reach the wire, whatever put it there.
			name: "handler set html", captured: "text/html; charset=utf-8", preset: "",
		},
		{name: "middleware preset, handler silent", captured: "", preset: "application/json"},
		{name: "middleware preset, handler set html", captured: "text/html", preset: "application/json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCapturedResponse()
			if tt.captured != "" {
				c.Header().Set("Content-Type", tt.captured)
			}
			c.Header().Set("X-Custom", "kept")
			c.WriteHeader(http.StatusCreated)
			_, _ = c.Write([]byte(`{"id":"sched_1"}`))

			rr := httptest.NewRecorder()
			if tt.preset != "" {
				rr.Header().Set("Content-Type", tt.preset)
			}
			c.flush(rr)

			if got := rr.Header().Values("Content-Type"); len(got) != 1 {
				t.Fatalf("Content-Type appears %d time(s) (%v), want exactly 1", len(got), got)
			}
			if got := rr.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := rr.Header().Get("X-Custom"); got != "kept" {
				t.Errorf("an unrelated captured header was dropped: X-Custom = %q", got)
			}
			if rr.Code != http.StatusCreated {
				t.Errorf("status = %d, want 201 — flush must replay the captured status", rr.Code)
			}
			if rr.Body.String() != `{"id":"sched_1"}` {
				t.Errorf("body = %q, want the captured body verbatim", rr.Body.String())
			}
		})
	}
}

// TestCapturedResponse_FlushKeepsMultiValuedHeaders guards the over-correction:
// switching the first value of each key from Add to Set must not collapse a
// header that legitimately repeats.
func TestCapturedResponse_FlushKeepsMultiValuedHeaders(t *testing.T) {
	c := newCapturedResponse()
	c.Header().Add("Set-Cookie", "a=1")
	c.Header().Add("Set-Cookie", "b=2")
	c.WriteHeader(http.StatusOK)

	rr := httptest.NewRecorder()
	c.flush(rr)

	// The values, not the count. Counting alone passes a replay that emits
	// "a=1" twice and drops "b=2" — which is exactly the bug the Set-then-Add
	// split could introduce, so counting would miss the one thing this test
	// exists to catch.
	got := rr.Header().Values("Set-Cookie")
	want := []string{"a=1", "b=2"}
	if len(got) != len(want) {
		t.Fatalf("Set-Cookie = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Set-Cookie[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
