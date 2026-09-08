// Package groupchat persists human workspace conversations independently of agent runs.
package groupchat

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrForbidden = errors.New("conversation access denied")
	ErrInvalid   = errors.New("invalid conversation request")
	ErrConflict  = errors.New("client message ID already used with different content")
)

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

type CreateInput struct {
	Title     string   `json:"title"`
	Kind      string   `json:"kind"`
	MemberIDs []string `json:"member_ids"`
}
type SendInput struct {
	ClientID          string   `json:"client_id"`
	Content           string   `json:"content"`
	MentionedAgentIDs []string `json:"mentioned_agent_ids,omitempty"`
}
type Conversation struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	CreatedBy        string `json:"created_by"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	LastSequence     int64  `json:"last_sequence"`
	LastReadSequence int64  `json:"last_read_sequence"`
	AccessScope      string `json:"access_scope"`
	UnreadCount      int64  `json:"unread_count"`
	Muted            bool   `json:"muted"`
	IsDirect         bool   `json:"is_direct"`
	DirectUserID     string `json:"direct_user_id,omitempty"`
	DirectUserName   string `json:"direct_user_name,omitempty"`
	DirectAvatarURL  string `json:"direct_avatar_url,omitempty"`
}
type Message struct {
	SourceKind        string   `json:"source_kind,omitempty"`
	ID                string   `json:"id"`
	ConversationID    string   `json:"conversation_id"`
	Sequence          int64    `json:"sequence"`
	AuthorUserID      string   `json:"author_user_id"`
	AuthorAgentID     string   `json:"author_agent_id,omitempty"`
	Kind              string   `json:"kind"`
	SubjectAgentID    string   `json:"subject_agent_id,omitempty"`
	MentionedAgentIDs []string `json:"mentioned_agent_ids"`
	AuthorName        string   `json:"author_name"`
	AuthorAvatarURL   string   `json:"author_avatar_url,omitempty"`
	AuthorAvatarStyle string   `json:"author_avatar_style,omitempty"`
	AuthorAvatarSeed  string   `json:"author_avatar_seed,omitempty"`
	AuthorSlug        string   `json:"author_slug,omitempty"`
	ClientID          string   `json:"client_id"`
	Content           string   `json:"content"`
	CreatedAt         string   `json:"created_at"`
}
type Member struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url,omitempty"`
	JoinedAt  string `json:"joined_at"`
	Role      string `json:"role"`
}
type Event struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	CreatedAt      string `json:"created_at"`
}
type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type scanner interface{ Scan(...any) error }

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "c" + strconv.FormatInt(time.Now().UnixMilli(), 36) + hex.EncodeToString(b[:])
}
func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// Take the SQLite write reservation before reading. This avoids deferred
// transaction read-to-write upgrades failing with SQLITE_BUSY_SNAPSHOT.
func (s *Store) write(ctx context.Context, fn func(querier) error) error {
	return WithWriteTransaction(ctx, s.db, func(c *sql.Conn) error { return fn(c) })
}

func workspaceMember(ctx context.Context, q querier, w, u string) error {
	var ok int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM workspace_members m JOIN workspaces w ON w.id=m.workspace_id JOIN users usr ON usr.id=m.user_id WHERE m.workspace_id=? AND m.user_id=? AND w.deleted_at IS NULL`, w, u).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}

const accessible = `c.workspace_id=? AND c.deleted_at IS NULL AND EXISTS(SELECT 1 FROM workspace_members wm JOIN workspaces w ON w.id=wm.workspace_id WHERE wm.workspace_id=c.workspace_id AND wm.user_id=? AND w.deleted_at IS NULL) AND (c.kind='channel' OR EXISTS(SELECT 1 FROM workspace_conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=?))`
const conversationBaseCols = `c.id,c.workspace_id,c.kind,c.is_direct,c.title,COALESCE(c.created_by,''),c.created_at,c.updated_at,c.last_sequence,COALESCE((SELECT last_read_sequence FROM workspace_conversation_members WHERE conversation_id=c.id AND user_id=?),0),COALESCE((SELECT muted FROM workspace_conversation_members WHERE conversation_id=c.id AND user_id=?),0),COALESCE((SELECT json_object('id',usr.id,'name',COALESCE(NULLIF(TRIM(usr.full_name),''),usr.id),'avatar_url',COALESCE(usr.avatar_url,'')) FROM workspace_conversation_members peer JOIN users usr ON usr.id=peer.user_id JOIN workspace_members wm ON wm.user_id=usr.id AND wm.workspace_id=c.workspace_id WHERE c.is_direct=1 AND peer.conversation_id=c.id AND peer.user_id<>? LIMIT 1),'{}')`
const conversationCols = conversationBaseCols + `,(SELECT COUNT(*) FROM workspace_conversation_messages um WHERE um.conversation_id=c.id AND um.sequence>COALESCE((SELECT last_read_sequence FROM workspace_conversation_members WHERE conversation_id=c.id AND user_id=?),0) AND (um.author_user_id IS NULL OR um.author_user_id<>?))`

func scanConversation(r scanner) (c Conversation, err error) {
	var peerJSON string
	err = r.Scan(&c.ID, &c.WorkspaceID, &c.Kind, &c.IsDirect, &c.Title, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt, &c.LastSequence, &c.LastReadSequence, &c.Muted, &peerJSON, &c.UnreadCount)
	if err == nil {
		var peer struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
		}
		err = json.Unmarshal([]byte(peerJSON), &peer)
		c.DirectUserID, c.DirectUserName, c.DirectAvatarURL = peer.ID, peer.Name, peer.AvatarURL
	}
	c.AccessScope = "participants"
	if c.Kind == "channel" {
		c.AccessScope = "workspace"
	}
	return
}
func get(ctx context.Context, q querier, w, u, id string) (Conversation, error) {
	c, err := scanConversation(q.QueryRowContext(ctx, `SELECT `+conversationCols+` FROM workspace_conversations c WHERE c.id=? AND `+accessible, u, u, u, u, u, id, w, u, u))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrForbidden
	}
	return c, err
}

// Mutations need access and the sequence, not a potentially large unread count.
func getForWrite(ctx context.Context, q querier, w, u, id string) (Conversation, error) {
	c, err := scanConversation(q.QueryRowContext(ctx, `SELECT `+conversationBaseCols+`,0 FROM workspace_conversations c WHERE c.id=? AND `+accessible, u, u, u, id, w, u, u))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrForbidden
	}
	return c, err
}
func (s *Store) Get(ctx context.Context, w, u, id string) (Conversation, error) {
	return get(ctx, s.db, w, u, id)
}
func (s *Store) Create(ctx context.Context, w, u string, in CreateInput) (Conversation, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Title) > 120 || (in.Kind != "channel" && in.Kind != "group") || len(in.MemberIDs) > 500 {
		return Conversation{}, ErrInvalid
	}
	c := Conversation{ID: newID(), WorkspaceID: w, Kind: in.Kind, Title: in.Title, CreatedBy: u, CreatedAt: now()}
	c.UpdatedAt = c.CreatedAt
	c.AccessScope = "participants"
	if c.Kind == "channel" {
		c.AccessScope = "workspace"
	}
	err := s.write(ctx, func(q querier) error {
		if err := workspaceMember(ctx, q, w, u); err != nil {
			return err
		}
		ids := map[string]bool{u: true}
		for _, id := range in.MemberIDs {
			ids[id] = true
		}
		for id := range ids {
			if err := workspaceMember(ctx, q, w, id); err != nil {
				return err
			}
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO workspace_conversations(id,workspace_id,kind,title,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, c.ID, w, c.Kind, c.Title, u, c.CreatedAt, c.UpdatedAt); err != nil {
			return err
		}
		for id := range ids {
			if _, err := q.ExecContext(ctx, `INSERT INTO workspace_conversation_members(conversation_id,user_id,joined_at) VALUES(?,?,?)`, c.ID, id, c.CreatedAt); err != nil {
				return err
			}
		}
		return nil
	})
	return c, err
}
func boundedLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}
func (s *Store) List(ctx context.Context, w, u string, limit int) ([]Conversation, error) {
	return s.ListPage(ctx, w, u, limit, 0)
}
func (s *Store) ListPage(ctx context.Context, w, u string, limit, offset int) ([]Conversation, error) {
	if offset < 0 {
		return nil, ErrInvalid
	}
	if err := workspaceMember(ctx, s.db, w, u); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+conversationCols+` FROM workspace_conversations c WHERE `+accessible+` ORDER BY c.updated_at DESC,c.id DESC LIMIT ? OFFSET ?`, u, u, u, u, u, w, u, u, boundedLimit(limit), offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const messageCols = `m.id,m.conversation_id,m.sequence,COALESCE(m.author_user_id,''),COALESCE(u.full_name,(SELECT name FROM agents WHERE id=m.author_agent_id),''),m.client_id,m.content,m.created_at,COALESCE(m.author_agent_id,''),m.kind,COALESCE(m.subject_agent_id,''),m.mentioned_agent_ids_json,COALESCE(u.avatar_url,''),COALESCE((SELECT ` + agentIdentityCols + ` FROM agents a LEFT JOIN crews cr ON cr.id=a.crew_id WHERE a.id=m.author_agent_id),'{}'),m.source_kind`

func scanMessage(r scanner) (m Message, err error) {
	var mentions, agentJSON string
	err = r.Scan(&m.ID, &m.ConversationID, &m.Sequence, &m.AuthorUserID, &m.AuthorName, &m.ClientID, &m.Content, &m.CreatedAt, &m.AuthorAgentID, &m.Kind, &m.SubjectAgentID, &mentions, &m.AuthorAvatarURL, &agentJSON, &m.SourceKind)
	if err == nil {
		err = json.Unmarshal([]byte(mentions), &m.MentionedAgentIDs)
		if err == nil && m.AuthorAgentID != "" {
			var a agentIdentity
			err = json.Unmarshal([]byte(agentJSON), &a)
			m.AuthorAvatarURL = a.url()
			m.AuthorAvatarStyle, m.AuthorAvatarSeed, m.AuthorSlug = a.Style, a.Seed, a.Slug
		}
	}
	return
}
func (s *Store) Messages(ctx context.Context, w, u, id string, after int64, limit int) ([]Message, error) {
	if after < 0 {
		return nil, ErrInvalid
	}
	// Authorization is part of the read statement, not just a preceding check.
	if _, err := s.Get(ctx, w, u, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM workspace_conversation_messages m LEFT JOIN users u ON u.id=m.author_user_id JOIN workspace_conversations c ON c.id=m.conversation_id WHERE c.id=? AND `+accessible+` AND m.sequence>? ORDER BY m.sequence LIMIT ?`, id, w, u, u, after, boundedLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) Send(ctx context.Context, w, u, id string, in SendInput) (Message, bool, error) {
	if len(in.MentionedAgentIDs) > 10 {
		return Message{}, false, ErrInvalid
	}
	in.MentionedAgentIDs = append([]string{}, in.MentionedAgentIDs...)
	slices.Sort(in.MentionedAgentIDs)
	in.MentionedAgentIDs = slices.Compact(in.MentionedAgentIDs)
	if strings.TrimSpace(in.Content) == "" || len(in.Content) > 32768 || !utf8.ValidString(in.Content) || strings.TrimSpace(in.ClientID) == "" || len(in.ClientID) > 128 {
		return Message{}, false, ErrInvalid
	}
	var m Message
	duplicate := false
	err := s.write(ctx, func(q querier) error {
		c, err := getForWrite(ctx, q, w, u, id)
		if err != nil {
			return err
		}
		existing, err := scanMessage(q.QueryRowContext(ctx, `SELECT `+messageCols+` FROM workspace_conversation_messages m LEFT JOIN users u ON u.id=m.author_user_id WHERE m.conversation_id=? AND m.author_user_id=? AND m.client_id=?`, id, u, in.ClientID))
		if err == nil {
			if existing.Content != in.Content || !slices.Equal(existing.MentionedAgentIDs, in.MentionedAgentIDs) {
				return ErrConflict
			}
			m = existing
			duplicate = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, agentID := range in.MentionedAgentIDs {
			if c.Kind != "channel" {
				return ErrInvalid
			}
			if err := channelAgent(ctx, q, w, id, agentID); err != nil {
				return err
			}
		}
		m = Message{ID: newID(), ConversationID: id, Sequence: c.LastSequence + 1, AuthorUserID: u, ClientID: in.ClientID, Content: in.Content, CreatedAt: now()}
		m.Kind = "message"
		m.MentionedAgentIDs = in.MentionedAgentIDs
		mentionsJSON, _ := json.Marshal(m.MentionedAgentIDs)
		if err = q.QueryRowContext(ctx, `SELECT COALESCE(full_name,''),COALESCE(avatar_url,'') FROM users WHERE id=?`, u).Scan(&m.AuthorName, &m.AuthorAvatarURL); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_messages(id,conversation_id,sequence,author_user_id,client_id,content,created_at,mentioned_agent_ids_json) VALUES(?,?,?,?,?,?,?,?)`, m.ID, id, m.Sequence, u, in.ClientID, in.Content, m.CreatedAt, string(mentionsJSON)); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE workspace_conversations SET last_sequence=?,updated_at=? WHERE id=?`, m.Sequence, m.CreatedAt, id); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_members(conversation_id,user_id,joined_at,last_read_sequence) VALUES(?,?,?,?) ON CONFLICT(conversation_id,user_id) DO NOTHING`, id, u, m.CreatedAt, 0); err != nil {
			return err
		}
		for _, agentID := range in.MentionedAgentIDs {
			if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_agent_jobs(id,conversation_id,message_id,agent_id,requested_by_user_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, newID(), id, m.ID, agentID, u, m.CreatedAt, m.CreatedAt); err != nil {
				return err
			}
		}
		_, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_outbox(id,conversation_id,message_id,created_at) VALUES(?,?,?,?)`, newID(), id, m.ID, m.CreatedAt)
		return err
	})
	return m, duplicate, err
}
func (s *Store) MarkRead(ctx context.Context, w, u, id string, sequence int64) error {
	if sequence < 0 {
		return ErrInvalid
	}
	return s.write(ctx, func(q querier) error {
		c, err := getForWrite(ctx, q, w, u, id)
		if err != nil {
			return err
		}
		if sequence > c.LastSequence {
			return ErrInvalid
		}
		_, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_members(conversation_id,user_id,joined_at,last_read_sequence) VALUES(?,?,?,?) ON CONFLICT(conversation_id,user_id) DO UPDATE SET last_read_sequence=MAX(last_read_sequence,excluded.last_read_sequence)`, id, u, now(), sequence)
		if err != nil {
			return err
		}
		return clearInbox(ctx, q, w, u, id, max(sequence, c.LastReadSequence))
	})
}
func (s *Store) Members(ctx context.Context, w, u, id string) ([]Member, error) {
	if _, err := s.Get(ctx, w, u, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT wm.user_id,COALESCE(usr.full_name,''),COALESCE(usr.avatar_url,''),COALESCE(cm.joined_at,wm.created_at),CASE WHEN c.created_by=wm.user_id THEN 'owner' ELSE 'member' END FROM workspace_conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id JOIN users usr ON usr.id=wm.user_id LEFT JOIN workspace_conversation_members cm ON cm.conversation_id=c.id AND cm.user_id=wm.user_id WHERE c.id=? AND `+accessible+` AND (c.kind='channel' OR cm.user_id IS NOT NULL) ORDER BY usr.full_name,wm.user_id`, id, w, u, u)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Name, &m.AvatarURL, &m.JoinedAt, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PendingEvents is for a trusted server consumer only. Delivery is at-least-once;
// consumers must deduplicate by event ID and recheck current recipient access.
func (s *Store) PendingEvents(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,message_id,created_at FROM workspace_conversation_outbox WHERE delivered_at IS NULL ORDER BY created_at,id LIMIT ?`, boundedLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ConversationID, &e.MessageID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) AcknowledgeEvent(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: event ID", ErrInvalid)
	}
	return s.write(ctx, func(q querier) error {
		_, err := q.ExecContext(ctx, `UPDATE workspace_conversation_outbox SET delivered_at=COALESCE(delivered_at,?) WHERE id=?`, now(), id)
		return err
	})
}

// AddMember explicitly grants existing history to another current workspace member.
func (s *Store) AddMember(ctx context.Context, w, u, id, target string) error {
	return s.write(ctx, func(q querier) error {
		c, err := getForWrite(ctx, q, w, u, id)
		if err != nil {
			return err
		}
		if c.CreatedBy != u {
			return ErrForbidden
		}
		if c.IsDirect {
			return ErrInvalid
		}
		if err = workspaceMember(ctx, q, w, target); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_members(conversation_id,user_id,joined_at) VALUES(?,?,?) ON CONFLICT(conversation_id,user_id) DO NOTHING`, id, target, now())
		return err
	})
}

// RemoveMember never implies removal from a public workspace channel.
func (s *Store) RemoveMember(ctx context.Context, w, u, id, target string) error {
	return s.write(ctx, func(q querier) error {
		c, err := getForWrite(ctx, q, w, u, id)
		if err != nil {
			return err
		}
		if c.CreatedBy != u {
			return ErrForbidden
		}
		if c.IsDirect {
			return ErrInvalid
		}
		if c.Kind != "group" || target == c.CreatedBy {
			return ErrInvalid
		}
		if _, err = q.ExecContext(ctx, `DELETE FROM workspace_conversation_members WHERE conversation_id=? AND user_id=?`, id, target); err != nil {
			return err
		}
		return clearInbox(ctx, q, w, target, id, c.LastSequence)
	})
}
func clearInbox(ctx context.Context, q querier, w, u, id string, sequence int64) error {
	_, err := q.ExecContext(ctx, `UPDATE inbox_items SET state='read',updated_at=? WHERE workspace_id=? AND state!='resolved' AND kind='message' AND source_id=? AND CAST(COALESCE(json_extract(payload_json,'$.last_sequence'),0) AS INTEGER)<=?`, now(), w, "conversation_"+id+"_"+u, sequence)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO inbox_item_reads(inbox_item_id,user_id,read_at) SELECT id,?,? FROM inbox_items WHERE workspace_id=? AND kind='message' AND source_id=? AND CAST(COALESCE(json_extract(payload_json,'$.last_sequence'),0) AS INTEGER)<=? ON CONFLICT(inbox_item_id,user_id) DO UPDATE SET read_at=excluded.read_at`, u, now(), w, "conversation_"+id+"_"+u, sequence)
	return err
}

// MessagesBefore returns a newest page, ascending for display. Zero starts at the
// current end; a positive sequence is an exclusive backward pagination cursor.
func (s *Store) MessagesBefore(ctx context.Context, w, u, id string, before int64, limit int) ([]Message, error) {
	if before < 0 {
		return nil, ErrInvalid
	}
	if _, err := s.Get(ctx, w, u, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM workspace_conversation_messages m LEFT JOIN users u ON u.id=m.author_user_id JOIN workspace_conversations c ON c.id=m.conversation_id WHERE c.id=? AND `+accessible+` AND (?=0 OR m.sequence<?) ORDER BY m.sequence DESC LIMIT ?`, id, w, u, u, before, before, boundedLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	slices.Reverse(out)
	return out, rows.Err()
}

// SetMuted suppresses future inbox projection, preserving history and unread cursors.
func (s *Store) SetMuted(ctx context.Context, w, u, id string, muted bool) error {
	return s.write(ctx, func(q querier) error {
		if _, err := getForWrite(ctx, q, w, u, id); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `INSERT INTO workspace_conversation_members(conversation_id,user_id,joined_at,muted) VALUES(?,?,?,?) ON CONFLICT(conversation_id,user_id) DO UPDATE SET muted=excluded.muted`, id, u, now(), muted)
		return err
	})
}
