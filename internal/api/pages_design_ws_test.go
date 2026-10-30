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

func (f *designAuthorityFixture) reader(t *testing.T, user string) {
	t.Helper()
	f.addUser(t, user)
	if w := f.grant(t, user, "read"); w.Code != 200 {
		t.Fatalf("read grant %d %s", w.Code, w.Body)
	}
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
		{"database busy", errors.New("database is locked"), websocket.CloseInternalServerErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := designCloseFor(tc.err); code != tc.code {
				t.Fatalf("close code %d, want %d", code, tc.code)
			}
		})
	}
}
