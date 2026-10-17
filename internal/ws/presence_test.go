package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPresenceValidationAndIdentityCannotBeSpoofed(t *testing.T) {
	valid := `{"schema_version":1,"revision":2,"cursor":{"x":10,"y":20,"coordinate_space":"viewport","viewport_width":300,"viewport_height":200}}`
	if _, err := decodePresence([]byte(valid), "artifact:page"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(valid, `"revision":2`, `"revision":null`, 1), strings.Replace(valid, `"revision":2`, `"Revision":2`, 1), strings.Replace(valid, `"revision":2`, `"revision":2,"revision":3`, 1), strings.Replace(valid, `"revision":2`, `"revision":2,"user_id":"victim"`, 1), strings.Replace(valid, `"revision":2`, `"revision":2,"display_name":"Administrator"`, 1), strings.Replace(valid, `"x":10`, `"x":null`, 1), strings.Replace(valid, `"x":10`, `"x":301`, 1), strings.Repeat("x", 2049)} {
		if _, err := decodePresence([]byte(raw), "artifact:page"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := decodePresence([]byte(valid), "page:page"); err == nil {
		t.Fatal("workspace invalidation room accepted presence")
	}
	if _, err := decodePresence([]byte(valid), "board:page"); err == nil {
		t.Fatal("artifact coordinates accepted for board")
	}
}
func TestPresenceContentAuthorizationAndRevocation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users(id) VALUES ('reader')`); err != nil {
		t.Fatal(err)
	}
	auth := NewDBChannelAuthorizer(db)
	if ok, _ := auth.CanSubscribe(t.Context(), "reader", "artifact:allowed"); ok {
		t.Fatal("missing content resolver allowed room")
	}
	var revoked atomic.Bool
	auth.SetPageContentAuthorizer(func(_ context.Context, user, kind, id string) (bool, error) {
		return user == "reader" && kind == "artifact" && id == "allowed" && !revoked.Load(), nil
	})
	for _, channel := range []string{"artifact:foreign", "board:allowed"} {
		if ok, _ := auth.CanSubscribe(t.Context(), "reader", channel); ok {
			t.Fatal("cross-page/type allowed")
		}
	}
	if ok, err := auth.CanDeliver(t.Context(), "reader", "artifact:allowed"); err != nil || !ok {
		t.Fatalf("authorized: %v %v", ok, err)
	}
	revoked.Store(true)
	if ok, _ := auth.CanDeliver(t.Context(), "reader", "artifact:allowed"); ok {
		t.Fatal("queued delivery survived revoke")
	}
}
func TestPresenceBoundsTTLAndSlowConsumer(t *testing.T) {
	h := NewHub(testLogger(), nil, NopValidatorForTests, NopSessionsForTests)
	now := time.Now()
	for i := 0; i < 64; i++ {
		c := &Client{userID: fmt.Sprint(i)}
		if !h.joinPresence(c, "board:one", now) {
			t.Fatal("valid room capacity denied")
		}
	}
	if h.joinPresence(&Client{userID: "overflow"}, "board:one", now) {
		t.Fatal("room cap bypass")
	}
	same := &Client{userID: "tabs"}
	for i := 0; i < 4; i++ {
		if !h.joinPresence(&Client{userID: "tabs"}, "artifact:tabs", now) {
			t.Fatal("tabs denied")
		}
	}
	if h.joinPresence(same, "artifact:tabs", now) {
		t.Fatal("identity tab cap bypass")
	}
	for i := 0; i < 8; i++ {
		if !h.joinPresence(same, fmt.Sprint("board:", i), now) {
			t.Fatal("room denied")
		}
	}
	if h.joinPresence(same, "board:overflow", now) {
		t.Fatal("connection room cap bypass")
	}
	for len(h.broadcast) < cap(h.broadcast) {
		h.broadcast <- ChannelMessage{}
	}
	h.emitPresence("board:one") // Full broadcast queue must never block ephemeral traffic.
	h.expirePresence(now.Add(presenceTTL))
	if len(h.presenceRooms) != 0 {
		t.Fatal("expired room identities retained")
	}
}
func TestPresenceRateSharedAcrossTabsAndRevokeRemovesMember(t *testing.T) {
	h := NewHub(testLogger(), nil, NopValidatorForTests, NopSessionsForTests)
	var revoked atomic.Bool
	h.SetChannelAuthorizer(presenceTestAuthorizer{revoked: &revoked})
	clients := []*Client{}
	for i := 0; i < 2; i++ {
		c := &Client{hub: h, userID: "human", ctx: t.Context(), channels: map[string]bool{"board:page": true}, send: make(chan []byte, 4)}
		clients = append(clients, c)
		h.joinPresence(c, "board:page", time.Now())
	}
	msg := ClientMessage{Channel: "board:page", Payload: json.RawMessage(`{"schema_version":1,"board_seq":1}`)}
	for i := 0; i < 20; i++ {
		clients[i%2].handlePresence(msg)
	}
	if h.presenceRooms["board:page"].Rates["human"].Count != 10 {
		t.Fatal("tabs bypassed shared rate")
	}
	revoked.Store(true)
	clients[0].presenceWindow = time.Time{}
	clients[0].handlePresence(msg)
	if h.presenceRooms["board:page"].Members[clients[0]] != nil {
		t.Fatal("revoked publisher retained presence")
	}
}

type presenceTestAuthorizer struct{ revoked *atomic.Bool }

func (a presenceTestAuthorizer) CanSubscribe(context.Context, string, string) (bool, error) {
	return !a.revoked.Load(), nil
}

func TestPresenceAuthorizationSweepRemovesIdleRevokedMember(t *testing.T) {
	h := newRunningHub(t)
	var revoked atomic.Bool
	h.SetChannelAuthorizer(presenceTestAuthorizer{revoked: &revoked})
	c := newClient(t, h, "human")
	c.subscribe("board:page")
	h.presenceMu.Lock()
	joined := h.presenceRooms["board:page"] != nil
	h.presenceMu.Unlock()
	if !joined {
		t.Fatal("authorized subscription did not join presence")
	}
	revoked.Store(true)
	h.sweepChannelAuthorization(t.Context())
	c.mu.Lock()
	subscribed := c.channels["board:page"]
	c.mu.Unlock()
	h.presenceMu.Lock()
	retained := h.presenceRooms["board:page"] != nil
	h.presenceMu.Unlock()
	if subscribed || retained {
		t.Fatal("idle revoked member survived reauthorization sweep")
	}
}

func TestPresenceResolverSurvivesBothStartupConstructionOrders(t *testing.T) {
	for _, routerFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(routerFirst), func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			if _, err := db.Exec(`INSERT INTO users(id) VALUES ('human')`); err != nil {
				t.Fatal(err)
			}
			h := NewHub(testLogger(), nil, NopValidatorForTests, NopSessionsForTests)
			a := NewDBChannelAuthorizer(db)
			resolver := func(_ context.Context, user, kind, id string) (bool, error) {
				return user == "human" && kind == "board" && id == "page", nil
			}
			if routerFirst {
				h.SetPageContentAuthorizer(resolver)
				h.SetChannelAuthorizer(a)
			} else {
				h.SetChannelAuthorizer(a)
				h.SetPageContentAuthorizer(resolver)
			}
			if ok, err := a.CanSubscribe(t.Context(), "human", "board:page"); err != nil || !ok {
				t.Fatalf("startup lost ACL: %v %v", ok, err)
			}
			if ok, _ := a.CanSubscribe(t.Context(), "human", "board:other"); ok {
				t.Fatal("callback widened page access")
			}
		})
	}
}

func TestPresenceExpiredMembershipDoesNotBypassSubscriptionCap(t *testing.T) {
	h := NewHub(testLogger(), nil, NopValidatorForTests, NopSessionsForTests)
	var revoked atomic.Bool
	h.SetChannelAuthorizer(presenceTestAuthorizer{revoked: &revoked})
	c := &Client{hub: h, userID: "human", ctx: t.Context(), channels: map[string]bool{}, send: make(chan []byte, 64)}
	for i := 0; i < 8; i++ {
		c.subscribe(fmt.Sprint("board:", i))
	}
	h.expirePresence(time.Now().Add(presenceTTL))
	c.subscribe("board:overflow")
	if c.channels["board:overflow"] {
		t.Fatal("TTL expiry bypassed durable subscription cap")
	}
}

func TestPresenceSnapshotFanoutHasRoomAndGlobalCeilings(t *testing.T) {
	h := NewHub(testLogger(), nil, NopValidatorForTests, NopSessionsForTests)
	h.joinPresence(&Client{userID: "human"}, "board:page", time.Now())
	for i := 0; i < 100; i++ {
		h.emitPresence("board:page")
	}
	if got := len(h.broadcast); got != 20 {
		t.Fatalf("room fanout ceiling %d", got)
	}
	h.presenceEmitRate = presenceRate{At: time.Now(), Count: 200}
	h.emitPresence("board:other")
	if got := len(h.broadcast); got != 20 {
		t.Fatalf("global fanout ceiling %d", got)
	}
}

func TestPresenceDisplayNamesUseVerifiedIdentityOnceOutsideRoomLock(t *testing.T) {
	h := &Hub{broadcast: make(chan ChannelMessage, 8)}
	var calls int
	h.SetPresenceDisplayNameResolver(func(ctx context.Context, user string) (string, error) {
		calls++
		if user != "actual-user" {
			t.Errorf("unverified identity %q", user)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("resolver has no deadline")
		}
		if !h.presenceMu.TryLock() {
			t.Error("identity lookup holds room lock")
		} else {
			h.presenceMu.Unlock()
		}
		return "\x00Alice\u202e", nil
	})
	client := &Client{userID: "actual-user", ctx: context.Background()}
	for range 10 {
		if !h.joinPresence(client, "artifact:page", time.Now()) {
			t.Fatal("join denied")
		}
	}
	if calls != 1 {
		t.Fatalf("cursor/heartbeat repeated identity lookup %d", calls)
	}
	h.emitPresence("artifact:page")
	var event struct {
		Payload struct {
			Members []struct {
				User string `json:"user_id"`
				Name string `json:"display_name"`
			} `json:"members"`
		} `json:"payload"`
	}
	if err := json.Unmarshal((<-h.broadcast).Data, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Payload.Members) != 1 || event.Payload.Members[0].User != "actual-user" || event.Payload.Members[0].Name != "Alice" {
		t.Fatalf("wrong identity %+v", event)
	}
	if len([]rune(boundedPresenceName(strings.Repeat("ž", 200)))) != 128 {
		t.Fatal("display name exceeds bound")
	}
	h.SetPresenceDisplayNameResolver(func(context.Context, string) (string, error) { return "forged", fmt.Errorf("unavailable") })
	other := &Client{userID: "other", ctx: context.Background()}
	if !h.joinPresence(other, "artifact:page", time.Now()) {
		t.Fatal("lookup failure should not deny presence")
	}
	if h.presenceRooms["artifact:page"].Members[other].Name != "" {
		t.Fatal("failed lookup retained a name")
	}
}
