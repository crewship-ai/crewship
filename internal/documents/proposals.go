package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	"unicode/utf8"
)

// ProposalLimit bounds one document's stored proposals, open and decided.
const ProposalLimit = 500

// ProposalOpenLimit bounds the proposals awaiting review on one document.
const ProposalOpenLimit = 100

// ProposalSchema is the only content schema proposals exist on (#5137).
const ProposalSchema = 4

var ErrInvalidProposal = errors.New("invalid bounded proposal")

var proposalHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var proposalBlock = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// Proposal is one agent's reviewed-but-unapplied replacement of one block's
// text. Base and Proposed are the block subtree before and after, as the
// engine projected them and Go re-proved them.
type Proposal struct {
	ID           string          `json:"id"`
	RunID        string          `json:"run_id"`
	Actor        string          `json:"actor"`
	Target       string          `json:"target"`
	Schema       int             `json:"schema"`
	Generation   int64           `json:"generation"`
	BaseRev      int64           `json:"base_rev"`
	BaseHash     string          `json:"base_hash"`
	ProposedHash string          `json:"proposed_hash"`
	Text         string          `json:"text"`
	Status       string          `json:"status"`
	DecidedBy    string          `json:"decided_by,omitempty"`
	DecidedAt    string          `json:"decided_at,omitempty"`
	AcceptedRev  *int64          `json:"accepted_rev,omitempty"`
	CreatedAt    string          `json:"created_at"`
	Base         json.RawMessage `json:"base_content,omitempty"`
	Proposed     json.RawMessage `json:"proposed_content,omitempty"`
}

// ProposalInput is one new proposal. ID is also its idempotency receipt.
// Guard re-proves the semantic replacement and the run's mandate inside the
// writer transaction; it is required.
type ProposalInput struct {
	ID, Fingerprint, RunID, Target, BaseHash, ProposedHash, Text string
	// Ancestors are the target's enclosing block IDs, nearest first: their
	// subtree hashes change with the target and nothing else may.
	Ancestors      []string
	Base, Proposed json.RawMessage
	IfRev          int64
	Generation     *int64
	Guard          MutationGuard
}

func (in ProposalInput) valid() bool {
	return in.ID != "" && len(in.ID) <= 128 && len(in.Fingerprint) <= 256 && in.RunID != "" && len(in.RunID) <= 256 &&
		proposalBlock.MatchString(in.Target) && proposalHash.MatchString(in.BaseHash) && proposalHash.MatchString(in.ProposedHash) &&
		utf8.ValidString(in.Text) && len(in.Text) <= 100000 && in.Generation != nil && in.IfRev >= 0 && in.Guard != nil &&
		len(in.Ancestors) <= 40 && validProposalBlocks(in.Ancestors) &&
		len(in.Base) >= 2 && len(in.Base) <= 500000 && json.Valid(in.Base) && len(in.Proposed) >= 2 && len(in.Proposed) <= 500000 && json.Valid(in.Proposed)
}

func validProposalBlocks(ids []string) bool {
	seen := map[string]bool{}
	for _, id := range ids {
		if !proposalBlock.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// Propose stores a proposal under the SQL writer fence. Like a comment it
// needs no editor lease and never changes content, revision or generation.
func (s *Store) Propose(ctx context.Context, k Key, in ProposalInput, a Authorize) (r Receipt, err error) {
	if !in.valid() {
		return r, ErrInvalidProposal
	}
	digest, err := commentDigest("proposal", struct {
		Fingerprint, Target, BaseHash, ProposedHash, Text string
		Ancestors                                         []string
		Base, Proposed                                    json.RawMessage
		Rev, Generation                                   int64
	}{in.Fingerprint, in.Target, in.BaseHash, in.ProposedHash, in.Text, in.Ancestors, in.Base, in.Proposed, in.IfRev, *in.Generation})
	if err != nil {
		return r, err
	}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, actor string) error {
		previous, found, e := replay(ctx, tx, k, in.ID, digest, actor, "proposal")
		if e != nil || found {
			r = previous
			return e
		}
		v, e := state(ctx, tx, k)
		if e != nil {
			return e
		}
		if v.Schema != ProposalSchema {
			return ErrSchema
		}
		if v.HeadRev != in.IfRev || v.Generation != *in.Generation {
			return ErrConflict
		}
		if e = in.Guard(ctx, tx, actor, v); e != nil {
			return e
		}
		// Only proposals that can still be accepted count as awaiting review.
		// When the document holds ProposalLimit rows, the oldest one that can
		// no longer be accepted (decided, or from an earlier generation) makes
		// room; a document full of live proposals refuses with ErrQuota.
		var total, open int
		if e = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(status='open' AND generation=?),0) FROM document_proposals WHERE workspace_id=? AND page_id=?`, v.Generation, k.WorkspaceID, k.PageID).Scan(&total, &open); e != nil {
			return e
		}
		if open >= ProposalOpenLimit {
			return ErrQuota
		}
		if total >= ProposalLimit {
			result, e := tx.ExecContext(ctx, `DELETE FROM document_proposals WHERE rowid IN (SELECT rowid FROM document_proposals WHERE workspace_id=? AND page_id=? AND (status!='open' OR generation!=?) ORDER BY created_at,rowid LIMIT ?)`, k.WorkspaceID, k.PageID, v.Generation, total-ProposalLimit+1)
			if e != nil {
				return e
			}
			if n, e := result.RowsAffected(); e != nil || n != int64(total-ProposalLimit+1) {
				if e != nil {
					return e
				}
				return ErrQuota
			}
		}
		if e = s.admitDiscussionReceipt(ctx, tx, k); e != nil {
			return e
		}
		if e = admitDisk(ctx, tx, len(in.Text)+len(in.Base)+len(in.Proposed)+512); e != nil {
			return e
		}
		ancestors, e := json.Marshal(append([]string{}, in.Ancestors...))
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO document_proposals(workspace_id,page_id,id,run_id,actor,target,ancestors,schema_version,generation,base_rev,base_hash,proposed_hash,text,base_json,proposed_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			k.WorkspaceID, k.PageID, in.ID, in.RunID, actor, in.Target, string(ancestors), v.Schema, v.Generation, v.HeadRev, in.BaseHash, in.ProposedHash, in.Text, string(in.Base), string(in.Proposed)); e != nil {
			return e
		}
		r = Receipt{in.ID, v.HeadRev, v.Epoch, actor, "proposal"}
		_, e = tx.ExecContext(ctx, `INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, in.ID, digest, in.Fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return e
	})
	return
}

const proposalColumns = `id,run_id,actor,target,schema_version,generation,base_rev,base_hash,proposed_hash,text,status,coalesce(decided_by,''),coalesce(decided_at,''),accepted_rev,created_at`

func scanProposal(row interface{ Scan(...any) error }, p *Proposal, extra ...any) error {
	var accepted sql.NullInt64
	if e := row.Scan(append([]any{&p.ID, &p.RunID, &p.Actor, &p.Target, &p.Schema, &p.Generation, &p.BaseRev, &p.BaseHash, &p.ProposedHash, &p.Text, &p.Status, &p.DecidedBy, &p.DecidedAt, &accepted, &p.CreatedAt}, extra...)...); e != nil {
		return e
	}
	if accepted.Valid {
		p.AcceptedRev = &accepted.Int64
	}
	return nil
}

// Proposals lists the document's proposals, oldest first, without content.
func (s *Store) Proposals(ctx context.Context, k Key, a Authorize) (out []Proposal, err error) {
	out = []Proposal{}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, _ string) error {
		if _, e := state(ctx, tx, k); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+proposalColumns+` FROM document_proposals WHERE workspace_id=? AND page_id=? ORDER BY created_at,rowid LIMIT ?`, k.WorkspaceID, k.PageID, ProposalLimit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var p Proposal
			if e = scanProposal(rows, &p); e != nil {
				return e
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return
}

// ProposalRead returns one proposal with its base and proposed block content.
func (s *Store) ProposalRead(ctx context.Context, k Key, id string, a Authorize) (p Proposal, err error) {
	if id == "" || len(id) > 128 {
		return p, ErrInvalidProposal
	}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, _ string) error {
		if _, e := state(ctx, tx, k); e != nil {
			return e
		}
		var base, proposed string
		e := scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalColumns+`,base_json,proposed_json FROM document_proposals WHERE workspace_id=? AND page_id=? AND id=?`, k.WorkspaceID, k.PageID, id), &p, &base, &proposed)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		p.Base, p.Proposed = json.RawMessage(base), json.RawMessage(proposed)
		return e
	})
	return
}

// RejectProposal closes an open proposal without touching content.
func (s *Store) RejectProposal(ctx context.Context, k Key, id, fingerprint, proposal string, ifRev int64, generation *int64, a Authorize) (r Receipt, err error) {
	if id == "" || len(id) > 128 || len(fingerprint) > 256 || proposal == "" || len(proposal) > 128 || generation == nil {
		return r, ErrInvalidProposal
	}
	digest, err := commentDigest("proposal-reject", struct {
		Fingerprint, Proposal string
		Rev, Generation       int64
	}{fingerprint, proposal, ifRev, *generation})
	if err != nil {
		return r, err
	}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, actor string) error {
		previous, found, e := replay(ctx, tx, k, id, digest, actor, "proposal-reject")
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
		if e = s.admitDiscussionReceipt(ctx, tx, k); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, `UPDATE document_proposals SET status='rejected',decided_by=?,decision_id=?,decided_at=? WHERE workspace_id=? AND page_id=? AND id=? AND status='open'`, actor, id, time.Now().UTC().Format(time.RFC3339Nano), k.WorkspaceID, k.PageID, proposal)
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			if e != nil {
				return e
			}
			return ErrConflict
		}
		r = Receipt{id, v.HeadRev, v.Epoch, actor, "proposal-reject"}
		_, e = tx.ExecContext(ctx, `INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, id, digest, fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return e
	})
	return
}

// AcceptProposalGuard binds a human commit to exactly one open proposal. It
// runs inside Commit's transaction after receipt replay: the proposal must be
// open on this generation, its target must still hash to the reviewed base,
// and the candidate projection must differ from the head only in that target,
// which must hash to the proposed block. The proposal is marked accepted in
// the same transaction, so a failed commit leaves it open.
func AcceptProposalGuard(k Key, proposal, decision string, candidate json.RawMessage) MutationGuard {
	return func(ctx context.Context, tx *sql.Tx, actor string, v State) error {
		if proposal == "" || len(proposal) > 128 || decision == "" || len(decision) > 128 {
			return ErrInvalidProposal
		}
		var target, base, proposed, status, path string
		var generation int64
		e := tx.QueryRowContext(ctx, `SELECT target,ancestors,base_hash,proposed_hash,status,generation FROM document_proposals WHERE workspace_id=? AND page_id=? AND id=?`, k.WorkspaceID, k.PageID, proposal).Scan(&target, &path, &base, &proposed, &status, &generation)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if status != "open" || generation != v.Generation || v.Schema != ProposalSchema {
			return ErrConflict
		}
		var ancestors []string
		if json.Unmarshal([]byte(path), &ancestors) != nil {
			return ErrConflict
		}
		enclosing := map[string]bool{}
		for _, id := range ancestors {
			enclosing[id] = true
		}
		var before, after []struct{ ID, Hash string }
		if json.Unmarshal(v.Projection, &before) != nil || json.Unmarshal(candidate, &after) != nil || len(before) != len(after) {
			return ErrConflict
		}
		seen := false
		for i := range before {
			if before[i].ID != after[i].ID {
				return ErrConflict
			}
			if before[i].ID == target {
				if before[i].Hash != base || after[i].Hash != proposed {
					return ErrConflict
				}
				seen = true
			} else if before[i].Hash != after[i].Hash && !enclosing[before[i].ID] {
				return ErrConflict
			}
		}
		if !seen {
			return ErrConflict
		}
		result, e := tx.ExecContext(ctx, `UPDATE document_proposals SET status='accepted',decided_by=?,decision_id=?,decided_at=?,accepted_rev=? WHERE workspace_id=? AND page_id=? AND id=? AND status='open'`, actor, decision, time.Now().UTC().Format(time.RFC3339Nano), v.HeadRev+1, k.WorkspaceID, k.PageID, proposal)
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			if e != nil {
				return e
			}
			return ErrConflict
		}
		return nil
	}
}
