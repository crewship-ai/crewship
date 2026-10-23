package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// No verify request or refused append is needed: the scheduled full walk
// detects middle damage behind a healthy tail in either chain.
func TestAuditPeriodicVerificationAlerts(t *testing.T) {
	for _, scope := range []string{"instance", "workspace", "fallback", "healthy"} {
		t.Run(scope, func(t *testing.T) {
			f := newInstanceFixture(t)
			defer f.r.Shutdown()
			writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
			defer writer.Close()
			f.r.journal = writer
			for range 3 {
				if err := auditInstance(t.Context(), nil, f.db, "fixture.history", "instance", "", "", nil); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-old", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "History"}); err != nil {
					t.Fatal(err)
				}
			}
			switch scope {
			case "instance":
				execOrFatal(t, f.db, `UPDATE instance_audit_logs SET metadata='{"tampered":true}' WHERE chain_seq=2`)
			case "workspace":
				execOrFatal(t, f.db, `UPDATE journal_entries SET summary='tampered' WHERE workspace_id='ws-old' AND seq=2`)
			case "fallback":
				// A fallback can remain after repair even though every chained row verifies.
				execOrFatal(t, f.db, `INSERT INTO instance_audit_logs(id,action,entity_type,metadata,created_at) VALUES('fixture-fallback','fixture.revoke','USER','{"audit_chain":"unchained_fallback"}','2026-10-23T00:00:00Z')`)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); f.r.StartAuditVerification(ctx, 10*time.Millisecond) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("monitor did not stop")
				}
				drainAlerts(t)
			}()
			want, key, reason := 1, "chain_break:instance:seq:2", "post_upgrade_invalid"
			if scope == "workspace" {
				key = "chain_break:workspace:ws-old:seq:2"
			}
			if scope == "fallback" {
				key, reason = "chain_break:instance:seq:0", "unchained_fallback"
			}
			if scope == "healthy" {
				want = 0
			}
			deadline := time.Now().Add(5 * time.Second)
			for want > 0 && adminAlerts(t, f.db, "boss", key) == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			<-done
			drainAlerts(t)
			if n := adminAlerts(t, f.db, "boss", ""); n != want {
				t.Fatalf("inbox alerts=%d, want %d", n, want)
			}
			if want == 0 {
				return
			}
			if n := adminAlerts(t, f.db, "wsadmin", ""); n != 0 {
				t.Fatalf("non-instance admin received %d alerts", n)
			}
			var raw string
			if err := f.db.QueryRow(`SELECT metadata FROM instance_audit_logs WHERE action='audit.chain_break_detected' AND entity_id=?`, key).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
				t.Fatal(err)
			}
			if s, _ := metadata["reason"].(string); !strings.HasPrefix(s, reason) {
				t.Fatalf("reason %q, want %s", s, reason)
			}
			// Repeated timer ticks/sweeps retain a single incident and inbox item.
			if err := f.r.verifyAuditHistories(t.Context()); err != nil {
				t.Fatal(err)
			}
			drainAlerts(t)
			if chained, fallback := breakEvents(t, f.db, key); chained+fallback != 1 {
				t.Fatalf("duplicate events: %d/%d", chained, fallback)
			}
		})
	}
}
