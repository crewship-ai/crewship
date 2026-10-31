package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/design"
	"github.com/gorilla/websocket"
)

func TestDesignSharedOnlyReaders(t *testing.T) {
	for _, level := range []string{"read", "comment"} {
		t.Run(level, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			user := "shared-only-reader"
			f.addUser(t, user)
			if w := f.grant(t, user, level); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			if w := f.call(t, user, "get", ""); w.Code != 404 {
				t.Errorf("unshared content exposed: %d %s", w.Code, w.Body)
			}
			for _, step := range []struct{ op, body string }{
				{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"SHARED"}}]}`},
				{"share", `{"version":2}`},
				{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"PRIVATE_SAVED"}}]}`},
			} {
				if w := f.call(t, f.owner, step.op, step.body); w.Code != 200 {
					t.Fatal(step.op, w.Code, w.Body)
				}
			}
			f.liveOnly(t, `[{"op":"upsert","node":{"id":"private-live","type":"text","x":0,"y":40,"width":200,"height":30,"text":"PRIVATE_LIVE"}}]`)
			w := f.call(t, user, "get", "")
			var view struct {
				Version       int64             `json:"version"`
				LatestVersion int64             `json:"latest_version"`
				SharedVersion int64             `json:"shared_version"`
				Nodes         []json.RawMessage `json:"nodes"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || view.Version != 2 || view.LatestVersion != 2 || view.SharedVersion != 2 || len(view.Nodes) != 1 || !bytes.Contains(view.Nodes[0], []byte("SHARED")) || bytes.Contains(w.Body.Bytes(), []byte("PRIVATE_")) {
				t.Errorf("Read/Comment escaped the shared snapshot: %d %s", w.Code, w.Body)
			}
			if level == "comment" {
				w := f.call(t, user, "comment", `{"version":2,"path":"design.json","selector":"title","quote":"SHARED","text":"Shared feedback"}`)
				if w.Code != 201 {
					t.Errorf("cannot comment on displayed shared version: %d %s", w.Code, w.Body)
				}
				w = f.call(t, user, "comment", `{"version":3,"path":"design.json","selector":"title","quote":"PRIVATE_SAVED","text":"Private feedback"}`)
				if w.Code != 409 && w.Code != 404 {
					t.Errorf("comment accepted on unshared content: %d %s", w.Code, w.Body)
				}
			}
			socket := f.dial(t, user)
			empty, err := design.Run(t.Context(), design.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if err = socket.WriteMessage(websocket.BinaryMessage, empty.Hello); err != nil {
				t.Fatal(err)
			}
			_ = socket.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, frame, err := socket.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			initial, err := design.Run(t.Context(), design.Request{Frame: frame})
			if err != nil || len(initial.Nodes) != 1 || !bytes.Contains(initial.Nodes[0], []byte("SHARED")) {
				t.Fatalf("readonly sync exposed live state: %s %v", initial.Nodes, err)
			}
			if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"PRIVATE_BROADCAST"}}]}`); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			// A queued presence marker follows all updates from the completed
			// mutation. This checks delivery ordering without a sleep-based test.
			marker := []byte{1, 0}
			room := f.h.designRoom(artifacts.VersionKey{WorkspaceID: f.workspace, PageID: f.page})
			room.mu.Lock()
			for client := range room.clients {
				if client.actor == user {
					_ = client.enqueue(marker)
				}
			}
			room.mu.Unlock()
			_ = socket.SetReadDeadline(time.Now().Add(5 * time.Second))
			for {
				_, frame, err = socket.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(frame, marker) {
					break
				}
				kind, _, err := designSyncKind(frame)
				if err != nil || kind == 0 {
					t.Fatalf("live document update reached shared-only reader: %x %v", frame, err)
				}
			}
		})
	}
}

func TestDesignWorkspaceAdminReadsAndEditsForeignDesignWithAudit(t *testing.T) {
	for _, role := range []string{"ADMIN", "OWNER"} {
		for _, private := range []bool{false, true} {
			t.Run(role+fmt.Sprint(private), func(t *testing.T) {
				f := newDesignAuthorityFixture(t)
				user := "design-workspace-admin"
				f.addUser(t, user)
				mustExec(t, f.h.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, role, f.workspace, user)
				if private {
					mustExec(t, f.h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, f.page)
				}
				w := f.call(t, user, "get", "")
				if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"can_edit":true`)) {
					t.Fatalf("admin could not open an unshared foreign Design: %d %s", w.Code, w.Body)
				}
				var reads, writes int
				mustScan := func(op string, out *int) {
					t.Helper()
					if err := f.h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='design.workspace_admin_accessed' AND workspace_id=? AND user_id=? AND entity_id=? AND json_extract(metadata,'$.operation')=? AND json_extract(metadata,'$.owner_user_id')=?`, f.workspace, user, f.page, op, f.owner).Scan(out); err != nil {
						t.Fatal(err)
					}
				}
				mustScan("read", &reads)
				mustScan("write", &writes)
				if reads != 1 || writes != 0 {
					t.Fatalf("read probes falsified or duplicated the audit: read=%d write=%d", reads, writes)
				}
				w = f.call(t, user, "apply", `{"operations":[{"op":"upsert","node":{"id":"admin-node","type":"text","name":"Admin change","x":0,"y":0,"width":100,"height":30,"text":"ADMIN_EDIT"}}]}`)
				if w.Code != 200 {
					t.Fatalf("admin edit: %d %s", w.Code, w.Body)
				}
				mustScan("write", &writes)
				if writes < 1 {
					t.Fatal("admin write not audited")
				}
				w = f.call(t, user, "get", "")
				if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("ADMIN_EDIT")) || !bytes.Contains(w.Body.Bytes(), []byte(`"latest_version":2`)) {
					t.Fatalf("admin did not use normal versioned mutation: %d %s", w.Code, w.Body)
				}
			})
		}
	}
}

func TestDesignWorkspaceAdminAuditFailurePreservesData(t *testing.T) {
	for _, failure := range []string{"abort", "ignore", "erase", "rewrite"} {
		t.Run(failure, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			user := "design-workspace-admin"
			f.addUser(t, user)
			mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
			var before []byte
			if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&before); err != nil {
				t.Fatal(err)
			}
			trigger := map[string]string{
				"abort":   `CREATE TRIGGER fail_design_audit BEFORE INSERT ON audit_logs WHEN NEW.action='design.workspace_admin_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
				"ignore":  `CREATE TRIGGER fail_design_audit BEFORE INSERT ON audit_logs WHEN NEW.action='design.workspace_admin_accessed' BEGIN SELECT RAISE(IGNORE); END`,
				"erase":   `CREATE TRIGGER fail_design_audit AFTER INSERT ON audit_logs WHEN NEW.action='design.workspace_admin_accessed' BEGIN DELETE FROM audit_logs WHERE id=NEW.id; END`,
				"rewrite": `CREATE TRIGGER fail_design_audit AFTER INSERT ON audit_logs WHEN NEW.action='design.workspace_admin_accessed' BEGIN UPDATE audit_logs SET metadata='{}' WHERE id=NEW.id; END`,
			}[failure]
			mustExec(t, f.h.db, trigger)
			if w := f.call(t, user, "get", ""); w.Code != 500 {
				t.Fatal("admin read escaped without a durable audit", w.Code, w.Body)
			}
			if w := f.call(t, user, "apply", `{"operations":[{"op":"upsert","node":{"id":"unsafe","type":"frame","name":"Unacknowledged","x":0,"y":0,"width":100,"height":100}}]}`); w.Code != 500 {
				t.Fatal("admin mutation escaped failed audit", w.Code, w.Body)
			}
			var after []byte
			if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || f.versions(t) != 1 {
				t.Fatal("audit refusal changed confirmed Design")
			}
		})
	}
}

func TestDesignWorkspaceAdminReadScopeStillSeesUnsharedDesign(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	user := "design-read-admin"
	f.addUser(t, user)
	mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
	mustExec(t, f.h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, f.page)
	f.token(t, user)
	mustExec(t, f.h.db, `UPDATE cli_tokens SET scopes='["pages:read"]' WHERE user_id=?`, user)
	w := f.call(t, user, "get", "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"can_edit":false`)) {
		t.Fatalf("read-scoped admin lost unshared Design read: %d %s", w.Code, w.Body)
	}
	if w := f.call(t, user, "versions", ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"latest_version":1`)) {
		t.Fatal("read-scoped admin lost private history", w.Code, w.Body)
	}
	if w := f.call(t, user, "apply", `{"operations":[]}`); w.Code != 403 {
		t.Fatal("read token became write authority", w.Code, w.Body)
	}
	var before []byte
	if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&before); err != nil {
		t.Fatal(err)
	}
	candidate, err := design.Run(t.Context(), design.Request{State: before, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"forbidden","type":"text","x":0,"y":0,"width":100,"height":30,"text":"FORBIDDEN_ADMIN_TOKEN_EDIT"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	conn := f.dial(t, user)
	if err := conn.WriteMessage(websocket.BinaryMessage, designUpdateFrame(candidate.Update)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			break
		}
	}
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatal("read-scoped admin write was not policy-rejected", err)
	}
	var after []byte
	if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-scoped websocket changed live Design")
	}
	if f.versions(t) != 1 {
		t.Fatal("read-only token changed history")
	}
}

func TestDesignWorkspaceAdminAuditRechecksCredentialBeforeContent(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	user := "design-audit-revoked"
	f.addUser(t, user)
	mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
	f.token(t, user)
	mustExec(t, f.h.db, `CREATE TRIGGER revoke_during_design_audit AFTER INSERT ON audit_logs WHEN NEW.action='design.workspace_admin_accessed' BEGIN UPDATE cli_tokens SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE user_id=NEW.user_id; END`)
	// This public HTTP boundary is post-commit, so the ordinary second proof
	// already prevents a response. The direct delivery seam must be safe too.
	var released bool
	r := f.request(t, user, "GET", "", "")
	k := artifacts.VersionKey{WorkspaceID: f.workspace, PageID: f.page}
	err := f.h.designTx(r, k, false, func(_ context.Context, _ *sql.Tx, _ string) error { released = true; return nil })
	if err == nil || released {
		t.Fatal("an audit-side revocation passed initial admission before delivery", err, released)
	}
}

func TestDesignWorkspaceAdminWebsocketAuditTracksAdmissionAndEdit(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	user := "design-ws-admin"
	f.addUser(t, user)
	mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
	mustExec(t, f.h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, f.page)
	conn := f.dial(t, user)
	count := func(operation string) int {
		t.Helper()
		var n int
		if err := f.h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='design.workspace_admin_accessed' AND user_id=? AND entity_id=? AND json_extract(metadata,'$.operation')=?`, user, f.page, operation).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for range 2 {
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err != nil {
			t.Fatal(err)
		}
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			kind, sub, err := designSyncKind(frame)
			if err != nil {
				t.Fatal(err)
			}
			if kind == 0 && sub == 1 {
				break
			}
		}
	}
	if n := count("read"); n != 1 {
		t.Fatal("websocket probes duplicated admission audit", n)
	}
	var state []byte
	if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&state); err != nil {
		t.Fatal(err)
	}
	candidate, err := design.Run(t.Context(), design.Request{State: state, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"admin-ws","type":"text","x":0,"y":0,"width":100,"height":30,"text":"ADMIN_WS"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, designUpdateFrame(candidate.Update)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for count("write") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := count("write"); n != 1 {
		t.Fatal("websocket edit audit missing or duplicated", n)
	}
	if w := f.call(t, f.owner, "get", ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("ADMIN_WS")) {
		t.Fatal("audited websocket edit was not committed", w.Code, w.Body)
	}
}
