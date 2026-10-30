package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
