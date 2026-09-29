package ws

import (
	"context"
	"database/sql"
	_ "modernc.org/sqlite"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Compare the previous two-query decision with the single-snapshot hot path.
// Both arms use current DB state for every delivery; neither caches an allow.
func BenchmarkChatDeliveryAuthorization(b *testing.B) {
	db := testutil.MigratedSQLDB(b)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('bench-user','bench@access.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('bench-workspace','Bench','bench')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('bench-member','bench-workspace','bench-user','MEMBER')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('bench-agent','bench-workspace','Agent','bench-agent')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('bench-chat','bench-workspace','bench-agent','bench-user','private')`,
	} {
		if _, err := db.Exec(q); err != nil {
			b.Fatal(err)
		}
	}
	authorizer := NewDBChannelAuthorizer(db)
	for _, combined := range []bool{false, true} {
		name := "two_queries"
		if combined {
			name = "single_snapshot"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var allowed bool
				var err error
				if combined {
					allowed, err = authorizer.CanDeliver(b.Context(), "bench-user", "session:bench-chat")
				} else {
					restricted, e := (access.Store{DB: db}).HasRestrictedMembership(b.Context(), "bench-user")
					if e != nil || restricted {
						b.Fatalf("ceiling: %v %v", restricted, e)
					}
					allowed, err = chataudience.CanRead(b.Context(), db, "bench-chat", "bench-user")
				}
				if err != nil || !allowed {
					b.Fatalf("delivery denied: %v", err)
				}
			}
		})
	}
}

// BenchmarkCanSubscribe_Parse isolates the parse/dispatch portion of
// CanSubscribe by rejecting a malformed channel before any DB roundtrip.
// A global providers subscription now checks the resource ceiling; this measurement
// focuses on the per-call string parsing cost rather than sqlite latency.
func BenchmarkCanSubscribe_Parse(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	a := NewDBChannelAuthorizer(db)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if allowed, err := a.CanSubscribe(ctx, "user-123", "malformed"); allowed || err != nil {
			b.Fatalf("malformed channel: %v %v", allowed, err)
		}
	}
}
