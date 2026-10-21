package ws

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// Model concurrent per-client delivery pumps sharing the production-sized
// five-connection pool. One benchmark operation is one authorization frame.
func BenchmarkChatDeliveryAuthorization(b *testing.B) {
	for _, scale := range [][2]int{{1, 1}, {16, 8}, {64, 16}} {
		b.Run(fmt.Sprintf("clients%d/channels%d", scale[0], scale[1]), func(b *testing.B) {
			db, counter := deliveryAuthorizationFixture(b, scale[0], scale[1])
			auth := NewDBChannelAuthorizer(db)
			for _, kind := range []string{"workspace", "agent", "session"} {
				b.Run(kind, func(b *testing.B) {
					b.ReportAllocs()
					var next atomic.Int64
					var failed atomic.Bool
					var wg sync.WaitGroup
					start := make(chan struct{})
					for client := 0; client < scale[0]; client++ {
						wg.Add(1)
						go func(client int) {
							defer wg.Done()
							<-start
							for {
								i := next.Add(1) - 1
								if i >= int64(b.N) {
									return
								}
								channel := fmt.Sprintf("%s:bench-%s-%d", kind, kind, int(i)%scale[1])
								if kind == "session" {
									channel = fmt.Sprintf("session:bench-chat-%d-%d", client, int(i)%scale[1])
								}
								allowed, err := auth.CanDeliver(b.Context(), fmt.Sprintf("bench-user-%d", client), channel)
								if err != nil || !allowed {
									failed.Store(true)
									return
								}
							}
						}(client)
					}
					counter.Reset()
					before := db.Stats()
					b.ResetTimer()
					close(start)
					wg.Wait()
					b.StopTimer()
					after := db.Stats()
					if failed.Load() {
						b.Fatal("delivery denied")
					}
					b.ReportMetric(float64(counter.Count())/float64(b.N), "SQL/frame")
					b.ReportMetric(float64(after.WaitCount-before.WaitCount)/float64(b.N), "waits/frame")
					b.ReportMetric(float64(after.WaitDuration-before.WaitDuration)/float64(b.N), "wait-ns/frame")
				})
			}
		})
	}
}

// Every workload uses real registered migrations, native memberships, and
// private chats belonging to the corresponding client. No synthetic allow.
func deliveryAuthorizationFixture(t testing.TB, clients, channels int) (*sql.DB, *testutil.SQLCounter) {
	t.Helper()
	fixture := testutil.MigratedDB(t)
	tx, err := fixture.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for client := 0; client < clients; client++ {
		exec(`INSERT INTO users(id,email) VALUES(?,?)`, fmt.Sprintf("bench-user-%d", client), fmt.Sprintf("bench%d@access.test", client))
	}
	for channel := 0; channel < channels; channel++ {
		workspace := fmt.Sprintf("bench-workspace-%d", channel)
		agent := fmt.Sprintf("bench-agent-%d", channel)
		exec(`INSERT INTO workspaces(id,name,slug) VALUES(?, 'Bench',?)`, workspace, workspace)
		exec(`INSERT INTO agents(id,workspace_id,name,slug) VALUES(?,?,'Agent',?)`, agent, workspace, agent)
		for client := 0; client < clients; client++ {
			user := fmt.Sprintf("bench-user-%d", client)
			exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, fmt.Sprintf("bench-member-%d-%d", client, channel), workspace, user)
			exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, fmt.Sprintf("bench-chat-%d-%d", client, channel), workspace, agent, user)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return testutil.CountingSQLDB(t, fixture.Path(), 5)
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
