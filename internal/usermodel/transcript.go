package usermodel

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// isoMillis renders a time in the fixed-width millisecond-ISO form the
// conversation_messages.ts column is written in
// (internal/conversation/store.go: appendMirror) and that SQLite's
// strftime('%Y-%m-%dT%H:%M:%fZ','now') produces for legacy rows.
//
// The comparison in the query below is a pure string comparison, so the
// layout has to match. RFC3339Nano would NOT: it strips trailing zeros,
// so "…:02.5Z" sorts above "…:02.500123456Z" and a lookback boundary
// landing on a round fraction would silently move. Same reasoning as
// internal/api's isoMillisNow.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// LoadTranscript returns the recent conversation for one operator in one
// workspace, oldest turn first, attributed.
//
// Source is conversation_messages (v111) joined to chats. That table is
// the searchable mirror of the JSONL session logs and is the only place
// with per-turn role AND per-turn human authorship (author_user_id, v118)
// in one row — the JSONL is the durable source of truth but has no index
// to walk per operator.
//
// # Attribution
//
// BySubject is set only for a `user` turn this person authored:
//
//   - author_user_id = the subject, or
//   - author_user_id NULL in a PRIVATE chat they opened. NULL means
//     "agent, system, or a row written before v118"; in a private 1:1
//     chat the only human is the opener, so the attribution is sound.
//     In a GROUP chat it is not, and those rows stay unattributed rather
//     than being credited to the opener.
//
// Everything else — agent turns, other humans' turns — is loaded too and
// marked BySubject=false, because the model needs the surrounding
// conversation to read a turn correctly, and because a span found only
// in one of those turns must be refusable with an accurate reason rather
// than invisible.
//
// # Scope
//
// Chats the subject OPENED (chats.created_by), matching how the sweep
// picks candidates in the first place (consolidate.loadUserModelCandidates
// groups on the same column). A group chat opened by someone else that
// the subject spoke in is therefore not read. That is a known, bounded
// gap and it fails in the safe direction: fewer facts, never
// misattributed ones.
func LoadTranscript(
	ctx context.Context,
	db *sql.DB,
	workspaceID, userID string,
	since time.Time,
	maxTurns int,
) ([]Turn, error) {
	if db == nil || workspaceID == "" || userID == "" {
		return nil, nil
	}
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}

	// Newest-first with a LIMIT, then reversed: the bound has to keep the
	// MOST RECENT turns, and ordering ascending with a LIMIT would keep
	// the oldest ones instead — which for a chatty operator means
	// extracting from a fortnight-old conversation forever.
	rows, err := db.QueryContext(ctx, `
		SELECT m.role,
		       m.content,
		       COALESCE(m.author_user_id, ''),
		       COALESCE(c.visibility, 'private')
		FROM conversation_messages m
		JOIN chats c ON c.id = m.session_id
		WHERE c.workspace_id = ?
		  AND c.created_by = ?
		  AND m.ts >= ?
		ORDER BY m.ts DESC
		LIMIT ?
	`, workspaceID, userID, isoMillis(since), maxTurns)
	if err != nil {
		return nil, fmt.Errorf("usermodel: transcript query: %w", err)
	}
	defer rows.Close()

	var rev []Turn
	for rows.Next() {
		var role, content, author, visibility string
		if err := rows.Scan(&role, &content, &author, &visibility); err != nil {
			return nil, fmt.Errorf("usermodel: scan transcript row: %w", err)
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		isUser := strings.EqualFold(role, "user")
		bySubject := isUser && (author == userID ||
			(author == "" && !strings.EqualFold(visibility, "group")))
		rev = append(rev, Turn{Role: role, BySubject: bySubject, Content: content})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usermodel: iterate transcript: %w", err)
	}

	out := make([]Turn, len(rev))
	for i, t := range rev {
		out[len(rev)-1-i] = t
	}
	return out, nil
}

// DefaultMaxTurns bounds one extraction's transcript. 200 turns of chat
// is a large fortnight for one operator and is comfortably inside a
// haiku-class context; the sweep runs daily, so anything above the bound
// was almost certainly already read yesterday.
const DefaultMaxTurns = 200

// HasSubjectTurns reports whether a transcript contains anything the
// subject actually said. Nothing can be extracted from a transcript
// without one, so the caller skips the model call entirely — a stated-
// only extractor asked to read a conversation the person did not speak
// in has exactly one correct answer, and paying a model to produce it is
// waste.
func HasSubjectTurns(turns []Turn) bool {
	for _, t := range turns {
		if t.BySubject && strings.TrimSpace(t.Content) != "" {
			return true
		}
	}
	return false
}
