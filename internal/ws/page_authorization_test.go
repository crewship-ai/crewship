package ws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A Page returned to its owner's private audience (PX-11) ends the Page channel
// subscription of every former reader at once, keeps the owner's and the
// reader's other channels, and leaves the shared socket open.
func TestRevokePageSubscribersEndsFormerReadersAtOnce(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	for _, query := range []string{
		`INSERT INTO users(id) VALUES('page-owner'),('page-reader')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','page-owner','MEMBER'),('ws1','page-reader','MEMBER')`,
		`INSERT INTO pages(id,workspace_id,owner_user_id,private_audience) VALUES('returned','ws1','page-owner',0),('other','ws1','page-owner',0)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
	owner := newClient(t, hub, "page-owner")
	owner.subscribe("page:returned")
	reader := newClient(t, hub, "page-reader")
	reader.subscribe("page:returned")
	reader.subscribe("page:other")
	if n := hub.RevokePageSubscribers(t.Context(), "returned"); n != 0 {
		t.Fatalf("shared Page ended %d subscriptions", n)
	}
	if _, err := db.Exec(`UPDATE pages SET private_audience=1 WHERE id='returned'`); err != nil {
		t.Fatal(err)
	}
	if n := hub.RevokePageSubscribers(t.Context(), "returned"); n != 1 {
		t.Fatalf("ended %d subscriptions, want only the reader's", n)
	}
	select {
	case raw := <-reader.send:
		var frame struct {
			Type    string `json:"type"`
			Channel string `json:"channel"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.Type != "error" || frame.Channel != "page:returned" {
			t.Fatalf("revocation frame %s", raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader was not told")
	}
	reader.mu.Lock()
	returned, other := reader.channels["page:returned"], reader.channels["page:other"]
	reader.mu.Unlock()
	owner.mu.Lock()
	ownerKept := owner.channels["page:returned"]
	owner.mu.Unlock()
	if returned || !other || !ownerKept {
		t.Fatalf("returned=%v other=%v owner=%v", returned, other, ownerKept)
	}
	select {
	case <-reader.ctx.Done():
		t.Fatal("a definitive deny closed the shared socket")
	default:
	}
}

type failingPageAuthorization struct{}

func (failingPageAuthorization) CanSubscribe(context.Context, string, string) (bool, error) {
	return false, errors.New("database unavailable")
}

// The deliberate revocation fails closed: an authorizer error disconnects the
// client rather than leaving the subscription for the next sweep.
func TestRevokePageSubscribersFailsClosed(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(failingPageAuthorization{})
	reader := newClient(t, hub, "page-reader")
	reader.mu.Lock()
	reader.channels["page:returned"] = true
	reader.mu.Unlock()
	hub.mu.Lock()
	if hub.channels["page:returned"] == nil {
		hub.channels["page:returned"] = map[*Client]bool{}
	}
	hub.channels["page:returned"][reader] = true
	hub.mu.Unlock()
	if n := hub.RevokePageSubscribers(t.Context(), "returned"); n != 1 {
		t.Fatalf("ended %d, want 1", n)
	}
	select {
	case <-reader.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client kept a subscription whose re-check failed")
	}
}
