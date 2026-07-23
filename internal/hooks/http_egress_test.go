package hooks

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

// openEgressTestDB layers the crew network-policy columns (migration v18)
// onto a minimal crews table so httpHandler can resolve the crew allowlist.
func openEgressTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE crews (
	id             TEXT PRIMARY KEY,
	network_mode   TEXT NOT NULL DEFAULT 'free',
	allowed_domains TEXT,
	deleted_at     TEXT
);
INSERT INTO crews (id, network_mode, allowed_domains) VALUES ('crew_free', 'free', NULL);
INSERT INTO crews (id, network_mode, allowed_domains) VALUES ('crew_blocked', 'restricted', '["api.partner.com"]');
INSERT INTO crews (id, network_mode, allowed_domains) VALUES ('crew_allowed', 'restricted', '["127.0.0.1"]');`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// TestHTTPHandlerCrewEgressBlockedBeforeBytesLeave is the block-proof for
// #1367: a hook authored by a RESTRICTED crew whose allowed_domains do not
// include the target host is blocked BEFORE the request is dispatched — the
// receiver is never contacted. This closes the gap where hooks honored only
// the SSRF guard, letting a domain-locked crew reach any public host.
func TestHTTPHandlerCrewEgressBlockedBeforeBytesLeave(t *testing.T) {
	// Loopback is opt-in for the SSRF guard; enabling it proves the *crew
	// allowlist* (not the SSRF dialer) is what blocks below.
	t.Setenv(allowPrivateEnvVar, "true")
	db := openEgressTestDB(t)

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	defer ts.Close()

	h := Hook{
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
	}
	res, err := httpHandler(context.Background(), db, h, EventContext{
		WorkspaceID: "ws_test",
		CrewID:      "crew_blocked",
	})
	if err == nil {
		t.Fatal("expected crew egress block, got nil error")
	}
	if res.Outcome != OutcomeBlock {
		t.Errorf("outcome = %s, want Block", res.Outcome)
	}
	if !strings.Contains(res.Message, "crew egress policy") {
		t.Errorf("message = %q, want crew egress policy reason", res.Message)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("receiver was contacted %d time(s); egress must block before bytes leave", n)
	}
}

// TestHTTPHandlerCrewEgressAllowed confirms the gate does not over-block: a
// free crew and a restricted crew that allowlists the host both reach the
// receiver, so the allowlist layer never breaks legitimate hooks.
func TestHTTPHandlerCrewEgressAllowed(t *testing.T) {
	t.Setenv(allowPrivateEnvVar, "true")
	db := openEgressTestDB(t)

	for _, crew := range []string{"crew_free", "crew_allowed"} {
		t.Run(crew, func(t *testing.T) {
			var hits int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(200)
			}))
			defer ts.Close()

			h := Hook{
				HandlerKind:   HandlerKindHTTP,
				HandlerConfig: map[string]any{"url": ts.URL},
			}
			res, err := httpHandler(context.Background(), db, h, EventContext{
				WorkspaceID: "ws_test",
				CrewID:      crew,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Outcome != OutcomePass {
				t.Errorf("outcome = %s, want Pass", res.Outcome)
			}
			if n := atomic.LoadInt32(&hits); n != 1 {
				t.Errorf("receiver contacted %d time(s), want 1", n)
			}
		})
	}
}
