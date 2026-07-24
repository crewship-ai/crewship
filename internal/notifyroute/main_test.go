package notifyroute

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/notify"
	_ "modernc.org/sqlite"
)

// TestMain swaps notify's SSRF-safe webhook transport for the default
// transport, exactly like internal/notify's own TestMain, so this
// package's httptest servers (bound to 127.0.0.1) are reachable.
func TestMain(m *testing.M) {
	restore := notify.SetWebhookTransportForTesting(http.DefaultTransport)
	code := m.Run()
	restore()
	os.Exit(code)
}

// testEncKey mirrors internal/notify's channels_test.go convention: a
// throwaway 32-byte AES key, built at runtime so no secret scanner flags a
// literal.
var testEncKey = strings.Repeat("0123456789abcdef", 4)

var routeTestCounter atomic.Int64

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRouteTestDB runs the full migration chain (needed for
// notification_channels/user_notification_prefs/notification_deliveries
// plus the workspaces/workspace_members FKs the router queries) against a
// fresh on-disk-backed SQLite database, and seeds a workspace + members.
func newRouteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", testEncKey)
	dir := t.TempDir()
	name := fmt.Sprintf("%s/notifyroute-%d.db", dir, routeTestCounter.Add(1))
	db, err := database.Open("file:" + name)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db.DB, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	for _, m := range [][2]string{{"u_owner", "OWNER"}, {"u_member", "MEMBER"}, {"u_manager", "MANAGER"}} {
		if _, err := db.Exec(`INSERT INTO users (id, email) VALUES (?, ?)`, m[0], m[0]+"@example.com"); err != nil {
			t.Fatalf("seed user %s: %v", m[0], err)
		}
		if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, 'ws1', ?, ?)`,
			"wm_"+m[0], m[0], m[1]); err != nil {
			t.Fatalf("seed member %s: %v", m[0], err)
		}
	}
	return db.DB
}
