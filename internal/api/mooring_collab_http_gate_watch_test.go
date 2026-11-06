package api

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	_ "modernc.org/sqlite"
)

type gateWatchClock struct {
	boot string
	now  time.Duration
	err  error
}

func (c *gateWatchClock) Now() (string, time.Duration, error) { return c.boot, c.now, c.err }

func gateWatchFixture(t *testing.T, expires int64, closed bool) (*collaborationHTTPConnection, *gateWatchClock, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE mooring_delivery_gates(id TEXT,page_id TEXT,workspace_id TEXT,application_id TEXT,boot_id TEXT,expires_ns INTEGER,closed INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO mooring_delivery_gates VALUES('gate','page','ws','app','boot',?,?)`, expires, closed); err != nil {
		t.Fatal(err)
	}
	clock := &gateWatchClock{boot: "boot", now: 100}
	d := &collaborationHTTPDocument{key: mooring.SessionKey{WorkspaceID: "ws", ArtifactID: "page", ApplicationID: "app"}, manager: &collaborationHTTPManager{pages: &PageHandler{db: db}, clock: clock}}
	c := &collaborationHTTPConnection{document: d, joinedDelivered: true, participantBinding: &mooring.SessionParticipantBinding{}, joined: collaborationHumanJoined{delivery: &mooring.DeliveryStore{}, gate: mooring.DeliveryGrant{GateID: "gate"}}}
	return c, clock, db
}

// A false positive costs a writer transaction; a false negative is the stall.
// Everything but plain expiry stays with its existing path.
func TestCollaborationGateLapsedOnlyOnPlainExpiry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expires int64
		closed  bool
		mutate  func(*collaborationHTTPConnection, *gateWatchClock)
		want    bool
	}{
		{name: "before the horizon", expires: 101},
		{name: "exactly at the horizon", expires: 100, want: true},
		{name: "past the horizon", expires: 1, want: true},
		{name: "closed row belongs to revocation", expires: 1, closed: true},
		{name: "another boot is not comparable", expires: 1, mutate: func(_ *collaborationHTTPConnection, k *gateWatchClock) { k.boot = "later" }},
		{name: "rotation already running", expires: 1, mutate: func(c *collaborationHTTPConnection, _ *gateWatchClock) {
			c.transition = &collaborationGateTransition{ready: make(chan struct{})}
		}},
		{name: "socket closed", expires: 1, mutate: func(c *collaborationHTTPConnection, _ *gateWatchClock) { c.closed = true }},
		{name: "joined not delivered yet", expires: 1, mutate: func(c *collaborationHTTPConnection, _ *gateWatchClock) { c.joinedDelivered = false }},
		{name: "no native binding", expires: 1, mutate: func(c *collaborationHTTPConnection, _ *gateWatchClock) { c.participantBinding = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, clock, _ := gateWatchFixture(t, tc.expires, tc.closed)
			if tc.mutate != nil {
				tc.mutate(c, clock)
			}
			got, err := c.gateLapsed(t.Context())
			if err != nil || got != tc.want {
				t.Fatalf("gateLapsed=%v err=%v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestCollaborationGateLapsedReportsUnreadableState(t *testing.T) {
	c, clock, db := gateWatchFixture(t, 1, false)
	clock.err = errors.New("clock unavailable")
	if lapsed, err := c.gateLapsed(t.Context()); err == nil || lapsed {
		t.Fatal(lapsed, err)
	}
	clock.err = nil
	if _, err := db.Exec(`DELETE FROM mooring_delivery_gates`); err != nil {
		t.Fatal(err)
	}
	if lapsed, err := c.gateLapsed(t.Context()); !errors.Is(err, sql.ErrNoRows) || lapsed {
		t.Fatal(lapsed, err)
	}
}

// The socket going away is not a rotation failure: the other recipients stay.
func TestCollaborationGateReopenFailureEndsDocumentOnlyWhileConnected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		closed bool
		want   int32
	}{{"open socket", false, 1}, {"closed socket", true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := gateWatchFixture(t, 1, false)
			var cancellations atomic.Int32
			c.document.cancel = func() { cancellations.Add(1) }
			c.closed = tc.closed
			c.participantBinding = nil // startGateTransition refuses without a binding.
			c.reopenLapsedGate(t.Context())
			if cancellations.Load() != tc.want {
				t.Fatalf("cancellations=%d, want %d", cancellations.Load(), tc.want)
			}
		})
	}
}

func TestCollaborationGateWatchStopsWithoutAnotherTick(t *testing.T) {
	c, _, _ := gateWatchFixture(t, 101, false)
	stop := c.startGateWatch(t.Context(), time.Hour)
	finished := make(chan struct{})
	go func() { stop(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop")
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopped := c.startGateWatch(ctx, time.Hour)
	cancel()
	stopped()
}
