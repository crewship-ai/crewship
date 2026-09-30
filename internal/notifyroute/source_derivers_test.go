package notifyroute

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

// A producer with its own durable record (the backup scheduler's incidents)
// registers a SourceDeriver; the recovery sweep then retries its rows through
// the same channel lookup, delivery and log as every other row.
func TestRecovery_RegisteredSourceDeriver(t *testing.T) {
	const kind = "test_source_deriver"
	cases := []struct {
		name       string
		derive     SourceDeriver
		wantSent   int
		wantPosts  int
		wantStatus string
		wantError  string
	}{
		{
			name: "derived message is delivered and marked sent",
			derive: func(_ context.Context, _ *sql.DB, d Delivery) (notify.CategoryMessage, error) {
				return notify.CategoryMessage{Body: "rebuilt from " + d.SourceID}, nil
			},
			wantSent: 1, wantPosts: 1, wantStatus: StatusSent,
		},
		{
			name: "a gone source ages out with the deriver's reason",
			derive: func(context.Context, *sql.DB, Delivery) (notify.CategoryMessage, error) {
				return notify.CategoryMessage{}, fmt.Errorf("incident was resolved: %w", sql.ErrNoRows)
			},
			wantStatus: StatusFailed, wantError: "recovery: incident was resolved",
		},
		{
			name: "a transient error leaves the row untouched for the next sweep",
			derive: func(context.Context, *sql.DB, Delivery) (notify.CategoryMessage, error) {
				return notify.CategoryMessage{}, errors.New("database is locked")
			},
			wantStatus: StatusPending,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newRouteTestDB(t)
			rs := newRecordingWebhookServer(t)
			r := newTestRouter(db, nil, nil)
			ch := seedWebhookChannel(t, db, rs.URL)
			RegisterSourceDeriver(kind, tc.derive)
			id := insertStuckDelivery(t, r, ch, kind, "src-1", notify.CategorySystemHealth, StatusPending)

			_, sent := r.RecoverStuckDeliveries(context.Background())
			if sent != tc.wantSent {
				t.Fatalf("sent = %d, want %d", sent, tc.wantSent)
			}
			if got := rs.count(); got != tc.wantPosts {
				t.Fatalf("posts = %d, want %d", got, tc.wantPosts)
			}
			status, _ := deliveryStatus(t, r, id)
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			if tc.wantPosts > 0 {
				rs.mu.Lock()
				post := rs.posts[0]
				rs.mu.Unlock()
				// The stored title stands in for an empty derived one.
				if post["title"] != "Approve" || post["body"] != "rebuilt from src-1" {
					t.Fatalf("delivered %v", post)
				}
			}
			if tc.wantError != "" {
				var msg string
				_ = db.QueryRow(`SELECT COALESCE(error,'') FROM notification_deliveries WHERE id = ?`, id).Scan(&msg)
				if !strings.HasPrefix(msg, tc.wantError) {
					t.Fatalf("error = %q, want prefix %q", msg, tc.wantError)
				}
			}
		})
	}
}
