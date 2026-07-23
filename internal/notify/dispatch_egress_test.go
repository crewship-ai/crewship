package notify

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
// onto a minimal crews table so the dispatcher can resolve the crew allowlist.
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

// TestWebhookCrewEgressBlockedBeforeBytesLeave is the block-proof for #1367:
// a webhook fired by a RESTRICTED crew whose allowed_domains do not include
// the channel host is blocked BEFORE any request is sent — the receiver is
// never contacted. Before this fix notify used a bare http.Client that
// honored neither the SSRF guard nor the crew allowlist.
func TestWebhookCrewEgressBlockedBeforeBytesLeave(t *testing.T) {
	db := openEgressTestDB(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	d := NewDispatcher(staticLister{nil}, nil, nil, db)
	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "s", Enabled: true}

	err := d.DispatchOne(context.Background(), ch, NotificationEvent{
		Type:         EventRunCompleted,
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_blocked",
		RunID:        "run1",
	})
	if err == nil {
		t.Fatal("expected crew egress block, got nil error")
	}
	if !strings.Contains(err.Error(), "crew egress policy") {
		t.Errorf("error = %v, want crew egress policy reason", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("receiver contacted %d time(s); egress must block before bytes leave", n)
	}
}

// TestWebhookCrewEgressAllowed confirms the gate does not over-block: a free
// crew and a restricted crew that allowlists the host both deliver.
func TestWebhookCrewEgressAllowed(t *testing.T) {
	db := openEgressTestDB(t)

	for _, crew := range []string{"crew_free", "crew_allowed"} {
		t.Run(crew, func(t *testing.T) {
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(200)
			}))
			defer srv.Close()

			d := NewDispatcher(staticLister{nil}, nil, nil, db)
			ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "s", Enabled: true}

			if err := d.DispatchOne(context.Background(), ch, NotificationEvent{
				Type:         EventRunCompleted,
				WorkspaceID:  "ws_test",
				AuthorCrewID: crew,
				RunID:        "run1",
			}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n := atomic.LoadInt32(&hits); n != 1 {
				t.Errorf("receiver contacted %d time(s), want 1", n)
			}
		})
	}
}
