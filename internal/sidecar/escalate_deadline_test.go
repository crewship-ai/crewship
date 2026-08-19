package sidecar

// The agent side of the escalation deadline.
//
// The defect: handleEscalate waited a hardcoded 300 s, gave up, and returned
// {"status":"TIMEOUT","resolution":""} — which reads to a model exactly like a
// question answered with silence. Meanwhile crewshipd's row stayed PENDING
// forever, because nothing on this path ever told it the agent had stopped
// waiting. Two clocks, one of them invisible to the other.
//
// The fix makes the server authoritative: it publishes the window on the
// create, this client waits on THAT, and the server's terminal answer is what
// the agent hears. These tests hold that contract from the client's side.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestServerWaitWindow — the client's wait is derived from the server's
// answer, never from a constant of its own, except when the server did not
// publish one.
func TestServerWaitWindow(t *testing.T) {
	cases := []struct {
		name   string
		create map[string]interface{}
		want   time.Duration
		// approx allows the deadline_at cases a little slack for the time
		// between constructing the fixture and reading it.
		approx time.Duration
	}{
		{
			name:   "timeout_seconds is preferred — it needs no agreement about clocks",
			create: map[string]interface{}{"timeout_seconds": float64(45), "deadline_at": time.Now().Add(9 * time.Hour).Format(time.RFC3339)},
			want:   45 * time.Second,
		},
		{
			name:   "deadline_at is the fallback when only the instant is published",
			create: map[string]interface{}{"deadline_at": time.Now().Add(90 * time.Second).Format(time.RFC3339)},
			want:   90 * time.Second,
			approx: 3 * time.Second,
		},
		{
			name:   "a deadline already in the past still polls, briefly — the server answers EXPIRED at once",
			create: map[string]interface{}{"deadline_at": time.Now().Add(-time.Hour).Format(time.RFC3339)},
			want:   time.Second,
		},
		{
			name:   "an older server that publishes nothing keeps the historical window",
			create: map[string]interface{}{"escalation_id": "esc-1", "status": "PENDING"},
			want:   escalateWaitFallback,
		},
		{
			name:   "a nonsense deadline is not a deadline",
			create: map[string]interface{}{"deadline_at": "tomorrow-ish"},
			want:   escalateWaitFallback,
		},
		{
			name:   "a non-positive window is ignored rather than making the wait instantaneous",
			create: map[string]interface{}{"timeout_seconds": float64(0)},
			want:   escalateWaitFallback,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := serverWaitWindow(tc.create)
			slack := tc.approx
			if slack == 0 {
				slack = 50 * time.Millisecond
			}
			if got < tc.want-slack || got > tc.want+slack {
				t.Errorf("serverWaitWindow = %s, want ~%s", got, tc.want)
			}
		})
	}
}

// TestEscalateSurfacesServerExpiry — the whole point. crewshipd decides the
// question expired; the agent must be told that, with a warning, rather than
// being handed an empty resolution and left to infer.
func TestEscalateSurfacesServerExpiry(t *testing.T) {
	var waitCalls int
	crewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/internal/escalations":
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			// A tiny window, published the way the real server publishes it.
			_, _ = w.Write([]byte(`{"escalation_id":"esc-exp","status":"PENDING","timeout_seconds":1,` +
				`"deadline_at":"` + time.Now().UTC().Add(time.Second).Format(time.RFC3339) + `"}`))
		case strings.HasSuffix(r.URL.Path, "/wait"):
			waitCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"EXPIRED","resolution":"","warning":"No human answered before the deadline.",` +
				`"agent_action":"continued_with_warning"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer crewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: crewshipd.URL, Token: "secret-token", AgentSlug: "nela",
		CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1",
	}, []CrewMember{{Slug: "nela"}})

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela","reason":"can I drop the table?"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleEscalate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if waitCalls != 1 {
		t.Fatalf("the wait endpoint was called %d times, want 1", waitCalls)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if got["status"] != "EXPIRED" {
		t.Errorf("status = %v, want EXPIRED — the server's terminal answer must reach the agent unchanged", got["status"])
	}
	if warn, _ := got["warning"].(string); warn == "" {
		t.Error("the agent was handed an empty resolution with no warning — that is the silent continuation this closes")
	}
	if got["escalation_id"] != "esc-exp" {
		t.Errorf("escalation_id = %v, want esc-exp", got["escalation_id"])
	}
}

// TestEscalateGiveUpCarriesAWarning — when this client stops waiting without
// hearing a terminal answer (dropped connection, 5xx, unreadable body) it does
// not know the outcome, so it still says TIMEOUT. What it must not do is say
// so silently.
func TestEscalateGiveUpCarriesAWarning(t *testing.T) {
	crewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/internal/escalations":
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"escalation_id":"esc-boom","status":"PENDING","timeout_seconds":1}`))
		case strings.HasSuffix(r.URL.Path, "/wait"):
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer crewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: crewshipd.URL, Token: "secret-token", AgentSlug: "nela",
		CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1",
	}, []CrewMember{{Slug: "nela"}})

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela","reason":"anything"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleEscalate(w, req)

	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if got["status"] != "TIMEOUT" {
		t.Errorf("status = %v, want TIMEOUT — this client did not learn the outcome and must not claim it did", got["status"])
	}
	if warn, _ := got["warning"].(string); warn == "" {
		t.Error("a give-up with no warning is the original defect: an empty resolution that reads as an answer")
	}
}

// A human answering inside the window still wins — the deadline must not steal
// a real decision.
func TestEscalateStillDeliversAnAnswer(t *testing.T) {
	crewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/internal/escalations":
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"escalation_id":"esc-ok","status":"PENDING","timeout_seconds":30}`))
		case strings.HasSuffix(r.URL.Path, "/wait"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"RESOLVED","resolution":"go ahead","action":"approve"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer crewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: crewshipd.URL, Token: "secret-token", AgentSlug: "nela",
		CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1",
	}, []CrewMember{{Slug: "nela"}})

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela","reason":"ship it?"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleEscalate(w, req)

	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if got["status"] != "RESOLVED" || got["resolution"] != "go ahead" {
		t.Errorf("got %v, want the human's answer intact", got)
	}
	if _, hasWarning := got["warning"]; hasWarning {
		t.Error("a real answer must not carry a no-answer warning")
	}
}
