package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

// Docs presence (#5166): who else has a document open and where their caret
// and range are. It is ephemeral by design: held in this process's memory,
// never written to the database, the engine or the document, and gone after
// documentPresenceTTL without a heartbeat or on an explicit leave.
//
// Authority: every heartbeat runs the same live document read authorization
// as a document read (session or CLI token, workspace membership, Page grant,
// private custody, attached outputs). A heartbeat that fails it removes every
// entry of that person on that document and returns 404, so a revoked reader
// can neither stay listed nor read who else is there. Rooms are keyed by the
// authorized workspace and Page id: presence never crosses documents.
//
// Removal on revoke happens on the revoked person's next heartbeat (about a
// second) when the request still reaches this handler (a Page grant revoke).
// When it no longer does (membership removed, session or token revoked, scope
// lost) the middleware refuses it and the entry expires after the TTL; the
// same bound covers a revoke landing between the authorization and the room
// write of one heartbeat. Expired entries of every room are swept at least
// once per TTL, so tabs that vanish without a leave never accumulate.
//
// Identity is the server's: the user id comes from the authenticated request,
// the name from the user record of that workspace member, the colour from a
// per-document assignment. A client only says which tab it is and where its
// caret is (two encoded Yjs relative positions, opaque to the server).
const (
	documentPresenceTTL      = 6 * time.Second
	documentPresenceInterval = time.Second
	documentPresenceColors   = 8
	// Per document and per server: bounded memory whatever clients send.
	documentPresenceRoomLimit  = 64
	documentPresenceTotalLimit = 20000
	// Tabs of one person on one document, and on the whole server, so nobody
	// can fill a room or the server alone.
	documentPresenceUserTabLimit   = 8
	documentPresenceUserTotalLimit = 64
	// A full server forces a sweep of every room at most this often.
	documentPresenceForcedSweep = time.Second
	// One encoded Yjs relative position: a client id and clock, or a type key, plus assoc.
	documentPresencePositionLimit = 192
	// Canvas presence (#5149 C06): a pointer in canvas coordinates within the
	// surface's own coordinate bound, and at most this many selected cards.
	documentPresenceCanvasCoordinate = 1_000_000
	documentPresenceCanvasSelection  = 8
)

var documentPresenceClientPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// A selected card is a placement (a Page block ID) or a canvas item ID, as the surface stores them.
var documentPresenceCanvasIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// documentPresenceCanvasPoint is a pointer position in canvas (world)
// coordinates. Both are required: a missing or null coordinate is refused
// rather than relayed as 0.
type documentPresenceCanvasPoint struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

// documentPresenceCanvas is where a person is on the Canvas view: their
// pointer (absent once it leaves the canvas) and the cards they selected.
// It is as ephemeral as the caret: kept with the tab's entry, replaced by
// every heartbeat and gone with it after the TTL.
type documentPresenceCanvas struct {
	Pointer   *documentPresenceCanvasPoint `json:"pointer,omitempty"`
	Selection []string                     `json:"selection,omitempty"`
}

func validPresenceCanvas(canvas *documentPresenceCanvas) bool {
	if canvas == nil {
		return true
	}
	if p := canvas.Pointer; p != nil && (p.X == nil || p.Y == nil || math.IsNaN(*p.X) || math.IsNaN(*p.Y) || math.Abs(*p.X) > documentPresenceCanvasCoordinate || math.Abs(*p.Y) > documentPresenceCanvasCoordinate) {
		return false
	}
	if len(canvas.Selection) > documentPresenceCanvasSelection {
		return false
	}
	seen := map[string]bool{}
	for _, id := range canvas.Selection {
		if !documentPresenceCanvasIDPattern.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

type documentPresenceEntry struct {
	user, client, name string
	color              int
	anchor, head       string
	canvas             *documentPresenceCanvas
	seen               time.Time
}

type documentPresencePeer struct {
	Client string `json:"client"`
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Color  int    `json:"color"`
	Anchor string `json:"anchor,omitempty"`
	Head   string `json:"head,omitempty"`
	// Canvas is present only while the person is on the Canvas view.
	Canvas *documentPresenceCanvas `json:"canvas,omitempty"`
}

type documentPresenceHub struct {
	mu     sync.Mutex
	now    func() time.Time
	total  int
	swept  time.Time
	forced time.Time
	users  map[string]int
	rooms  map[string]map[string]*documentPresenceEntry
}

func newDocumentPresenceHub() *documentPresenceHub {
	return &documentPresenceHub{now: time.Now, users: map[string]int{}, rooms: map[string]map[string]*documentPresenceEntry{}}
}

func documentPresenceRoom(workspace, page string) string { return workspace + "\x00" + page }
func documentPresenceKey(user, client string) string     { return user + "\x00" + client }

// remove deletes one entry and its counts; the caller holds mu.
func (h *documentPresenceHub) remove(entries map[string]*documentPresenceEntry, key string) {
	entry := entries[key]
	if entry == nil {
		return
	}
	delete(entries, key)
	h.total--
	if h.users[entry.user]--; h.users[entry.user] <= 0 {
		delete(h.users, entry.user)
	}
}

// sweep removes expired entries of one room; the caller holds mu.
func (h *documentPresenceHub) sweep(room string) map[string]*documentPresenceEntry {
	entries := h.rooms[room]
	limit := h.now().Add(-documentPresenceTTL)
	for key, entry := range entries {
		if entry.seen.Before(limit) {
			h.remove(entries, key)
		}
	}
	if len(entries) == 0 {
		delete(h.rooms, room)
		return nil
	}
	return entries
}

// colour keeps a person's colour while any of their tabs is present, and
// otherwise starts from a stable per-document hash of the user id and takes
// the next colour nobody else on this document holds.
func documentPresenceColour(entries map[string]*documentPresenceEntry, room, user string) int {
	taken := map[int]bool{}
	for _, entry := range entries {
		if entry.user == user {
			return entry.color
		}
		taken[entry.color] = true
	}
	sum := fnv.New32a()
	_, _ = io.WriteString(sum, room+"\x00"+user)
	start := int(sum.Sum32() % documentPresenceColors)
	for step := 0; step < documentPresenceColors; step++ {
		if candidate := (start + step) % documentPresenceColors; !taken[candidate] {
			return candidate
		}
	}
	return start
}

// sweepAll removes expired entries of every room at most once per TTL (or
// whenever the server-wide limit is reached); the caller holds mu.
func (h *documentPresenceHub) sweepAll(force bool) {
	now := h.now()
	if !force && now.Sub(h.swept) < documentPresenceTTL || force && now.Sub(h.forced) < documentPresenceForcedSweep {
		return
	}
	h.swept = now
	if force {
		h.forced = now
	}
	for room := range h.rooms {
		h.sweep(room)
	}
}

// beat records one authorized heartbeat and returns the other people present.
func (h *documentPresenceHub) beat(room, user, client, name, anchor, head string, canvas *documentPresenceCanvas) (int, []documentPresencePeer, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepAll(false)
	entries := h.sweep(room)
	key := documentPresenceKey(user, client)
	entry := entries[key]
	if entry == nil {
		if h.total >= documentPresenceTotalLimit {
			h.sweepAll(true)
			entries = h.rooms[room]
		}
		tabs := 0
		for _, other := range entries {
			if other.user == user {
				tabs++
			}
		}
		if len(entries) >= documentPresenceRoomLimit || h.total >= documentPresenceTotalLimit || tabs >= documentPresenceUserTabLimit || h.users[user] >= documentPresenceUserTotalLimit {
			return 0, nil, false
		}
		if entries == nil {
			entries = map[string]*documentPresenceEntry{}
			h.rooms[room] = entries
		}
		entry = &documentPresenceEntry{user: user, client: client, color: documentPresenceColour(entries, room, user)}
		entries[key] = entry
		h.total++
		h.users[user]++
	}
	entry.name, entry.anchor, entry.head, entry.canvas, entry.seen = name, anchor, head, canvas, h.now()
	peers := []documentPresencePeer{}
	for _, other := range entries {
		if other.user == user {
			continue
		}
		peers = append(peers, documentPresencePeer{Client: other.client, UserID: other.user, Name: other.name, Color: other.color, Anchor: other.anchor, Head: other.head, Canvas: other.canvas})
	}
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Name != peers[j].Name {
			return peers[i].Name < peers[j].Name
		}
		return peers[i].Client < peers[j].Client
	})
	return entry.color, peers, true
}

func (h *documentPresenceHub) leave(room, user, client string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entries := h.rooms[room]; entries != nil {
		h.remove(entries, documentPresenceKey(user, client))
	}
	h.sweep(room)
}

// drop removes every tab of a person from one document: their access is gone.
func (h *documentPresenceHub) drop(room, user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := h.rooms[room]
	for key, entry := range entries {
		if entry.user == user {
			h.remove(entries, key)
		}
	}
	h.sweep(room)
}

func validPresencePosition(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > base64.StdEncoding.EncodedLen(documentPresencePositionLimit) {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) > 0 && len(decoded) <= documentPresencePositionLimit
}

// DocumentPresence is one heartbeat: authorize the live read, record where
// this tab's caret is, return the other people on the document.
func (r *Router) documentPresence(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if agent, _ := pageGrantCallerIsAgent(req); agent {
		replyError(w, 403, "human document credentials required")
		return
	}
	p := r.pages
	if p.documentCall == nil {
		replyError(w, 503, "Docs are not enabled on this server")
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 2048)
	var input struct {
		Client string `json:"client"`
		Anchor string `json:"anchor"`
		Head   string `json:"head"`
		Leave  bool   `json:"leave"`
		// Canvas: pointer and selection on the Canvas view (unknown fields inside are refused too).
		Canvas *documentPresenceCanvas `json:"canvas"`
	}
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !documentPresenceClientPattern.MatchString(input.Client) ||
		!validPresencePosition(input.Anchor) || !validPresencePosition(input.Head) || (input.Anchor == "") != (input.Head == "") || !validPresenceCanvas(input.Canvas) {
		replyError(w, 400, "presence needs a tab id and, optionally, both caret positions and a bounded canvas pointer and selection")
		return
	}
	if input.Canvas != nil && input.Canvas.Pointer == nil && len(input.Canvas.Selection) == 0 {
		input.Canvas = nil
	}
	user := UserFromContext(req.Context())
	workspace := WorkspaceIDFromContext(req.Context())
	if user == nil || workspace == "" {
		replyError(w, 404, "document page not found")
		return
	}
	rec, err := p.loadPageForRead(req.Context(), workspace, req.PathValue("slug"))
	if err != nil {
		// No room id without the page; entries of a deleted page expire with the TTL.
		replyError(w, 404, "document page not found")
		return
	}
	room := documentPresenceRoom(workspace, rec.ID)
	// Private output is read by its custodian alone (the audited administrator
	// read is a document read, not a presence): nobody else is ever listed there.
	allowed := !rec.PrivateAudience || rec.OwnerUserID == user.ID
	if allowed {
		allowed = r.documentPresenceReadable(req, documents.Key{WorkspaceID: workspace, PageID: rec.ID})
	}
	if !allowed {
		r.documentPresenceHub.drop(room, user.ID)
		replyError(w, 404, "document page not found")
		return
	}
	if input.Leave {
		r.documentPresenceHub.leave(room, user.ID, input.Client)
		writeJSON(w, 200, map[string]any{"peers": []documentPresencePeer{}})
		return
	}
	// The member's display name only: an email is never shown to co-readers,
	// who may hold no more than a grant on this one page.
	var name string
	if r.db.QueryRowContext(req.Context(), `SELECT coalesce(u.full_name,'') FROM users u JOIN workspace_members m ON m.user_id=u.id AND m.workspace_id=? WHERE u.id=?`, workspace, user.ID).Scan(&name) != nil || strings.TrimSpace(name) == "" {
		name = "Workspace member"
	}
	color, peers, ok := r.documentPresenceHub.beat(room, user.ID, input.Client, name, input.Anchor, input.Head, input.Canvas)
	if !ok {
		replyError(w, 429, "too many people on this document right now")
		return
	}
	writeJSON(w, 200, map[string]any{"peers": peers, "color": color, "ttl_ms": documentPresenceTTL.Milliseconds(), "interval_ms": documentPresenceInterval.Milliseconds()})
}

// documentPresenceReadable runs the document read authorization in a read-only transaction.
func (r *Router) documentPresenceReadable(req *http.Request, key documents.Key) bool {
	tx, err := r.db.BeginTx(req.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback() }()
	authority := &documentAuthority{pages: r.pages, middleware: r.authMw}
	_, err = authority.authorize(req, key, false, false)(req.Context(), tx)
	return err == nil
}
