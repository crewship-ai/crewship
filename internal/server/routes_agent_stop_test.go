package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #2879: the daemon's stop route answers success for an agent confirmed idle
// and a coded refusal when idleness cannot be established.
func TestHandleAgentStop_Outcomes(t *testing.T) {
	cases := []struct {
		name       string
		seed       map[string]string // agent_runs key -> raw record
		wantStatus int
		want       map[string]string
	}{
		{
			name:       "idle_agent_already_stopped",
			wantStatus: http.StatusOK,
			want:       map[string]string{"agent_id": "a1", "status": "stopped", "outcome": "already_stopped"},
		},
		{
			name:       "finished_runs_only_already_stopped",
			seed:       map[string]string{"r1": `{"id":"r1","agent_id":"a1","status":"completed"}`},
			wantStatus: http.StatusOK,
			want:       map[string]string{"outcome": "already_stopped"},
		},
		{
			name:       "unattributable_record_not_confirmed",
			seed:       map[string]string{"bad": `not json`},
			wantStatus: http.StatusServiceUnavailable,
			want:       map[string]string{"error": "runtime stop not confirmed", "code": "stop_not_confirmed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			for k, v := range tc.seed {
				if err := s.state.Set(context.Background(), "agent_runs", k, []byte(v)); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			s.ipcMux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/agents/a1/stop", nil))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", w.Code, tc.wantStatus, w.Body.String())
			}
			body := parseJSON(t, w.Body.Bytes())
			for k, v := range tc.want {
				if body[k] != v {
					t.Errorf("%s = %v, want %q", k, body[k], v)
				}
			}
		})
	}
}
