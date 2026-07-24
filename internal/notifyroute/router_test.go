package notifyroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/notify"
)

// recordingWebhookServer captures every POST body it receives, for
// asserting exactly-N-deliveries assertions without a real network hop.
type recordingWebhookServer struct {
	*httptest.Server
	mu    sync.Mutex
	posts []map[string]any
}

func newRecordingWebhookServer(t *testing.T) *recordingWebhookServer {
	t.Helper()
	rs := &recordingWebhookServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		rs.mu.Lock()
		rs.posts = append(rs.posts, body)
		rs.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *recordingWebhookServer) count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.posts)
}

// fakePresence lets a test declare exactly which (channel, user) pairs are
// "watching live," mirroring internal/chatnotify.Hub's IsUserSubscribed.
type fakePresence map[string]bool // key: channel+"\x00"+userID

func (f fakePresence) IsUserSubscribed(channel, userID string) bool {
	return f[channel+"\x00"+userID]
}

// newTestRouter wires a Router against db with a real notify.Dispatcher
// (email disabled, webhook SSRF-safe transport swapped to default by
// TestMain) so table-driven cases exercise the real delivery path end to
// end, not a mock.
func newTestRouter(db *sql.DB, presence PresenceChecker, limiter *RateLimiter) *Router {
	dispatcher := notify.NewDispatcher(notify.NewChannelStore(db), nil, quietLogger(), db)
	return NewRouter(db, dispatcher, presence, limiter, quietLogger())
}

func seedWebhookChannel(t *testing.T, db *sql.DB, url string) notify.Channel {
	t.Helper()
	ch, err := notify.NewChannelStore(db).Create(context.Background(), notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: url,
	})
	if err != nil {
		t.Fatalf("seed webhook channel: %v", err)
	}
	return ch
}

// TestRouter_Route is the table-driven acceptance suite for the routing
// pipeline (issue #1412): audience -> presence -> preference matrix ->
// admin allowlist -> priority floor -> anti-storm -> delivery.
func TestRouter_Route(t *testing.T) {
	type setup struct {
		name       string
		category   string
		item       func(chID string) inbox.Item
		pref       *PrefCell // nil = no pref row set (default off)
		channelCat []string  // admin allowlist; nil = every category
		minPrio    string
		presence   PresenceChecker
		limiter    *RateLimiter
		wantPosts  int
		wantStatus string // expected terminal delivery status, "" = no row expected
	}

	cases := []setup{
		{
			name:     "immediate pref delivers",
			category: notify.CategoryApprovals,
			item: func(chID string) inbox.Item {
				return inbox.Item{WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-1", TargetUserID: "u_member", Title: "Approve"}
			},
			pref:       &PrefCell{Category: notify.CategoryApprovals, State: "immediate"},
			wantPosts:  1,
			wantStatus: StatusSent,
		},
		{
			name:     "default off never delivers",
			category: notify.CategoryApprovals,
			item: func(chID string) inbox.Item {
				return inbox.Item{WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-2", TargetUserID: "u_member", Title: "Approve"}
			},
			pref:      nil,
			wantPosts: 0,
		},
		{
			name:     "admin allowlist excludes category",
			category: notify.CategoryApprovals,
			item: func(chID string) inbox.Item {
				return inbox.Item{WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-3", TargetUserID: "u_member", Title: "Approve"}
			},
			pref:       &PrefCell{Category: notify.CategoryApprovals, State: "immediate"},
			channelCat: []string{notify.CategoryBudget},
			wantPosts:  0,
			wantStatus: StatusDroppedPref,
		},
		{
			name:     "priority below channel floor",
			category: notify.CategorySecurity,
			item: func(chID string) inbox.Item {
				return inbox.Item{WorkspaceID: "ws1", Kind: "escalation", SourceID: "esc-1", TargetUserID: "u_member", Title: "x", Priority: "low"}
			},
			pref:       &PrefCell{Category: notify.CategorySecurity, State: "immediate"},
			minPrio:    "high",
			wantPosts:  0,
			wantStatus: StatusDroppedPref,
		},
		{
			name:     "muted channel drops",
			category: notify.CategoryBudget,
			item: func(chID string) inbox.Item {
				return inbox.Item{WorkspaceID: "ws1", Kind: "failed_run", SourceID: "run-1", TargetUserID: "u_member", Title: "x"}
			},
			pref:       &PrefCell{Category: notify.CategoryBudget, State: "immediate"},
			wantPosts:  0,
			wantStatus: StatusDroppedPref,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newRouteTestDB(t)
			srv := newRecordingWebhookServer(t)
			ch := seedWebhookChannel(t, db, srv.URL)

			if len(tc.channelCat) > 0 {
				enabled := true
				cats := tc.channelCat
				if _, err := notify.NewChannelStore(db).Patch(context.Background(), "ws1", ch.ID, notify.PatchInput{
					Enabled: &enabled, Categories: &cats,
				}); err != nil {
					t.Fatalf("patch categories: %v", err)
				}
			}
			if tc.minPrio != "" {
				mp := tc.minPrio
				if _, err := notify.NewChannelStore(db).Patch(context.Background(), "ws1", ch.ID, notify.PatchInput{MinPriority: &mp}); err != nil {
					t.Fatalf("patch min priority: %v", err)
				}
			}

			prefs := NewPrefStore(db)
			cells := []PrefCell{}
			if tc.pref != nil {
				c := *tc.pref
				c.ChannelID = ch.ID
				cells = append(cells, c)
			}
			if tc.name == "muted channel drops" {
				cells = append(cells, PrefCell{Category: notify.CategoryMuteAll, ChannelID: ch.ID, State: "immediate"})
			}
			if len(cells) > 0 {
				if err := prefs.Set(context.Background(), "ws1", "u_member", cells); err != nil {
					t.Fatalf("set prefs: %v", err)
				}
			}

			r := newTestRouter(db, tc.presence, tc.limiter)
			r.route(context.Background(), tc.category, tc.item(ch.ID))

			if got := srv.count(); got != tc.wantPosts {
				t.Errorf("webhook posts = %d, want %d", got, tc.wantPosts)
			}
			rows, err := NewDeliveryStore(db).List(context.Background(), "ws1", ListFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus == "" {
				if len(rows) != 0 {
					t.Errorf("expected no delivery rows, got %d: %+v", len(rows), rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("expected exactly 1 delivery row, got %d: %+v", len(rows), rows)
			}
			if rows[0].Status != tc.wantStatus {
				t.Errorf("delivery status = %q, want %q", rows[0].Status, tc.wantStatus)
			}
		})
	}
}

func TestRouter_Route_ApprovalsBypassRateGate(t *testing.T) {
	db := newRouteTestDB(t)
	srv := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, srv.URL)
	prefs := NewPrefStore(db)
	if err := prefs.Set(context.Background(), "ws1", "u_member",
		[]PrefCell{{Category: notify.CategoryApprovals, ChannelID: ch.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}

	// Capacity 0: EVERY non-exempt category would be rate-limited instantly.
	limiter := NewRateLimiter(0, 0)
	r := newTestRouter(db, nil, limiter)

	r.route(context.Background(), notify.CategoryApprovals, inbox.Item{
		WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-approve-1", TargetUserID: "u_member", Title: "Approve",
	})

	if got := srv.count(); got != 1 {
		t.Fatalf("approvals must bypass the rate gate; got %d posts, want 1", got)
	}
}

func TestRouter_Route_NonApprovalRespectsRateGate(t *testing.T) {
	db := newRouteTestDB(t)
	srv := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, srv.URL)
	prefs := NewPrefStore(db)
	if err := prefs.Set(context.Background(), "ws1", "u_member",
		[]PrefCell{{Category: notify.CategoryBudget, ChannelID: ch.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}

	limiter := NewRateLimiter(0, 0) // instantly exhausted
	r := newTestRouter(db, nil, limiter)

	r.route(context.Background(), notify.CategoryBudget, inbox.Item{
		WorkspaceID: "ws1", Kind: "failed_run", SourceID: "run-rate-1", TargetUserID: "u_member", Title: "x",
	})

	if got := srv.count(); got != 0 {
		t.Fatalf("budget category should be rate-limited; got %d posts, want 0", got)
	}
	rows, _ := NewDeliveryStore(db).List(context.Background(), "ws1", ListFilter{Status: StatusDroppedRate})
	if len(rows) != 1 {
		t.Fatalf("expected 1 dropped_rate row, got %d", len(rows))
	}
}

func TestRouter_Route_CoalescesSameSourceID(t *testing.T) {
	db := newRouteTestDB(t)
	srv := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, srv.URL)
	prefs := NewPrefStore(db)
	if err := prefs.Set(context.Background(), "ws1", "u_member",
		[]PrefCell{{Category: notify.CategoryChatReplies, ChannelID: ch.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRouter(db, nil, nil)

	item := inbox.Item{WorkspaceID: "ws1", Kind: inbox.KindMessage, SourceID: "chat_reply_c1_u_member", TargetUserID: "u_member", Title: "Agent replied"}
	r.route(context.Background(), notify.CategoryChatReplies, item)
	r.route(context.Background(), notify.CategoryChatReplies, item)

	if got := srv.count(); got != 1 {
		t.Fatalf("re-firing the same source_id should coalesce to exactly 1 delivery, got %d", got)
	}
}

func TestRouter_Route_PresenceGateSkipsWatchingUser(t *testing.T) {
	db := newRouteTestDB(t)
	srv := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, srv.URL)
	prefs := NewPrefStore(db)
	if err := prefs.Set(context.Background(), "ws1", "u_member",
		[]PrefCell{{Category: notify.CategoryChatReplies, ChannelID: ch.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	presence := fakePresence{"session:c1\x00u_member": true}
	r := newTestRouter(db, presence, nil)

	item := inbox.Item{
		WorkspaceID: "ws1", Kind: inbox.KindMessage, SourceID: "chat_reply_c1_u_member", TargetUserID: "u_member",
		Title: "Agent replied", Payload: map[string]interface{}{"chat_id": "c1"},
	}
	// route() alone doesn't run the presence gate (that's in route()'s
	// caller loop over recipients) — call NotifyInboxItem's synchronous
	// counterpart path directly via the audience loop by invoking route()
	// which DOES include the presence check per resolved recipient.
	r.route(context.Background(), notify.CategoryChatReplies, item)

	if got := srv.count(); got != 0 {
		t.Fatalf("a user watching the chat live must not get an external push; got %d posts, want 0", got)
	}
}

func TestRouter_Route_TargetRoleFansOutToEveryMember(t *testing.T) {
	db := newRouteTestDB(t)
	srv := newRecordingWebhookServer(t)
	ch := seedWebhookChannel(t, db, srv.URL)
	prefs := NewPrefStore(db)
	// Only u_manager (role MANAGER) opts in; u_owner (role OWNER) does not
	// share this channel's category preference.
	if err := prefs.Set(context.Background(), "ws1", "u_manager",
		[]PrefCell{{Category: notify.CategoryEscalations, ChannelID: ch.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRouter(db, nil, nil)

	r.route(context.Background(), notify.CategoryEscalations, inbox.Item{
		WorkspaceID: "ws1", Kind: "escalation", SourceID: "esc-role-1", TargetRole: "MANAGER", Title: "Needs review",
	})

	if got := srv.count(); got != 1 {
		t.Fatalf("expected exactly 1 post (only the opted-in MANAGER), got %d", got)
	}
}

func TestRouter_NotifyInboxItem_UnmappedKindIsNoOp(t *testing.T) {
	db := newRouteTestDB(t)
	r := newTestRouter(db, nil, nil)
	// "memory_consolidation" is mapped (to CategoryMemory); an arbitrary
	// unmapped kind should resolve to "" and never even reach route()'s
	// DB queries. NotifyInboxItem is fire-and-forget (spawns a goroutine),
	// so this only asserts it doesn't panic / returns immediately; the
	// category-resolution unit behavior is pinned directly in
	// internal/notify's own category tests.
	r.NotifyInboxItem(context.Background(), inbox.Item{WorkspaceID: "ws1", Kind: "some_future_kind", SourceID: "x"})
}
