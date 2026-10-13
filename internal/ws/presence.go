package ws

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const presenceTTL = 15 * time.Second

type presenceCursor struct {
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	Space  string   `json:"coordinate_space"`
	Width  *float64 `json:"viewport_width,omitempty"`
	Height *float64 `json:"viewport_height,omitempty"`
}
type presenceState struct {
	Schema   int             `json:"schema_version"`
	Revision *int64          `json:"revision,omitempty"`
	BoardSeq *int64          `json:"board_seq,omitempty"`
	Cursor   *presenceCursor `json:"cursor,omitempty"`
}
type presenceMember struct {
	ID    string
	User  string
	Name  string
	State presenceState
	At    time.Time
}
type presenceRate struct {
	At    time.Time
	Count int
}
type presenceRoom struct {
	EmitRate presenceRate
	Members  map[*Client]*presenceMember
	Rates    map[string]presenceRate
}

func isContentRoom(channel string) bool {
	kind, id, ok := strings.Cut(channel, ":")
	return ok && id != "" && len(id) <= 128 && !strings.Contains(id, ":") && (kind == "artifact" || kind == "board")
}
func (h *Hub) joinPresence(c *Client, channel string, now time.Time) bool {
	// Existing cursor updates do not resolve identity or perform extra IO.
	h.presenceMu.Lock()
	resolver := h.presenceDisplayName
	if room := h.presenceRooms[channel]; room != nil && room.Members[c] != nil {
		h.presenceMu.Unlock()
		return true
	}
	h.presenceMu.Unlock()
	name := ""
	if resolver != nil {
		parent := c.ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, 200*time.Millisecond)
		if resolved, err := resolver(ctx, c.userID); err == nil {
			name = boundedPresenceName(resolved)
		}
		cancel()
	}
	h.presenceMu.Lock()
	defer h.presenceMu.Unlock()
	if h.presenceRooms == nil {
		h.presenceRooms = map[string]*presenceRoom{}
	}
	room := h.presenceRooms[channel]
	if room != nil {
		if member := room.Members[c]; member != nil {
			return true
		}
	}
	total, clientRooms, userTabs := 0, 0, 0
	for _, r := range h.presenceRooms {
		total += len(r.Members)
		if r.Members[c] != nil {
			clientRooms++
		}
	}
	if room != nil {
		for _, m := range room.Members {
			if m.User == c.userID {
				userTabs++
			}
		}
	}
	if total >= 1000 || clientRooms >= 8 || userTabs >= 4 || (room != nil && len(room.Members) >= 64) {
		return false
	}
	if room == nil {
		room = &presenceRoom{Members: map[*Client]*presenceMember{}, Rates: map[string]presenceRate{}}
		h.presenceRooms[channel] = room
	}
	room.Members[c] = &presenceMember{ID: rand.Text(), User: c.userID, Name: name, At: now, State: presenceState{Schema: 1}}
	return true
}
func (h *Hub) emitPresence(channel string) {
	h.presenceMu.Lock()
	now := time.Now()
	if now.Sub(h.presenceEmitRate.At) >= time.Second {
		h.presenceEmitRate = presenceRate{At: now}
	}
	if h.presenceEmitRate.Count >= 200 {
		h.presenceMu.Unlock()
		return
	}
	if room := h.presenceRooms[channel]; room != nil {
		if now.Sub(room.EmitRate.At) >= time.Second {
			room.EmitRate = presenceRate{At: now}
		}
		if room.EmitRate.Count >= 20 {
			h.presenceMu.Unlock()
			return
		}
		room.EmitRate.Count++
	}
	h.presenceEmitRate.Count++
	members := []map[string]any{}
	if room := h.presenceRooms[channel]; room != nil {
		for _, m := range room.Members {
			members = append(members, map[string]any{"connection_id": m.ID, "user_id": m.User, "display_name": m.Name, "revision": m.State.Revision, "board_seq": m.State.BoardSeq, "cursor": m.State.Cursor})
		}
	}
	data, err := json.Marshal(ServerMessage{Type: "presence.snapshot", Channel: channel, Payload: map[string]any{"schema_version": 1, "members": members}})
	h.presenceMu.Unlock()
	if err == nil {
		select {
		case h.broadcast <- ChannelMessage{Channel: channel, Data: data}:
		default:
		}
	}
}
func (h *Hub) removePresence(c *Client, channel string) {
	h.presenceMu.Lock()
	changed := []string{}
	for ch, room := range h.presenceRooms {
		if channel != "" && ch != channel {
			continue
		}
		if m := room.Members[c]; m != nil {
			delete(room.Members, c)
			remaining := false
			for _, other := range room.Members {
				if other.User == m.User {
					remaining = true
				}
			}
			if !remaining {
				delete(room.Rates, m.User)
			}
			changed = append(changed, ch)
		}
		if len(room.Members) == 0 {
			delete(h.presenceRooms, ch)
		}
	}
	h.presenceMu.Unlock()
	for _, ch := range changed {
		h.emitPresence(ch)
	}
}
func (h *Hub) expirePresence(now time.Time) {
	h.presenceMu.Lock()
	changed := []string{}
	for ch, room := range h.presenceRooms {
		removed := false
		for c, m := range room.Members {
			if now.Sub(m.At) >= presenceTTL {
				delete(room.Members, c)
				removed = true
			}
		}
		if removed {
			changed = append(changed, ch)
			for user := range room.Rates {
				found := false
				for _, m := range room.Members {
					if m.User == user {
						found = true
					}
				}
				if !found {
					delete(room.Rates, user)
				}
			}
		}
		if len(room.Members) == 0 {
			delete(h.presenceRooms, ch)
		}
	}
	h.presenceMu.Unlock()
	for _, ch := range changed {
		h.emitPresence(ch)
	}
}
func decodePresence(raw []byte, channel string) (presenceState, error) {
	var state presenceState
	if !utf8.Valid(raw) || len(raw) > 2048 || !isContentRoom(channel) {
		return state, fmt.Errorf("invalid presence payload")
	}
	// Exact key and duplicate checks prevent Go's case-insensitive aliases.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	allowed := map[string]bool{"schema_version": true, "revision": true, "board_seq": true, "cursor": true, "x": true, "y": true, "coordinate_space": true, "viewport_width": true, "viewport_height": true}
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 3 {
			return fmt.Errorf("presence nesting")
		}
		tok, err := decoder.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d != '{' {
				return fmt.Errorf("presence object required")
			}
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || !allowed[s] || seen[s] {
					return fmt.Errorf("invalid presence key")
				}
				seen[s] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return state, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return state, fmt.Errorf("one presence value required")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, err
	}
	if state.Schema != 1 {
		return state, fmt.Errorf("presence schema required")
	}
	board := strings.HasPrefix(channel, "board:")
	if board {
		if state.Revision != nil || state.BoardSeq == nil || *state.BoardSeq < 0 || *state.BoardSeq > 9007199254740991 {
			return state, fmt.Errorf("board sequence required")
		}
	} else if state.BoardSeq != nil || state.Revision == nil || *state.Revision < 1 || *state.Revision > 9007199254740991 {
		return state, fmt.Errorf("artifact revision required")
	}
	if c := state.Cursor; c != nil {
		valid := func(v *float64, min, max float64) bool {
			return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= min && *v <= max
		}
		if !valid(c.X, -1000000, 1000000) || !valid(c.Y, -1000000, 1000000) {
			return state, fmt.Errorf("invalid cursor")
		}
		if board {
			if c.Space != "board" || c.Width != nil || c.Height != nil {
				return state, fmt.Errorf("board cursor required")
			}
		} else if c.Space != "viewport" || !valid(c.Width, 1, 16384) || !valid(c.Height, 1, 16384) || *c.X < 0 || *c.Y < 0 || *c.X > *c.Width || *c.Y > *c.Height {
			return state, fmt.Errorf("viewport cursor required")
		}
	}
	return state, nil
}
func (c *Client) handlePresence(msg ClientMessage) {
	state, err := decodePresence(msg.Payload, msg.Channel)
	if err != nil {
		c.trySendError(msg.Channel, "invalid presence payload")
		return
	}
	c.mu.Lock()
	subscribed := c.channels[msg.Channel]
	now := time.Now()
	if now.Sub(c.presenceWindow) >= time.Second {
		c.presenceWindow = now
		c.presenceCount = 0
	}
	if c.presenceCount >= 10 {
		c.mu.Unlock()
		return
	}
	c.presenceCount++
	c.mu.Unlock()
	if !subscribed || c.hub.channelAuth == nil {
		c.trySendError(msg.Channel, "presence subscription required")
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	allowed, err := c.hub.channelAuth.CanSubscribe(ctx, c.userID, msg.Channel)
	cancel()
	if err != nil || !allowed {
		c.unsubscribe(msg.Channel)
		c.trySendError(msg.Channel, "access revoked")
		return
	}
	h := c.hub
	// A TTL-expired connection may restore its own bounded membership, but only
	// after the current subscription and live ACL checks above.
	if !h.joinPresence(c, msg.Channel, now) {
		c.trySendError(msg.Channel, "presence room limit reached")
		return
	}
	h.presenceMu.Lock()
	room := h.presenceRooms[msg.Channel]
	if room == nil {
		h.presenceMu.Unlock()
		return
	}
	rate := room.Rates[c.userID]
	if now.Sub(rate.At) >= time.Second {
		rate = presenceRate{At: now}
	}
	if rate.Count >= 10 {
		h.presenceMu.Unlock()
		return
	}
	rate.Count++
	room.Rates[c.userID] = rate
	if member := room.Members[c]; member != nil {
		member.State = state
		member.At = now
	}
	h.presenceMu.Unlock()
	h.emitPresence(msg.Channel)
}

// Configure at startup. Names are resolved only for authenticated, authorized
// room participants, never from browser-supplied presence fields.
func (h *Hub) SetPresenceDisplayNameResolver(resolve func(context.Context, string) (string, error)) {
	h.presenceMu.Lock()
	defer h.presenceMu.Unlock()
	h.presenceDisplayName = resolve
}
func boundedPresenceName(name string) string {
	if !utf8.ValidString(name) {
		return ""
	}
	runes := make([]rune, 0, 128)
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if len(runes) >= 128 {
			break
		}
		runes = append(runes, r)
	}
	return strings.TrimSpace(string(runes))
}
