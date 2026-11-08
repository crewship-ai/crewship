package documents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// CommentLimit bounds one document's discussion, threads and replies together.
const CommentLimit = 1000

// CommentBodyLimit is the longest comment text in characters.
const CommentBodyLimit = 10000

// CommentAnchorLimit matches the engine's anchor bound.
const CommentAnchorLimit = 8192

// ResolveLimit bounds how often one thread is resolved or reopened, so flipping
// threads cannot grow receipts without end; one thread reaching it leaves the
// others untouched.
const ResolveLimit = 100

var ErrInvalidComment = errors.New("invalid bounded comment")

// Comment is one stored remark. A root carries the engine-validated anchor;
// a reply names its root in Parent and has no anchor of its own.
type Comment struct {
	ID        string          `json:"id"`
	Parent    string          `json:"parent,omitempty"`
	Body      string          `json:"body"`
	Anchor    json.RawMessage `json:"anchor,omitempty"`
	Actor     string          `json:"actor"`
	Rev       int64           `json:"rev"`
	Version   int64           `json:"version"`
	Resolved  bool            `json:"resolved"`
	CreatedAt string          `json:"created_at"`
}

// CommentInput is one new root or reply. ID is also its idempotency receipt.
type CommentInput struct {
	ID, Fingerprint, Parent, Body string
	Anchor                        json.RawMessage
	IfRev                         int64
	Generation                    *int64
}

// Comments lists the document's threads and replies, oldest first.
func (s *Store) Comments(ctx context.Context, k Key, a Authorize) (out []Comment, err error) {
	out = []Comment{}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, _ string) error {
		if _, e := state(ctx, tx, k); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,coalesce(parent_id,''),body,anchor_json,actor,rev,version,resolved,created_at FROM document_comments WHERE workspace_id=? AND page_id=? ORDER BY created_at,id LIMIT ?`, k.WorkspaceID, k.PageID, CommentLimit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c Comment
			var anchor string
			if e = rows.Scan(&c.ID, &c.Parent, &c.Body, &anchor, &c.Actor, &c.Rev, &c.Version, &c.Resolved, &c.CreatedAt); e != nil {
				return e
			}
			if anchor != "" {
				c.Anchor = json.RawMessage(anchor)
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}

func commentDigest(kind string, value any) (string, error) {
	raw, err := json.Marshal(struct {
		Kind  string
		Value any
	}{kind, value})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// replay returns an earlier receipt for this exact actor and payload.
func replay(ctx context.Context, tx *sql.Tx, k Key, id, digest, actor, kind string) (Receipt, bool, error) {
	previous, stored, e := receipt(ctx, tx, k, id)
	if errors.Is(e, sql.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if e != nil {
		return Receipt{}, false, e
	}
	if stored != digest || previous.Actor != actor || previous.Kind != kind {
		return Receipt{}, false, ErrDuplicate
	}
	return previous, true, nil
}

// Comment stores a root comment or a reply under the SQL writer fence. It
// checks the confirmed revision and generation and needs no editor lease: a
// comment never changes content. The engine validates the anchor beforehand.
func (s *Store) Comment(ctx context.Context, k Key, in CommentInput, a Authorize) (r Receipt, err error) {
	root := in.Parent == ""
	if in.ID == "" || len(in.ID) > 128 || len(in.Fingerprint) > 256 || len(in.Parent) > 128 || in.Generation == nil ||
		!utf8.ValidString(in.Body) || in.Body == "" || utf8.RuneCountInString(in.Body) > CommentBodyLimit ||
		root != (len(in.Anchor) > 0) || len(in.Anchor) > CommentAnchorLimit || (root && !json.Valid(in.Anchor)) {
		return r, ErrInvalidComment
	}
	digest, err := commentDigest("comment", struct {
		Fingerprint, Parent, Body string
		Anchor                    json.RawMessage
		Rev, Generation           int64
	}{in.Fingerprint, in.Parent, in.Body, in.Anchor, in.IfRev, *in.Generation})
	if err != nil {
		return r, err
	}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, actor string) error {
		previous, found, e := replay(ctx, tx, k, in.ID, digest, actor, "comment")
		if e != nil || found {
			r = previous
			return e
		}
		v, e := state(ctx, tx, k)
		if e != nil {
			return e
		}
		if v.HeadRev != in.IfRev || v.Generation != *in.Generation {
			return ErrConflict
		}
		if !root {
			var parentRoot bool
			e = tx.QueryRowContext(ctx, `SELECT parent_id IS NULL FROM document_comments WHERE workspace_id=? AND page_id=? AND id=?`, k.WorkspaceID, k.PageID, in.Parent).Scan(&parentRoot)
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			if e != nil {
				return e
			}
			if !parentRoot {
				return ErrInvalidComment // one level of replies, as every Docs thread shows
			}
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM document_comments WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&n); e != nil {
			return e
		}
		if n >= CommentLimit {
			return ErrQuota
		}
		if e = s.admitDiscussionReceipt(ctx, tx, k); e != nil {
			return e
		}
		if e = admitDisk(ctx, tx, len(in.Body)+len(in.Anchor)+512); e != nil {
			return e
		}
		var parent any
		if !root {
			parent = in.Parent
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO document_comments(workspace_id,page_id,id,parent_id,body,anchor_json,actor,rev) VALUES(?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, in.ID, parent, in.Body, string(in.Anchor), actor, v.HeadRev); e != nil {
			return e
		}
		r = Receipt{in.ID, v.HeadRev, v.Epoch, actor, "comment"}
		_, e = tx.ExecContext(ctx, `INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, in.ID, digest, in.Fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return e
	})
	return
}

// ResolveComment resolves or reopens a root thread at its current version.
func (s *Store) ResolveComment(ctx context.Context, k Key, id, fingerprint, comment string, version int64, resolved bool, ifRev int64, generation *int64, a Authorize) (r Receipt, err error) {
	if id == "" || len(id) > 128 || len(fingerprint) > 256 || comment == "" || len(comment) > 128 || version < 1 || generation == nil {
		return r, ErrInvalidComment
	}
	digest, err := commentDigest("resolve", struct {
		Fingerprint, Comment string
		Version, Rev, Gen    int64
		Resolved             bool
	}{fingerprint, comment, version, ifRev, *generation, resolved})
	if err != nil {
		return r, err
	}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, actor string) error {
		previous, found, e := replay(ctx, tx, k, id, digest, actor, "resolve")
		if e != nil || found {
			r = previous
			return e
		}
		v, e := state(ctx, tx, k)
		if e != nil {
			return e
		}
		if v.HeadRev != ifRev || v.Generation != *generation {
			return ErrConflict
		}
		// version starts at 1 and grows by one per resolve or reopen.
		if version > ResolveLimit {
			return ErrQuota
		}
		if e = s.admitDiscussionReceipt(ctx, tx, k); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, `UPDATE document_comments SET resolved=?,version=version+1 WHERE workspace_id=? AND page_id=? AND id=? AND parent_id IS NULL AND version=?`, resolved, k.WorkspaceID, k.PageID, comment, version)
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			if e != nil {
				return e
			}
			return ErrConflict
		}
		r = Receipt{id, v.HeadRev, v.Epoch, actor, "resolve"}
		_, e = tx.ExecContext(ctx, `INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, id, digest, fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return e
	})
	return
}
