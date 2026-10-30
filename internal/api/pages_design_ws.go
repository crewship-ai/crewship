package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/design"
	"github.com/gorilla/websocket"
)

type designClient struct {
	conn            *websocket.Conn
	out             chan []byte
	actor           string
	displayName     string
	canEdit         bool
	canReadLive     bool
	awareness       []byte
	clientID, clock uint64
	hasID           bool
}

func (client *designClient) enqueue(frame []byte) error {
	select {
	case client.out <- frame:
		return nil
	default:
		if client.conn != nil {
			_ = client.conn.Close()
		}
		return errors.New("Design client is too slow")
	}
}
func (room *designRoom) broadcast(frame []byte, except *designClient) {
	for client := range room.clients {
		if client != except {
			if len(frame) > 0 && frame[0] == 0 && !client.canEdit && !client.canReadLive {
				continue
			}
			_ = client.enqueue(frame)
		}
	}
}

// The standard JavaScript peers represent protocol integers exactly through 2^53-1.
const designMaxSafeUint uint64 = 1<<53 - 1

func designUint(raw []byte, index *int) (uint64, error) {
	var n uint64
	for shift := uint(0); shift < 56; shift += 7 {
		if *index >= len(raw) {
			return 0, io.ErrUnexpectedEOF
		}
		b := raw[*index]
		*index++
		n |= uint64(b&127) << shift
		if n > designMaxSafeUint {
			return 0, errors.New("protocol integer exceeds JavaScript safe range")
		}
		if b < 128 {
			return n, nil
		}
	}
	return 0, errors.New("invalid protocol integer")
}

// y-protocols sync update message: envelope 0, subtype 2, varuint8array.
func designUpdateFrame(update []byte) []byte {
	out := []byte{0, 2}
	out = designAppendUint(out, uint64(len(update)))
	return append(out, update...)
}
func designAppendUint(raw []byte, n uint64) []byte {
	for n >= 128 {
		raw = append(raw, byte(n)|128)
		n >>= 7
	}
	return append(raw, byte(n))
}
func designBytes(raw []byte, index *int) ([]byte, error) {
	n, e := designUint(raw, index)
	if e != nil || n > uint64(len(raw)-*index) {
		return nil, io.ErrUnexpectedEOF
	}
	b := raw[*index : *index+int(n)]
	*index += int(n)
	return b, nil
}
func designSyncKind(raw []byte) (uint64, uint64, error) {
	i := 0
	t, e := designUint(raw, &i)
	if e != nil {
		return 0, 0, e
	}
	if t != 0 {
		return t, 0, nil
	}
	s, e := designUint(raw, &i)
	if s > 2 {
		return 0, 0, errors.New("unknown sync type")
	}
	return t, s, e
}

// Awareness is the standard y-protocols payload. One connection owns one client
// ID. Names are taken from host authority; arbitrary client names aren't trusted.
func designAwareness(raw []byte, client *designClient, room *designRoom) ([]byte, error) {
	i := 0
	kind, e := designUint(raw, &i)
	if e != nil || kind != 1 {
		return nil, errors.New("invalid awareness frame")
	}
	payload, e := designBytes(raw, &i)
	if e != nil || i != len(raw) {
		return nil, errors.New("invalid awareness length")
	}
	i = 0
	n, e := designUint(payload, &i)
	if e != nil || n != 1 {
		return nil, errors.New("one awareness identity per connection required")
	}
	id, e := designUint(payload, &i)
	if e != nil {
		return nil, e
	}
	clock, e := designUint(payload, &i)
	if e != nil {
		return nil, e
	}
	state, e := designBytes(payload, &i)
	if e != nil || i != len(payload) {
		return nil, errors.New("invalid awareness state")
	}
	if client.hasID && client.clientID != id {
		return nil, errors.New("awareness identity changed")
	}
	for c := range room.clients {
		if c != client && c.hasID && c.clientID == id {
			return nil, errors.New("awareness identity already connected")
		}
	}
	var value map[string]any
	if string(state) != "null" {
		if json.Unmarshal(state, &value) != nil || value == nil {
			return nil, errors.New("invalid awareness JSON")
		}
		user, _ := value["user"].(map[string]any)
		if user == nil {
			user = map[string]any{}
		}
		user["name"] = client.displayName
		user["id"] = client.actor
		value["user"] = user
		state, _ = json.Marshal(value)
	}
	client.hasID = true
	client.clientID = id
	client.clock = clock
	out := designAppendUint(nil, 1)
	out = designAppendUint(out, id)
	out = designAppendUint(out, clock)
	out = designAppendUint(out, uint64(len(state)))
	out = append(out, state...)
	frame := designAppendUint(nil, 1)
	frame = designAppendUint(frame, uint64(len(out)))
	frame = append(frame, out...)
	client.awareness = frame
	return frame, nil
}
func designAwarenessGone(c *designClient) []byte {
	p := designAppendUint(nil, 1)
	p = designAppendUint(p, c.clientID)
	clock := c.clock
	// y-protocols permits a null removal at the current clock. At the maximum
	// safe clock, use that rule instead of emitting an unrepresentable value.
	if clock < designMaxSafeUint {
		clock++
	}
	p = designAppendUint(p, clock)
	p = designAppendUint(p, 4)
	p = append(p, []byte("null")...)
	out := designAppendUint(nil, 1)
	out = designAppendUint(out, uint64(len(p)))
	return append(out, p...)
}
func (h *PageHandler) DesignWebsocket(w http.ResponseWriter, r *http.Request) {
	k, r, ok := h.designKey(w, r)
	if !ok {
		return
	}
	// Standard same-origin credentialed websocket. A foreign browser origin never
	// obtains a socket, even when it would send ambient session cookies.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || !strings.EqualFold(u.Host, r.Host) {
			replyError(w, 403, "same-origin Design websocket required")
			return
		}
	}
	expectedRevision := r.URL.Query().Get("authority_revision")
	room := h.designRoom(k)
	room.mu.Lock()
	var actor, displayName, admittedRevision string
	var initial design.Result
	canEdit := false
	canReadLive := false
	err := h.designTx(r, k, false, func(ctx context.Context, tx *sql.Tx, a string) error {
		actor = a
		var epochErr error
		admittedRevision, epochErr = designAuthorityRevision(ctx, tx, k.WorkspaceID)
		if epochErr != nil {
			return epochErr
		}
		// Browser transport renewal must not replay an old document after a
		// grant changes between its metadata read and websocket admission.
		if expectedRevision != "" && expectedRevision != admittedRevision {
			return errDesignAccess
		}
		if e := tx.QueryRowContext(ctx, `SELECT coalesce(nullif(full_name,''),email,id) FROM users WHERE id=?`, UserFromContext(ctx).ID).Scan(&displayName); e != nil {
			return e
		}
		_, e := h.designAuthority(r, k, true)(ctx, tx)
		canEdit = e == nil
		canReadLive = canEdit
		if !canReadLive {
			canReadLive, e = h.designManagerRead(ctx, tx, k)
			if e != nil {
				return e
			}
		}
		if canReadLive {
			var state []byte
			if e = tx.QueryRowContext(ctx, `SELECT state FROM artifact_design_documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&state); e != nil {
				return e
			}
			initial, e = design.Run(ctx, design.Request{State: state})
			if e != nil {
				return e
			}
		}
		return nil
	})
	room.mu.Unlock()
	if err != nil {
		designError(w, err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), designAdmissionKey{}, designAdmission{WorkspaceID: k.WorkspaceID, Revision: admittedRevision}))
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: 8192, WriteBufferSize: 8192}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(4 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	client := &designClient{conn: conn, out: make(chan []byte, 64), actor: actor, displayName: displayName, canEdit: canEdit, canReadLive: canReadLive}
	room.mu.Lock()
	if canReadLive {
		_ = client.enqueue(initial.Hello)
	}
	room.clients[client] = true
	for c := range room.clients {
		if c != client && len(c.awareness) > 0 {
			if client.enqueue(c.awareness) != nil {
				delete(room.clients, client)
				room.mu.Unlock()
				return
			}
		}
	}
	room.mu.Unlock()
	defer func() {
		room.mu.Lock()
		delete(room.clients, client)
		if client.hasID {
			room.broadcast(designAwarenessGone(client), nil)
		}
		room.mu.Unlock()
	}()
	incoming := make(chan []byte, 16)
	done := make(chan struct{})
	finish := beginBackgroundWork()
	go func() {
		defer finish()
		defer close(done)
		for {
			typ, b, e := conn.ReadMessage()
			if e != nil {
				return
			}
			if typ != websocket.BinaryMessage {
				return
			}
			select {
			case incoming <- b:
			case <-r.Context().Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	closePolicy := func() {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1008, "Design permissions changed"), time.Now().Add(time.Second))
	}
	// Every content delivery is protected by the writer fence too: a committed
	// revocation cannot leave a queued update readable on an old socket.
	send := func(frame []byte) error {
		return h.designTx(r, k, false, func(_ context.Context, _ *sql.Tx, _ string) error {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			return conn.WriteMessage(websocket.BinaryMessage, frame)
		})
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-done:
			return
		case frame := <-client.out:
			if send(frame) != nil {
				closePolicy()
				return
			}
		case <-ping.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		case <-ticker.C:
			e := h.designTx(r, k, canEdit, func(_ context.Context, _ *sql.Tx, _ string) error { return nil })
			if e != nil {
				closePolicy()
				return
			}
			if canEdit {
				room.mu.Lock()
				var revision, saved int64
				var updated string
				e = h.db.QueryRowContext(r.Context(), `SELECT revision,saved_revision,updated_at FROM artifact_design_documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&revision, &saved, &updated)
				stamp, _ := time.Parse(time.RFC3339Nano, updated)
				if e == nil && revision > saved && time.Since(stamp) > 10*time.Second {
					_, _ = h.designSnapshotLocked(r, k, "Automatic Design checkpoint")
				}
				room.mu.Unlock()
			}
		case frame := <-incoming:
			kind, sub, e := designSyncKind(frame)
			if e != nil {
				closePolicy()
				return
			}
			room.mu.Lock()
			if kind == 1 {
				e = h.designTx(r, k, false, func(_ context.Context, _ *sql.Tx, _ string) error { return nil })
				if e == nil {
					var sanitized []byte
					sanitized, e = designAwareness(frame, client, room)
					if e == nil {
						room.broadcast(sanitized, client)
					}
				}
			} else if kind == 0 {
				if sub != 0 {
					if !canEdit {
						e = errDesignAccess
					} else {
						var result design.Result
						result, e = h.designApplyLocked(newDesignAdminAuditRequest(r, "write"), k, design.Request{Frame: frame})
						if e == nil {
							room.broadcast(designUpdateFrame(result.Update), client)
							if len(result.Response) > 0 {
								e = client.enqueue(result.Response)
							}
						}
					}
				} else {
					var result design.Result
					e = h.designTx(r, k, false, func(ctx context.Context, tx *sql.Tx, _ string) error {
						var state []byte
						var x error
						if canReadLive {
							x = tx.QueryRowContext(ctx, `SELECT state FROM artifact_design_documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&state)
						} else {
							state, x = designSharedState(ctx, tx, k)
						}
						if x != nil {
							return x
						}
						result, x = design.Run(ctx, design.Request{State: state, Frame: frame})
						return x
					})
					if e == nil && len(result.Response) > 0 {
						e = client.enqueue(result.Response)
					}
				}
			} else {
				e = errors.New("unsupported Design protocol message")
			}
			room.mu.Unlock()
			if e != nil {
				closePolicy()
				return
			}
		}
	}
}
