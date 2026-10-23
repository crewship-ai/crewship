package ws

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func workbookAuthorizationDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	for _, query := range []string{
		`INSERT INTO users(id) VALUES('work-owner'),('work-editor')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','work-owner','MEMBER'),('ws1','work-editor','MEMBER')`,
		`INSERT INTO pages(id,workspace_id,owner_user_id,private_audience) VALUES('one','ws1','work-owner',0),('two','ws1','work-owner',0)`,
		`INSERT INTO gridcraft_heads(page_id) VALUES('one'),('two')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('one','user','work-editor','write','work-owner'),('two','user','work-editor','write','work-owner')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
func expectWorkbookClosed(t *testing.T, c *Client, page string) {
	t.Helper()
	select {
	case raw := <-c.send:
		var frame struct {
			Type    string            `json:"type"`
			Channel string            `json:"channel"`
			Payload map[string]string `json:"payload"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.Type != "session_closed" || frame.Channel != "" || frame.Payload["artifact_id"] != page {
			t.Fatalf("terminal control: %s", raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workbook not closed within 5s")
	}
}
func TestWorkbookGrantKickLeavesOtherWorkbookAndSocketAlive(t *testing.T) {
	db := workbookAuthorizationDB(t)
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
	hub.TrackWorkbookPage("one")
	hub.TrackWorkbookPage("two")
	client := newClient(t, hub, "work-editor")
	client.subscribe("page:one")
	client.subscribe("page:two")
	if _, err := db.Exec(`DELETE FROM page_grants WHERE page_id='one'`); err != nil {
		t.Fatal(err)
	}
	hub.RecheckWorkbookAccess(t.Context(), "one")
	expectWorkbookClosed(t, client, "one")
	client.mu.Lock()
	one, two := client.channels["page:one"], client.channels["page:two"]
	client.mu.Unlock()
	if one || !two {
		t.Fatalf("wrong subscription isolation: one=%v two=%v", one, two)
	}
	select {
	case <-client.ctx.Done():
		t.Fatal("shared socket was closed")
	default:
	}
	hub.BroadcastChannel("page", "two", "workbook.committed", map[string]int{"rev": 2})
	select {
	case raw := <-client.send:
		if !json.Valid(raw) {
			t.Fatal(string(raw))
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated workbook stopped receiving")
	}
	reconnect := newClient(t, hub, "work-editor")
	reconnect.subscribe("page:one")
	reconnect.mu.Lock()
	subscribed := reconnect.channels["page:one"]
	reconnect.mu.Unlock()
	if subscribed {
		t.Fatal("old authenticated participant rejoined revoked workbook")
	}
}

func TestQueuedWorkbookContentRechecksGrantBeforeWrite(t *testing.T) {
	db := workbookAuthorizationDB(t)
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
	hub.TrackWorkbookPage("one")
	// A nil socket makes any attempted network write fail loudly. A denied
	// queued frame must be dropped, preserving the pump for terminal control.
	client := &Client{hub: hub, userID: "work-editor", ctx: context.Background()}
	queued := []byte(`{"type":"workbook.committed","channel":"page:one","payload":{"content":"private-canary"}}`)
	if _, err := db.Exec(`DELETE FROM page_grants WHERE page_id='one'`); err != nil {
		t.Fatal(err)
	}
	if !client.writeFrame(queued) {
		t.Fatal("protected frame tore down the shared writer instead of being dropped")
	}
}

func TestWorkbookMembershipRevocationFallbackClosesWithinFiveSeconds(t *testing.T) {
	db := workbookAuthorizationDB(t)
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
	hub.TrackWorkbookPage("one")
	client := newClient(t, hub, "work-editor")
	client.subscribe("page:one")
	if _, err := db.Exec(`DELETE FROM workspace_members WHERE user_id='work-editor'`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	expectWorkbookClosed(t, client, "one")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("fallback latency: %v", elapsed)
	}
	t.Logf("membership revocation terminal latency: %v", time.Since(start))
}

type blockingWorkbookAuthorization struct{ block atomic.Bool }

func (a *blockingWorkbookAuthorization) CanSubscribe(ctx context.Context, _, _ string) (bool, error) {
	if !a.block.Load() {
		return true, nil
	}
	<-ctx.Done()
	return false, ctx.Err()
}
func TestWorkbookSweepBudgetDoesNotStarveLaterParticipants(t *testing.T) {
	hub := newRunningHub(t)
	auth := &blockingWorkbookAuthorization{}
	hub.SetChannelAuthorizer(auth)
	hub.TrackWorkbookPage("one")
	hub.TrackWorkbookPage("two")
	first, second := newClient(t, hub, "first"), newClient(t, hub, "second")
	first.subscribe("page:one")
	second.subscribe("page:two")
	auth.block.Store(true)
	start := time.Now()
	hub.maybeStartWorkbookSweep(t.Context())
	expectWorkbookClosed(t, first, "one")
	expectWorkbookClosed(t, second, "two")
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("budget applied per participant: %v", elapsed)
	}
}
