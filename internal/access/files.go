package access

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// FileVersion is classified output metadata, never a legacy storage path.
type FileVersion struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"created_at"`
}

func validFileName(name string) bool {
	if name == "" || len(name) > 240 || !utf8.ValidString(name) || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || part == "" {
			return false
		}
	}
	return true
}

// SaveFile is trusted host ingestion of a confined worker's bounded output.
// Admission determines identity and scope; callers cannot supply either.
func (s Store) SaveFile(ctx context.Context, handle, name string, content []byte) (FileVersion, error) {
	if s.DB == nil || !validFileName(name) || len(content) > 1<<20 {
		return FileVersion{}, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return FileVersion{}, err
	}
	defer tx.Rollback()
	a, err := resolve(ctx, tx, digest(handle), true, map[string]bool{})
	if err != nil {
		return FileVersion{}, err
	}
	sum := sha256.Sum256(content)
	version := FileVersion{ID: randomID(), Name: name, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), CreatedAt: tsformat.Format(time.Now())}
	var existing FileVersion
	err = tx.QueryRowContext(ctx, `SELECT id,name,size_bytes,sha256,created_at FROM access_files WHERE attempt_id=? AND name=?`, a.ID, name).
		Scan(&existing.ID, &existing.Name, &existing.Size, &existing.SHA256, &existing.CreatedAt)
	if err == nil {
		if existing.Size != version.Size || existing.SHA256 != version.SHA256 {
			return FileVersion{}, ErrDenied
		}
		version = existing
	} else if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO access_files(id,attempt_id,workspace_id,principal_id,scope,agent_id,name,content,size_bytes,sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			version.ID, a.ID, a.Workspace, a.Principal, a.Scope, a.Agent, name, content, version.Size, version.SHA256, version.CreatedAt)
		if err != nil {
			return FileVersion{}, err
		}
	} else {
		return FileVersion{}, err
	}
	if _, err = resolve(ctx, tx, digest(handle), true, map[string]bool{}); err != nil {
		return FileVersion{}, err
	}
	if err = tx.Commit(); err != nil {
		return FileVersion{}, err
	}
	if _, err = s.Resolve(ctx, handle); err != nil {
		return FileVersion{}, err
	}
	return version, nil
}

func currentFileAudience(ctx context.Context, q contextQuery, user, workspace, agent, chat string) (Attempt, error) {
	a, err := currentContextAudience(ctx, q, user, workspace, agent, chat)
	if err == nil || !errors.Is(err, ErrDenied) {
		return a, err
	}
	// A run-only principal may read its own private run artifacts. That does
	// not confer chat history or a shared-group audience.
	m, err := check(ctx, q, user, workspace, Right{"agent", agent, "run"})
	if err != nil || m.Mode != "restricted" {
		return Attempt{}, ErrDenied
	}
	if err = chatRead(ctx, q, user, workspace, chat); err != nil {
		return Attempt{}, err
	}
	a = Attempt{AdmissionOperation: "run", Principal: user, Workspace: workspace, Agent: agent, Chat: chat, Member: m.ID, Revision: m.Revision}
	if err = q.QueryRowContext(ctx, `SELECT authority_generation,authority_revision FROM chats WHERE id=? AND workspace_id=? AND agent_id=? AND visibility='private'`, chat, workspace, agent).
		Scan(&a.ChatGeneration, &a.ChatRevision); err != nil {
		return Attempt{}, ErrDenied
	}
	rows, err := q.QueryContext(ctx, `SELECT resource_kind,COALESCE(agent_id,project_id),operation FROM access_grants WHERE member_id=?`, m.ID)
	if err != nil {
		return Attempt{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var right Right
		if err = rows.Scan(&right.Kind, &right.ID, &right.Operation); err != nil {
			return Attempt{}, err
		}
		a.Rights = append(a.Rights, right)
	}
	if err = rows.Err(); err != nil {
		return Attempt{}, err
	}
	a.Scope = scope(a)
	return a, nil
}

func readFile(ctx context.Context, q queryer, audience Attempt, id string, withContent bool) (FileVersion, []byte, error) {
	var version FileVersion
	var originID string
	var content []byte
	column := "NULL"
	if withContent {
		column = "content"
	}
	err := q.QueryRowContext(ctx, `SELECT id,name,size_bytes,sha256,created_at,attempt_id,`+column+` FROM access_files WHERE id=? AND scope=? AND agent_id=? AND workspace_id=?`, id, audience.Scope, audience.Agent, audience.Workspace).
		Scan(&version.ID, &version.Name, &version.Size, &version.SHA256, &version.CreatedAt, &originID, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return FileVersion{}, nil, ErrDenied
	}
	if err != nil {
		return FileVersion{}, nil, err
	}
	origin, err := resolveState(ctx, q, originID, false, map[string]bool{}, true)
	if err != nil || origin.Scope != audience.Scope || origin.Agent != audience.Agent || !subset(origin.Rights, audience.Rights) {
		return FileVersion{}, nil, ErrDenied
	}
	return version, content, nil
}

func (s Store) FilesForChat(ctx context.Context, user, workspace, agent, chat string) ([]FileVersion, error) {
	if s.DB == nil {
		return nil, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := currentFileAudience(ctx, tx, user, workspace, agent, chat)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM access_files WHERE scope=? AND agent_id=? AND workspace_id=? ORDER BY rowid DESC LIMIT 256`, a.Scope, agent, workspace)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []FileVersion{}
	for _, id := range ids {
		version, _, err := readFile(ctx, tx, a, id, false)
		if errors.Is(err, ErrDenied) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, version)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for _, version := range out {
		if err = s.CheckFileForChat(ctx, user, workspace, agent, chat, version.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s Store) fileForChat(ctx context.Context, user, workspace, agent, chat, id string, content bool) (FileVersion, []byte, error) {
	if s.DB == nil {
		return FileVersion{}, nil, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FileVersion{}, nil, err
	}
	defer tx.Rollback()
	a, err := currentFileAudience(ctx, tx, user, workspace, agent, chat)
	if err != nil {
		return FileVersion{}, nil, err
	}
	version, data, err := readFile(ctx, tx, a, id, content)
	if err != nil {
		return FileVersion{}, nil, err
	}
	return version, data, tx.Commit()
}

func (s Store) ReadFileForChat(ctx context.Context, user, workspace, agent, chat, id string) (FileVersion, []byte, error) {
	version, data, err := s.fileForChat(ctx, user, workspace, agent, chat, id, true)
	if err != nil {
		return FileVersion{}, nil, err
	}
	if err = s.CheckFileForChat(ctx, user, workspace, agent, chat, id); err != nil {
		return FileVersion{}, nil, err
	}
	return version, data, nil
}

func (s Store) CheckFileForChat(ctx context.Context, user, workspace, agent, chat, id string) error {
	_, _, err := s.fileForChat(ctx, user, workspace, agent, chat, id, false)
	return err
}
