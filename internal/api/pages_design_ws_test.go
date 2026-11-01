package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/design"
	"github.com/gorilla/websocket"
)

func designTestAwarenessFrame(id, clock uint64, state string) []byte {
	payload := designAppendUint(nil, 1)
	payload = designAppendUint(payload, id)
	payload = designAppendUint(payload, clock)
	payload = designAppendUint(payload, uint64(len(state)))
	payload = append(payload, state...)
	frame := designAppendUint(nil, 1)
	frame = designAppendUint(frame, uint64(len(payload)))
	return append(frame, payload...)
}

func (f *designAuthorityFixture) reader(t *testing.T, user string) {
	t.Helper()
	f.addUser(t, user)
	if w := f.grant(t, user, "read"); w.Code != 200 {
		t.Fatalf("read grant %d %s", w.Code, w.Body)
	}
	// Readers synchronize only the shared pinned version (see
	// designSharedState); without one their sync request is refused.
	if w := f.call(t, f.owner, "share", `{"version":1}`); w.Code != 200 {
		t.Fatalf("share %d %s", w.Code, w.Body)
	}
}

// awarenessHolder reports the actor whose live connection currently holds the
// awareness client ID in this Design room, or "" when none does.
func (f *designAuthorityFixture) awarenessHolder(id uint64) string {
	room := f.h.designRoom(artifacts.VersionKey{WorkspaceID: f.workspace, PageID: f.page})
	room.mu.Lock()
	defer room.mu.Unlock()
	for c := range room.clients {
		if c.hasID && c.clientID == id {
			return c.actor
		}
	}
	return ""
}

func (f *designAuthorityFixture) waitAwarenessHolder(t *testing.T, id uint64, actor string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for f.awarenessHolder(id) != actor {
		if time.Now().After(deadline) {
			t.Fatalf("awareness %d held by %q, want %q", id, f.awarenessHolder(id), actor)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *designAuthorityFixture) waitRevisionAbove(t *testing.T, before int64) bool {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if f.revision(t) > before {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func designTestUpdate(t *testing.T, id string) design.Result {
	t.Helper()
	update, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(fmt.Sprintf(`[{"op":"upsert","node":{"id":%q,"type":"rect","x":0,"y":0,"width":10,"height":10}}]`, id))})
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func designCloseCodeOf(t *testing.T, conn *websocket.Conn) int {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var closeErr *websocket.CloseError
		if errors.As(err, &closeErr) {
			return closeErr.Code
		}
		return -1
	}
}

// A Read-only socket answering sync step 1 must not run the Node Yjs engine
// while holding the global SQLite writer fence: every other writer in every
// workspace would wait for it.
func TestDesignWebsocketReaderSyncRunsEngineOutsideWriterFence(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	reader := "design-ws-fence-reader"
	f.reader(t, reader)
	var calls, blocked atomic.Int32
	real := designWSRun
	designWSRun = func(ctx context.Context, request design.Request) (design.Result, error) {
		if len(request.Frame) > 1 && request.Frame[0] == 0 && request.Frame[1] == 0 {
			calls.Add(1)
			done := make(chan error, 1)
			go func() {
				_, err := f.h.db.ExecContext(context.Background(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("unrelated write failed: %v", err)
				}
			case <-time.After(750 * time.Millisecond):
				// Returning lets the fenced transaction finish so the waiting
				// writer can complete; never wait for it here.
				blocked.Add(1)
			}
		}
		return real(ctx, request)
	}
	t.Cleanup(func() { designWSRun = real })
	_ = f.dial(t, reader)
	if calls.Load() == 0 {
		t.Fatal("reader sync step 1 did not reach the Design engine")
	}
	if blocked.Load() != 0 {
		t.Fatal("reader sync step 1 held the database writer fence while the Design engine ran")
	}
}

// A burst of sync step 1 requests is answered; a flood is closed rather than
// spawning one engine process per frame.
func TestDesignWebsocketRejectsSyncStepOneFlood(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	reader := "design-ws-flood-reader"
	f.reader(t, reader)
	conn := f.dial(t, reader)
	for range 2 * designSyncBurst {
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err != nil {
			break
		}
	}
	if code := designCloseCodeOf(t, conn); code != designCloseRateLimited {
		t.Fatalf("sync step 1 flood close code %d, want %d", code, designCloseRateLimited)
	}
}

// An update the engine refuses is not a permission change: answering it with
// 1008 sends the browser into its "denied" re-admission loop.
func TestDesignWebsocketRejectedUpdateIsNotPermissionClose(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	editor := f.dial(t, f.owner)
	before := f.revision(t)
	if err := editor.WriteMessage(websocket.BinaryMessage, designUpdateFrame([]byte{255})); err != nil {
		t.Fatal(err)
	}
	if code := designCloseCodeOf(t, editor); code != designCloseRejected {
		t.Fatalf("rejected update close code %d, want %d", code, designCloseRejected)
	}
	if f.revision(t) != before {
		t.Fatal("rejected update changed the live head")
	}
	// A reader sending an update is still a permission violation.
	reader := "design-ws-rejected-reader"
	f.reader(t, reader)
	conn := f.dial(t, reader)
	if err := conn.WriteMessage(websocket.BinaryMessage, designTestUpdate(t, "reader-write").Frame); err != nil {
		t.Fatal(err)
	}
	if code := designCloseCodeOf(t, conn); code != websocket.ClosePolicyViolation {
		t.Fatalf("reader update close code %d, want 1008", code)
	}
}

func TestDesignSyncRequestBucket(t *testing.T) {
	client := &designClient{}
	now := time.Unix(1_000_000, 0)
	for i := range designSyncBurst {
		if !client.allowSyncRequest(now) {
			t.Fatalf("burst request %d refused", i)
		}
	}
	if client.allowSyncRequest(now) {
		t.Fatal("request beyond the burst allowed")
	}
	if !client.allowSyncRequest(now.Add(designSyncRefill)) {
		t.Fatal("refilled request refused")
	}
	if client.allowSyncRequest(now.Add(designSyncRefill)) {
		t.Fatal("one refill allowed two requests")
	}
}

func TestDesignWebsocketCloseCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"edit denied", errDesignAccess, websocket.ClosePolicyViolation},
		{"authority epoch", artifacts.ErrVersionAuthority, websocket.ClosePolicyViolation},
		{"page gone", fmt.Errorf("load: %w", sql.ErrNoRows), websocket.ClosePolicyViolation},
		{"disabled", artifacts.ErrDisabled, websocket.ClosePolicyViolation},
		{"invalid update", fmt.Errorf("%w: src too long", design.ErrInvalid), designCloseRejected},
		{"invalid version", artifacts.ErrVersionInvalid, designCloseRejected},
		{"protocol", errDesignProtocol, websocket.CloseProtocolError},
		{"rate", errDesignRateLimited, designCloseRateLimited},
		{"engine unavailable", design.ErrUnavailable, websocket.CloseInternalServerErr},
		{"engine startup timeout", &design.TimeoutError{Phase: design.PhaseStartup, Budget: design.DefaultStartupTimeout}, websocket.CloseInternalServerErr},
		{"engine work timeout", &design.TimeoutError{Phase: design.PhaseWork, Budget: design.DefaultWorkTimeout}, websocket.CloseInternalServerErr},
		{"database busy", errors.New("database is locked"), websocket.CloseInternalServerErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := designCloseFor(tc.err); code != tc.code {
				t.Fatalf("close code %d, want %d", code, tc.code)
			}
		})
	}
}

// A Read-only viewer that claims an editor's awareness client ID must not lock
// that editor out of the live session.
func TestDesignWebsocketReaderCannotSquatEditorAwareness(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	reader := "design-ws-squatter"
	f.reader(t, reader)
	const id = 4242
	first := f.dial(t, f.owner)
	if err := first.WriteMessage(websocket.BinaryMessage, designTestAwarenessFrame(id, 1, `{"cursor":null}`)); err != nil {
		t.Fatal(err)
	}
	f.waitAwarenessHolder(t, id, f.owner)
	_ = first.Close()
	f.waitAwarenessHolder(t, id, "")
	squatter := f.dial(t, reader)
	if err := squatter.WriteMessage(websocket.BinaryMessage, designTestAwarenessFrame(id, 1, `{"cursor":null}`)); err != nil {
		t.Fatal(err)
	}
	f.waitAwarenessHolder(t, id, reader)
	editor := f.dial(t, f.owner)
	if err := editor.WriteMessage(websocket.BinaryMessage, designTestAwarenessFrame(id, 2, `{"cursor":null}`)); err != nil {
		t.Fatal(err)
	}
	before := f.revision(t)
	if err := editor.WriteMessage(websocket.BinaryMessage, designTestUpdate(t, "after-squat").Frame); err != nil {
		t.Fatal(err)
	}
	if !f.waitRevisionAbove(t, before) {
		t.Fatal("editor update was not persisted after a reader squatted its awareness ID")
	}
	_ = editor.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		_, _, err := editor.ReadMessage()
		if err == nil {
			continue
		}
		if websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
			t.Fatalf("editor was closed as a permission change: %v", err)
		}
		break
	}
}

// The same person reconnecting while the server still holds the old socket
// (a dropped network) replaces that connection instead of being refused.
func TestDesignWebsocketSameActorReclaimsAwareness(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	const id = 77
	zombie := f.dial(t, f.owner)
	if err := zombie.WriteMessage(websocket.BinaryMessage, designTestAwarenessFrame(id, 1, `{"cursor":null}`)); err != nil {
		t.Fatal(err)
	}
	f.waitAwarenessHolder(t, id, f.owner)
	editor := f.dial(t, f.owner)
	if err := editor.WriteMessage(websocket.BinaryMessage, designTestAwarenessFrame(id, 2, `{"cursor":null}`)); err != nil {
		t.Fatal(err)
	}
	if code := designCloseCodeOf(t, zombie); code == websocket.ClosePolicyViolation {
		t.Fatal("replaced connection was reported as a permission change")
	}
	before := f.revision(t)
	if err := editor.WriteMessage(websocket.BinaryMessage, designTestUpdate(t, "after-reclaim").Frame); err != nil {
		t.Fatal(err)
	}
	if !f.waitRevisionAbove(t, before) {
		t.Fatal("reconnected editor could not write after reclaiming its awareness ID")
	}
	// The retired connection must not announce the shared ID as gone.
	time.Sleep(200 * time.Millisecond)
	if holder := f.awarenessHolder(id); holder != f.owner {
		t.Fatalf("awareness %d holder %q after reclaim", id, holder)
	}
}
