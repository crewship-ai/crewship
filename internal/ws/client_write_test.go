package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"golang.org/x/net/websocket"
)

func TestWriteFrameReauthorizesCurrentMembership(t *testing.T) {
	db := seededDB(t)
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
	client := &Client{hub: hub, conn: dialClientConn(t), userID: "u-good", ctx: context.Background()}
	frame := []byte(`{"type":"chat_event","channel":"session:chat-1","payload":{"content":"canary"}}`)
	if !client.writeFrame(frame) {
		t.Fatal("authorized frame was not delivered")
	}
	if _, err := db.Exec(`DELETE FROM workspace_members WHERE user_id='u-good'`); err != nil {
		t.Fatal(err)
	}
	if client.writeFrame(frame) {
		t.Fatal("queued frame delivered after membership revocation")
	}
}

func TestBroadcastBindsDeliveryToRoutingChannel(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	client := newClient(t, hub, "u1")
	client.subscribe("session:private")
	for _, declared := range []string{"", "providers:global"} {
		for _, except := range []bool{false, true} {
			message := ServerMessage{Type: "chat_event", Channel: declared, Payload: "private-canary"}
			if except {
				hub.BroadcastExcept("session:private", nil, message)
			} else {
				hub.Broadcast("session:private", message)
			}
			var received ServerMessage
			if err := json.Unmarshal(recvOrTimeout(t, client.send), &received); err != nil {
				t.Fatal(err)
			}
			if received.Channel != "session:private" {
				t.Fatal("delivery authorized against an unrelated channel")
			}
		}
	}
}

// dialClientConn spins up a real hub upgrade endpoint and returns a
// dialed *websocket.Conn so writeFrame can be exercised against an
// actual network connection (not a mock). It never sends the post-upgrade
// auth message — the tests here exercise writeFrame directly against a raw
// conn wrapped in a standalone *Client, bypassing hub registration
// entirely, so server-side auth completing is irrelevant.
func dialClientConn(t *testing.T) *websocket.Conn {
	t.Helper()
	v, err := auth.NewJWTValidator("test-secret-of-sufficient-length")
	if err != nil {
		t.Fatal(err)
	}
	hub := newRunningHub(t, withValidator(v))
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	u, _ := url.Parse(srv.URL)
	conn, err := websocket.Dial(fmt.Sprintf("ws://%s/", u.Host), "", srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// A write deadline already in the past must make writeFrame fail
// immediately rather than block — this is the core of the slow-consumer
// protection. Without SetWriteDeadline the write would proceed (or hang),
// so this test fails on the pre-fix code path.
func TestWriteFrameHonoursPastDeadline(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	// 1ns deadline is already in the past by the time the write syscall
	// runs, so the socket write must time out. On the pre-fix code path
	// (no SetWriteDeadline) the write would proceed and this fails.
	c := &Client{conn: conn, writeWait: time.Nanosecond}

	if c.writeFrame([]byte(`{"type":"ev"}`)) {
		t.Fatal("writeFrame returned true with an expired write deadline; expected timeout/false")
	}
}

// With a sane positive deadline a normal frame is delivered and writeFrame
// reports success — guards against a deadline so short it breaks delivery.
func TestWriteFrameDeliversWithinDeadline(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	c := &Client{conn: conn, writeWait: 5 * time.Second}

	if !c.writeFrame([]byte(`{"type":"ev"}`)) {
		t.Fatal("writeFrame returned false for a healthy write within the deadline")
	}
}

// Zero writeWait falls back to defaultWriteWait (not an instant-expiry
// zero deadline), so production clients constructed without the field set
// still get the full grace period.
func TestWriteFrameZeroUsesDefault(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	c := &Client{conn: conn, writeWait: 0}

	if !c.writeFrame([]byte(`{"type":"ev"}`)) {
		t.Fatal("writeFrame with zero writeWait should use defaultWriteWait and succeed")
	}
}

func TestEntityInvalidationDeliveryRechecksTrustedAudience(t *testing.T) {
	for _, tc := range []struct{ name, revoke string }{
		{"suspended", `UPDATE users SET suspended_at='now' WHERE id='u-good'`},
		{"workspace deleted", `UPDATE workspaces SET deleted_at='now' WHERE id='ws-1'`},
		{"agent deleted", `UPDATE agents SET deleted_at='now' WHERE id='agent-live'`},
		{"membership removed", `DELETE FROM workspace_members WHERE user_id='u-good'`},
		{"restricted elsewhere", `INSERT INTO workspaces(id,name,slug) VALUES('ws-2','Other','other'); INSERT INTO workspace_members(id,user_id,workspace_id,access_mode) VALUES('restricted','u-good','ws-2','restricted')`},
		{"participant removed", `UPDATE chats SET created_by='other-user',visibility='group' WHERE id='chat-1'; DELETE FROM chat_participants WHERE user_id='u-good'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := seededDB(t)
			if _, err := db.Exec(`INSERT INTO users(id,email) VALUES('other-user','other@invalid.test')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO chat_participants(chat_id,user_id) VALUES('chat-1','u-good')`); err != nil {
				t.Fatal(err)
			}
			auth := NewDBChannelAuthorizer(db)
			for _, channel := range []string{"session:chat-1", "chat-hints:chat-1"} {
				if allowed, err := auth.CanSubscribe(t.Context(), "u-good", channel); err != nil || !allowed {
					t.Fatalf("authorized subscription %s: %v %v", channel, allowed, err)
				}
			}
			hub := newRunningHub(t)
			hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
			client := &Client{hub: hub, conn: dialClientConn(t), userID: "u-good", ctx: context.Background()}
			for _, event := range []string{"chat.entity_refs_changed", "issue.revision_changed"} {
				frame := []byte(fmt.Sprintf(`{"type":%q,"channel":"session:chat-1","payload":{"contract_version":1}}`, event))
				if !client.writeFrame(frame) {
					t.Fatal("legitimate trusted frame denied")
				}
			}
			if _, err := db.Exec(tc.revoke); err != nil {
				t.Fatal(err)
			}
			for _, channel := range []string{"session:chat-1", "chat-hints:chat-1"} {
				subscribe, e1 := auth.CanSubscribe(t.Context(), "u-good", channel)
				deliver, e2 := auth.CanDeliver(t.Context(), "u-good", channel)
				if e1 != nil || e2 != nil || subscribe || deliver {
					t.Fatalf("revoked %s: subscribe=%v deliver=%v errors=%v/%v", channel, subscribe, deliver, e1, e2)
				}
			}
			for _, event := range []string{"chat.entity_refs_changed", "issue.revision_changed"} {
				frame := []byte(fmt.Sprintf(`{"type":%q,"channel":"session:chat-1","payload":{"contract_version":1}}`, event))
				if client.writeFrame(frame) {
					t.Fatal("queued hint delivered after authority revoked")
				}
			}
		})
	}
}

func TestWriteFrameRechecksSuspendedAccountOnBroadChannels(t *testing.T) {
	db := seededDB(t)
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
	client := &Client{hub: hub, conn: dialDeliverySink(t), userID: "u-good", ctx: context.Background()}
	frames := [][]byte{
		[]byte(`{"type":"update","channel":"workspace:ws-1","payload":{}}`),
		[]byte(`{"type":"update","channel":"providers:global","payload":{}}`),
		[]byte(`{"type":"pong"}`),
	}
	for _, frame := range frames {
		if !client.writeFrame(frame) {
			t.Fatal("active account denied")
		}
	}
	if _, err := db.Exec(`UPDATE users SET suspended_at='' WHERE id='u-good'`); err != nil {
		t.Fatal(err)
	}
	for _, frame := range frames {
		if client.writeFrame(frame) {
			t.Fatal("queued update delivered after account suspension")
		}
	}
}

// Multiple valid frames need a peer that keeps reading instead of treating the
// first frame as an upgrade-authentication message.
func dialDeliverySink(t *testing.T) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.Dial("ws://"+endpoint.Host+"/", "", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}
