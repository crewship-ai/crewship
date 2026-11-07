package backup

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/credhostgate"
)

// #5045: allowed_hosts is validated on restore. A readable list is stored in
// canonical form; anything else is stored unreadable, which delivery treats as
// "withhold" - never as "no restriction".
func TestRestoreDump_AllowedHostsValidated(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "ahosts")
	userID := "u_cov_ahosts"

	cases := []struct {
		id, bundle string
		wantStored string // "" = must be unreadable-bound
	}{
		{"h_canon", `["API.GitHub.com.","api.github.com","b.example.org"]`, `["api.github.com","b.example.org"]`},
		{"h_empty", `[]`, `[]`},
		{"h_blank", ``, ``},
		{"h_nonjson", `not-json`, ""},
		{"h_wildcard_all", `["*"]`, ""},
		{"h_bad_entry", `["ok.example.com","bad host"]`, ""},
		{"h_wrong_type", `{"a":1}`, ""},
	}
	rows := make([]map[string]any, 0, len(cases))
	for _, c := range cases {
		r := credBundleRow(c.id, wsID, userID, float64(2))
		r["allowed_hosts"] = c.bundle
		rows = append(rows, r)
	}
	if _, err := RestoreDumpTxHooks(ctx, db, &DBDump{
		WorkspaceID: wsID,
		Tables:      map[string][]map[string]any{"credentials": rows},
	}, nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, c := range cases {
		var stored sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT allowed_hosts FROM credentials WHERE id = ?`, c.id).Scan(&stored); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if c.wantStored != "" || c.bundle == "" {
			if stored.String != c.wantStored {
				t.Errorf("%s: stored %q, want %q", c.id, stored.String, c.wantStored)
			}
			continue
		}
		if _, bound, readable := credhostgate.Bound(stored.String); !bound || readable {
			t.Errorf("%s: stored %q must be bound and unreadable (fail closed), got bound=%v readable=%v", c.id, stored.String, bound, readable)
		}
	}
}
