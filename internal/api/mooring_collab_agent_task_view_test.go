package api

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Canonical SQL and original source fixtures, synthetic ingress/BOOTTIME and
// confirmed runtime. This does not claim a dispatched agent or browser proof.
func TestCollaborationAgentTaskViewRequiresAttachedMandate(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	read := func(want int) {
		t.Helper()
		x.tx(t, func(tx *sql.Tx) {
			views, err := x.store.currentTaskViews(x.human, tx, x.key, x.f.a.ChatID)
			if err != nil || len(views) != want {
				t.Fatalf("views=%+v err=%v want=%d", views, err, want)
			}
		})
	}
	read(0) // A standing write grant is not a task.
	pending := x.prepare(t)
	read(0) // Queued work and pending consent are not an active task.
	handle := x.attach(t, pending)
	read(1)
	x.tx(t, func(tx *sql.Tx) {
		views, err := x.store.currentTaskViews(x.human, tx, x.key, x.f.a.ChatID)
		if err != nil || len(views) != 1 {
			t.Fatal(views, err)
		}
		v := views[0]
		if v.WorkID != x.workID || v.RunID != x.f.a.RunID || v.Scope != "task" || !v.CanRead || !v.CanWrite || !v.ExpiresAt.After(time.Now()) || v.ExpiresAt.After(time.Now().Add(31*time.Second)) {
			t.Fatalf("wrong task projection: %+v", v)
		}
		encoded, err := json.Marshal(views)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{handle, "original-session", x.f.pages.artifactMasterToken, "credential", "binding", "mandate_ref"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("private authority leaked: %s", secret)
			}
		}
		otherChat, err := x.store.currentTaskViews(x.human, tx, x.key, "other-chat")
		if err != nil || len(otherChat) != 0 {
			t.Fatal("cross-chat task disclosure", otherChat, err)
		}
		other := x.key
		other.ArtifactID = "other-page"
		otherPage, err := x.store.currentTaskViews(x.human, tx, other, x.f.a.ChatID)
		if err != nil || len(otherPage) != 0 {
			t.Fatal("cross-page task disclosure", otherPage, err)
		}
	})
	read(1) // Displaying metadata never consumes or renews the task.
}

func TestCollaborationAgentTaskViewRejectsChangedAuthority(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"agent grant revoked", `DELETE FROM page_grants WHERE page_id='agent-shared-page'`},
		{"source revoked", `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id='original-session'`},
		{"source ABA", `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id='original-session'; UPDATE user_sessions SET revoked_at=NULL WHERE id='original-session'`},
		{"attempt replaced", `UPDATE work_attempts SET runtime_locator='replacement'`},
		{"attempt expired", `UPDATE work_attempts SET lease_expires_at='2020-01-01T00:00:00Z'`},
		{"terminal work", `UPDATE work_items SET state='cancelled'`},
		{"principal removed", `DELETE FROM artifact_run_principals`},
		{"private audience", `UPDATE pages SET private_audience=1 WHERE id='agent-shared-page'`},
		{"policy disabled", `UPDATE workspace_artifact_creation_policy SET enabled=0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCollaborationAgentTestFixture(t)
			x.attach(t, x.prepare(t))
			mustExec(t, x.f.rig.db, tc.mutation)
			x.tx(t, func(tx *sql.Tx) {
				views, err := x.store.currentTaskViews(x.human, tx, x.key, x.f.a.ChatID)
				if err == nil || len(views) != 0 {
					t.Fatal("changed/uncertain authority advertised as active", views, err)
				}
			})
		})
	}
}

func TestCollaborationAgentTaskViewCannotRecoverOrExtendConsent(t *testing.T) {
	for _, name := range []string{"new registry", "key rotation", "boot change", "monotonic expiry", "retired ingress"} {
		t.Run(name, func(t *testing.T) {
			x := newCollaborationAgentTestFixture(t)
			x.attach(t, x.prepare(t))
			switch name {
			case "new registry":
				var err error
				x.store, err = newCollaborationAgentMandateStore(x.f.pages, true, x.fence, x.clock, time.Minute, 4)
				if err != nil {
					t.Fatal(err)
				}
			case "key rotation":
				x.f.pages.artifactMasterToken = "different-host-key"
			case "boot change":
				x.clock.mu.Lock()
				x.clock.boot = "different-boot"
				x.clock.mu.Unlock()
			case "monotonic expiry":
				x.clock.advance(time.Minute)
			case "retired ingress":
				x.fence.SuspendBeforeRestore()
			}
			x.tx(t, func(tx *sql.Tx) {
				views, _ := x.store.currentTaskViews(x.human, tx, x.key, x.f.a.ChatID)
				if len(views) != 0 {
					t.Fatal("old consent recovered", views)
				}
			})
		})
	}
}
