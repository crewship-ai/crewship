package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// TestAgentPeers_InternalErrorsDoNotLeak pins that every 500 the peer-card
// endpoints return carries a generic message, while the underlying Go
// error (driver text, server filesystem paths) goes only to the log.
// Before this change the handlers replied with err.Error(), which exposed
// "sql: database is closed" and absolute host paths under the storage
// base to any authenticated caller.
func TestAgentPeers_InternalErrorsDoNotLeak(t *testing.T) {
	// cardPath is where the handlers resolve the u1 card for agent a1.
	cardPath := func(r *peerTestRig) string {
		paths := memory.PeerPaths{
			AgentDir: filepath.Join(r.output, "crews", r.crewID, "agents", "alice", ".memory"),
		}
		return paths.CardPath(memory.UserSlug(r.userID, r.wsID))
	}
	// occupyCardPathWithDir puts a non-empty directory where the card file
	// should be, so reading it fails with "is a directory" and removing it
	// fails with "directory not empty".
	occupyCardPathWithDir := func(t *testing.T, r *peerTestRig) {
		t.Helper()
		p := cardPath(r)
		if err := os.MkdirAll(filepath.Join(p, "child"), 0o755); err != nil {
			t.Fatalf("occupy card path: %v", err)
		}
	}
	closeDB := func(t *testing.T, r *peerTestRig) {
		t.Helper()
		if err := r.db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}
	}

	cases := []struct {
		name   string
		break_ func(t *testing.T, r *peerTestRig)
		call   func(r *peerTestRig, w http.ResponseWriter, req *http.Request)
		method string
		// leaks are fragments of the wrapped error that must not reach
		// the client.
		leaks []string
	}{
		{
			name:   "list resolve agent db failure",
			break_: closeDB,
			call:   func(r *peerTestRig, w http.ResponseWriter, req *http.Request) { r.h.ListAgentPeers(w, req) },
			method: http.MethodGet,
			leaks:  []string{"database is closed", "sql:"},
		},
		{
			name:   "get resolve agent db failure",
			break_: closeDB,
			call:   func(r *peerTestRig, w http.ResponseWriter, req *http.Request) { r.h.GetAgentPeer(w, req) },
			method: http.MethodGet,
			leaks:  []string{"database is closed", "sql:"},
		},
		{
			name:   "get load card filesystem failure",
			break_: occupyCardPathWithDir,
			call:   func(r *peerTestRig, w http.ResponseWriter, req *http.Request) { r.h.GetAgentPeer(w, req) },
			method: http.MethodGet,
			leaks:  []string{"is a directory", ".memory", "peers/"},
		},
		{
			name:   "delete resolve agent db failure",
			break_: closeDB,
			call:   func(r *peerTestRig, w http.ResponseWriter, req *http.Request) { r.h.DeleteAgentPeer(w, req) },
			method: http.MethodDelete,
			leaks:  []string{"database is closed", "sql:"},
		},
		{
			name:   "delete card filesystem failure",
			break_: occupyCardPathWithDir,
			call:   func(r *peerTestRig, w http.ResponseWriter, req *http.Request) { r.h.DeleteAgentPeer(w, req) },
			method: http.MethodDelete,
			leaks:  []string{"not empty", "peers: remove", ".memory"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := peerTestSetup(t)
			var logBuf bytes.Buffer
			r.h.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
			tc.break_(t, r)

			rec := httptest.NewRecorder()
			tc.call(r, rec, r.req(t, tc.method, "", map[string]string{
				"agentId": r.agentID, "userId": r.userID,
			}))

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
			}
			msg, ok := body["error"].(string)
			if !ok || msg == "" {
				t.Fatalf("body has no error string: %s", rec.Body.String())
			}
			raw := rec.Body.String()
			for _, leak := range append(tc.leaks, r.output) {
				if strings.Contains(raw, leak) {
					t.Errorf("response leaks internal error fragment %q: %s", leak, raw)
				}
			}
			// The detail must still reach operators through the log.
			if !strings.Contains(logBuf.String(), tc.leaks[0]) {
				t.Errorf("log does not carry the underlying error %q: %s", tc.leaks[0], logBuf.String())
			}
		})
	}
}
