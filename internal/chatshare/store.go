// Package chatshare stores short-lived, read-only capabilities for a single chat.
// It does not grant agent execution, file access, or access to another chat.
package chatshare

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	TokenPrefix = "cshr_"
	DefaultTTL  = 24 * time.Hour
	MaxTTL      = 7 * 24 * time.Hour
)

var (
	ErrDenied  = errors.New("chat share unavailable")
	ErrInvalid = errors.New("invalid chat share request")
)

type Grant struct {
	ID             string     `json:"id"`
	WorkspaceID    string     `json:"workspace_id"`
	AgentID        string     `json:"agent_id"`
	ChatID         string     `json:"chat_id"`
	IssuedByUserID string     `json:"issued_by_user_id"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Create mints a fresh random capability. Its plaintext is returned once.
// Membership and ownership are checked in the INSERT statement so a revoked
// actor cannot create a grant using a stale earlier authorization result.
func (s *Store) Create(ctx context.Context, workspaceID, agentID, chatID, issuerID string, ttl time.Duration) (Grant, string, error) {
	if workspaceID == "" || agentID == "" || chatID == "" || issuerID == "" {
		return Grant{}, "", ErrInvalid
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < time.Second || ttl > MaxTTL {
		return Grant{}, "", ErrInvalid
	}
	idBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return Grant{}, "", fmt.Errorf("generate chat share id: %w", err)
	}
	if _, err := rand.Read(tokenBytes); err != nil {
		return Grant{}, "", fmt.Errorf("generate chat share token: %w", err)
	}
	now := time.Now().UTC()
	grant := Grant{
		ID: "cshr_" + hex.EncodeToString(idBytes), WorkspaceID: workspaceID,
		AgentID: agentID, ChatID: chatID, IssuedByUserID: issuerID,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	res, err := s.db.ExecContext(ctx, `INSERT INTO chat_read_shares
		(id, workspace_id, agent_id, chat_id, issued_by_user_id, token_hash, created_at, expires_at)
		SELECT ?, c.workspace_id, c.agent_id, c.id, ?, ?, ?, ?
		FROM chats c
		JOIN workspaces ws ON ws.id=c.workspace_id AND ws.deleted_at IS NULL
		JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
		LEFT JOIN crews cr ON cr.id=a.crew_id AND cr.workspace_id=a.workspace_id AND cr.deleted_at IS NULL
		JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
		WHERE c.id=? AND c.agent_id=? AND c.workspace_id=?
		  AND c.mode='CHAT' AND (c.origin IS NULL OR c.origin IN ('UI','CLI'))
		  AND c.created_by IS NOT NULL
		  AND (a.crew_id IS NULL OR cr.id IS NOT NULL)
		  AND (c.created_by=? OR wm.role IN ('OWNER','ADMIN'))`,
		grant.ID, issuerID, hash[:], formatTime(now), formatTime(grant.ExpiresAt),
		issuerID, chatID, agentID, workspaceID, issuerID)
	if err != nil {
		return Grant{}, "", fmt.Errorf("create chat share: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Grant{}, "", fmt.Errorf("create chat share: %w", err)
	}
	if n != 1 {
		return Grant{}, "", ErrDenied
	}
	return grant, token, nil
}

// List is an administrative view of grants for one chat. It never returns
// token material and requires the caller to hold current sharing authority.
func (s *Store) List(ctx context.Context, workspaceID, agentID, chatID, actorID string) ([]Grant, error) {
	if workspaceID == "" || agentID == "" || chatID == "" || actorID == "" {
		return nil, ErrInvalid
	}
	allowed, err := s.mayManage(ctx, workspaceID, agentID, chatID, actorID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrDenied
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, agent_id, chat_id,
		issued_by_user_id, created_at, expires_at, revoked_at
		FROM chat_read_shares WHERE workspace_id=? AND agent_id=? AND chat_id=?
		ORDER BY created_at DESC, id DESC`, workspaceID, agentID, chatID)
	if err != nil {
		return nil, fmt.Errorf("list chat shares: %w", err)
	}
	defer rows.Close()
	grants := []Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list chat shares: %w", err)
	}
	return grants, nil
}

// Revoke immediately invalidates a grant on the next validation request.
func (s *Store) Revoke(ctx context.Context, workspaceID, agentID, chatID, id, actorID string) error {
	if workspaceID == "" || agentID == "" || chatID == "" || id == "" || actorID == "" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE chat_read_shares SET revoked_at=COALESCE(revoked_at, ?)
		WHERE id=? AND workspace_id=? AND agent_id=? AND chat_id=?
		  AND EXISTS (
		    SELECT 1 FROM chats c
		    JOIN workspaces ws ON ws.id=c.workspace_id AND ws.deleted_at IS NULL
		    JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
		    LEFT JOIN crews cr ON cr.id=a.crew_id AND cr.workspace_id=a.workspace_id AND cr.deleted_at IS NULL
		    JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
		    WHERE c.id=chat_read_shares.chat_id AND c.agent_id=chat_read_shares.agent_id
		      AND c.workspace_id=chat_read_shares.workspace_id
		      AND c.mode='CHAT' AND (c.origin IS NULL OR c.origin IN ('UI','CLI'))
		      AND c.created_by IS NOT NULL
		      AND (a.crew_id IS NULL OR cr.id IS NOT NULL)
		      AND (c.created_by=? OR wm.role IN ('OWNER','ADMIN'))
		  )`, formatTime(time.Now().UTC()), id, workspaceID, agentID, chatID, actorID, actorID)
	if err != nil {
		return fmt.Errorf("revoke chat share: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke chat share: %w", err)
	}
	if n != 1 {
		return ErrDenied
	}
	return nil
}

// Validate performs every security check at use time. Missing, bad-token,
// expired, revoked and no-longer-authorized grants have one indistinguishable
// error; storage failures remain errors so callers fail closed without hiding
// an outage as an absent grant.
func (s *Store) Validate(ctx context.Context, id, token string) (Grant, error) {
	if !validToken(token) || id == "" {
		return Grant{}, ErrDenied
	}
	var hash []byte
	var created, expires string
	var revoked sql.NullString
	var g Grant
	err := s.db.QueryRowContext(ctx, `SELECT sh.id, sh.workspace_id, sh.agent_id, sh.chat_id,
		sh.issued_by_user_id, sh.token_hash, sh.created_at, sh.expires_at, sh.revoked_at
		FROM chat_read_shares sh
		JOIN chats c ON c.id=sh.chat_id AND c.agent_id=sh.agent_id AND c.workspace_id=sh.workspace_id
		JOIN workspaces ws ON ws.id=sh.workspace_id AND ws.deleted_at IS NULL
		JOIN agents a ON a.id=sh.agent_id AND a.workspace_id=sh.workspace_id AND a.deleted_at IS NULL
		LEFT JOIN crews cr ON cr.id=a.crew_id AND cr.workspace_id=a.workspace_id AND cr.deleted_at IS NULL
		JOIN workspace_members wm ON wm.workspace_id=sh.workspace_id AND wm.user_id=sh.issued_by_user_id
		WHERE sh.id=? AND c.mode='CHAT'
		  AND (c.origin IS NULL OR c.origin IN ('UI','CLI'))
		  AND c.created_by IS NOT NULL
		  AND (a.crew_id IS NULL OR cr.id IS NOT NULL)
		  AND (c.created_by=sh.issued_by_user_id OR wm.role IN ('OWNER','ADMIN'))`, id).
		Scan(&g.ID, &g.WorkspaceID, &g.AgentID, &g.ChatID, &g.IssuedByUserID,
			&hash, &created, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrDenied
	}
	if err != nil {
		return Grant{}, fmt.Errorf("validate chat share: %w", err)
	}
	provided := sha256.Sum256([]byte(token))
	if len(hash) != sha256.Size || subtle.ConstantTimeCompare(hash, provided[:]) != 1 || revoked.Valid {
		return Grant{}, ErrDenied
	}
	g.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Grant{}, fmt.Errorf("invalid stored chat share created_at: %w", err)
	}
	g.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return Grant{}, fmt.Errorf("invalid stored chat share expires_at: %w", err)
	}
	if !time.Now().UTC().Before(g.ExpiresAt) {
		return Grant{}, ErrDenied
	}
	return g, nil
}

func (s *Store) mayManage(ctx context.Context, workspaceID, agentID, chatID, actorID string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM chats c
		JOIN workspaces ws ON ws.id=c.workspace_id AND ws.deleted_at IS NULL
		JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
		LEFT JOIN crews cr ON cr.id=a.crew_id AND cr.workspace_id=a.workspace_id AND cr.deleted_at IS NULL
		JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
		WHERE c.id=? AND c.agent_id=? AND c.workspace_id=?
		  AND c.mode='CHAT' AND (c.origin IS NULL OR c.origin IN ('UI','CLI'))
		  AND c.created_by IS NOT NULL
		  AND (a.crew_id IS NULL OR cr.id IS NOT NULL)
		  AND (c.created_by=? OR wm.role IN ('OWNER','ADMIN'))
	)`, actorID, chatID, agentID, workspaceID, actorID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("authorize chat share: %w", err)
	}
	return ok, nil
}

type grantScanner interface{ Scan(dest ...any) error }

func scanGrant(row grantScanner) (Grant, error) {
	var g Grant
	var created, expires string
	var revoked sql.NullString
	if err := row.Scan(&g.ID, &g.WorkspaceID, &g.AgentID, &g.ChatID,
		&g.IssuedByUserID, &created, &expires, &revoked); err != nil {
		return Grant{}, fmt.Errorf("scan chat share: %w", err)
	}
	var err error
	g.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Grant{}, fmt.Errorf("parse chat share created_at: %w", err)
	}
	g.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return Grant{}, fmt.Errorf("parse chat share expires_at: %w", err)
	}
	if revoked.Valid {
		v, err := time.Parse(time.RFC3339Nano, revoked.String)
		if err != nil {
			return Grant{}, fmt.Errorf("parse chat share revoked_at: %w", err)
		}
		g.RevokedAt = &v
	}
	return g, nil
}

func formatTime(v time.Time) string { return v.UTC().Format(time.RFC3339Nano) }

func validToken(token string) bool {
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, TokenPrefix))
	return err == nil && len(b) == 32
}
