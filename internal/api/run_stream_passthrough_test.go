package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

// Session-channel broadcasts that are not chat events — a delegated
// assignment, an escalation, a peer query, an exposed port — reached the NDJSON
// stream as a bare type name. The browser receives the same frame WITH its
// payload (which assignment, which target, the exposed URL), so a stream
// reader learned that something happened and never what. The payload now
// rides in `metadata`, through the same cost redaction as every other frame.
func TestRunStreamWriter_PassesBroadcastPayloadThrough(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		payload any
		want    map[string]string
	}{
		{"assignment", "assignment_created", map[string]string{"id": "as_1", "target": "lucy", "task": "summarise"},
			map[string]string{"id": "as_1", "target": "lucy", "task": "summarise"}},
		{"escalation", "escalation_created", map[string]string{"id": "es_1", "from": "max", "reason": "need a key"},
			map[string]string{"from": "max", "reason": "need a key"}},
		{"peer query", "peer_query_running", map[string]string{"id": "pq_1", "from": "max", "target": "lucy"},
			map[string]string{"target": "lucy"}},
		{"port", "port_expose_created", map[string]any{"id": "pe_1", "url": "https://p.example.invalid", "port": 3000},
			map[string]string{"url": "https://p.example.invalid"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s := &runStreamWriter{w: rec, flusher: rec}
			data, _ := json.Marshal(ws.ServerMessage{Type: tc.typ, Channel: "session:c_1", Payload: tc.payload})
			if s.writeHubFrame(data) {
				t.Fatal("a broadcast must not terminate the run")
			}
			var frame struct {
				Type     string         `json:"type"`
				Metadata map[string]any `json:"metadata"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &frame); err != nil {
				t.Fatalf("frame %q: %v", rec.Body.String(), err)
			}
			if frame.Type != tc.typ {
				t.Fatalf("type = %q, want %q", frame.Type, tc.typ)
			}
			for k, v := range tc.want {
				if frame.Metadata[k] != v {
					t.Errorf("metadata[%s] = %v, want %q (frame %s)", k, frame.Metadata[k], v, rec.Body.String())
				}
			}
		})
	}
}

func TestRunStreamWriter_PassthroughPayloadIsCostRedacted(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &runStreamWriter{w: rec, flusher: rec, redactCosts: true}
	data, _ := json.Marshal(ws.ServerMessage{Type: "assignment_completed", Channel: "session:c_1",
		Payload: map[string]any{"id": "as_1", "total_cost_usd": 4.2}})
	s.writeHubFrame(data)
	if strings.Contains(rec.Body.String(), "total_cost_usd") {
		t.Fatalf("cost survived redaction: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"as_1"`) {
		t.Fatalf("redaction dropped the rest of the payload: %s", rec.Body.String())
	}
}
