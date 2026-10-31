package mooring

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

var ErrNativeProposalNotFound = errors.New("native Craft proposal not found")
var ErrNativeProposalDecided = errors.New("native Craft proposal already decided")

// MaxOpenNativeProposals bounds undecided candidates per document. Each one
// may hold a full native document, so a busy human is never flooded.
const MaxOpenNativeProposals = 16

// SQLite can obtain a BLOB's length without loading its bytes. The TEXT
// fallback preserves exact UTF-8 byte accounting for old restored candidates.
const nativeProposalBytesQuery = `SELECT coalesce(sum(CASE WHEN typeof(content)='blob' THEN length(content) ELSE length(CAST(content AS BLOB)) END+length(CAST(summary AS BLOB))+length(CAST(author_actor_id AS BLOB))+length(CAST(request_id AS BLOB))+length(id)+length(engine_pin)+length(content_hash)+length(created_at)+512),0) FROM craft_native_proposals WHERE workspace_id=? AND page_id=?`

func nativeProposalBytes(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, k EditLeaseScope) (used int64, err error) {
	err = q.QueryRowContext(ctx, nativeProposalBytesQuery, k.WorkspaceID, k.ArtifactID).Scan(&used)
	return
}

// nativeProposalSuperseded records that no person decided a stale proposal:
// the document moved past its base before anyone accepted it.
const nativeProposalSuperseded = "system:superseded"

// NativeProposal is an agent's complete candidate document over one exact
// version. It is not a version: nothing changes until a human accepts it.
type NativeProposal struct {
	ID, PageID, AuthorActorID, RequestID, Summary, ContentHash, State string
	BaseVersion, ResultVersion                                        int64
	CreatedAt                                                         string
	Content                                                           []byte
	// Replayed is set by Propose when the request ID returned an earlier
	// proposal instead of recording one. It is never stored.
	Replayed bool
}

const nativeProposalColumns = `id,page_id,author_actor_id,request_id,summary,content_hash,state,base_version,COALESCE(result_version,0),created_at`

func scanNativeProposal(row interface{ Scan(...any) error }) (NativeProposal, error) {
	var p NativeProposal
	err := row.Scan(&p.ID, &p.PageID, &p.AuthorActorID, &p.RequestID, &p.Summary, &p.ContentHash, &p.State, &p.BaseVersion, &p.ResultVersion, &p.CreatedAt)
	return p, err
}

func nativeProposalID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ncp_" + hex.EncodeToString(b), nil
}

// Propose records an agent candidate over base. The caller's write authority,
// the immutable profile and base == latest are checked in one transaction; the
// lease is deliberately not touched, so a human holder keeps editing. The same
// request ID returns the original proposal and never records a second one.
func (s *NativeStore) Propose(ctx context.Context, k EditLeaseScope, base int64, requestID, summary string, raw []byte) (NativeProposal, error) {
	if err := s.ready(); err != nil {
		return NativeProposal{}, err
	}
	if base < 1 || len(requestID) == 0 || len(requestID) > 128 || len(summary) > 500 || !utf8.ValidString(summary) || strings.ContainsRune(summary, 0) {
		return NativeProposal{}, ErrInvalid
	}
	b, err := s.candidate(ctx, raw)
	if err != nil {
		return NativeProposal{}, err
	}
	sum := sha256.Sum256(b)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	id, err := nativeProposalID()
	if err != nil {
		return NativeProposal{}, err
	}
	var out NativeProposal
	var runSession string
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		principal, err := s.config.Authorize(ctx, tx, k, "write")
		if err != nil {
			return "", err
		}
		if principal.SessionID == "" || runSession != "" && runSession != principal.SessionID {
			return "", ErrEditLeaseDenied
		}
		runSession = principal.SessionID
		if err := s.profile(ctx, tx, k); err != nil {
			return "", err
		}
		return principal.ActorID, nil
	}
	recorded := false
	// The store checks authority before and after the body; record once and
	// let the second pass only re-check rights.
	_, err = s.versions.Quota(ctx, nativeVersionKey(k), func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := authorize(ctx, tx)
		if err != nil || recorded {
			return actor, err
		}
		if !strings.HasPrefix(actor, "agent:") {
			return "", ErrEditLeaseDenied
		}
		recorded = true
		// The session comes from current host authorization, never a caller's
		// request fields. Keep the display author stable and namespace only the
		// internal idempotency key; no old rows or migrations are rewritten.
		keyHash := sha256.Sum256([]byte(runSession + "\x00" + requestID))
		requestKey := "run:" + hex.EncodeToString(keyHash[:])
		prior, err := scanNativeProposal(tx.QueryRowContext(ctx, `SELECT `+nativeProposalColumns+` FROM craft_native_proposals WHERE page_id=? AND author_actor_id=? AND request_id=?`, k.ArtifactID, actor, requestKey))
		if err == nil {
			if prior.ContentHash != hash || prior.BaseVersion != base || prior.Summary != summary {
				return "", artifacts.ErrVersionConflict
			}
			out = prior
			out.Replayed = true
			return actor, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		var latest int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM artifact_versions WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.ArtifactID).Scan(&latest); err != nil {
			return "", err
		}
		if latest != base {
			return "", artifacts.ErrVersionConflict
		}
		// Open proposals over older content can never be accepted; record them
		// as stale so they stop holding places under the cap. A base whose
		// manifest equals the head's still applies (see proposalBase), so it
		// stays open. Rows are kept for review and history.
		if _, err = tx.ExecContext(ctx, `UPDATE craft_native_proposals SET state='stale',decided_by=?,decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE page_id=? AND workspace_id=? AND state='open' AND base_version<? AND (SELECT b.manifest_json FROM artifact_versions b WHERE b.workspace_id=craft_native_proposals.workspace_id AND b.page_id=craft_native_proposals.page_id AND b.version=craft_native_proposals.base_version) IS NOT (SELECT h.manifest_json FROM artifact_versions h WHERE h.workspace_id=? AND h.page_id=? AND h.version=?)`, nativeProposalSuperseded, k.ArtifactID, k.WorkspaceID, latest, k.WorkspaceID, k.ArtifactID, latest); err != nil {
			return "", err
		}
		var open int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM craft_native_proposals WHERE page_id=? AND state='open'`, k.ArtifactID).Scan(&open); err != nil {
			return "", err
		}
		if open >= MaxOpenNativeProposals {
			return "", artifacts.ErrVersionQuota
		}
		if err = artifacts.ConsumeStorageUpload(ctx, tx, nativeVersionKey(k)); err != nil {
			return "", err
		}
		if err = artifacts.CheckStorageHeadroom(ctx, tx, int64(len(b))); err != nil {
			return "", err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, k.ArtifactID, k.WorkspaceID, s.config.Application, s.EnginePin(), actor, requestKey, base, summary, hash, b); err != nil {
			return "", err
		}
		used, err := nativeProposalBytes(ctx, tx, k)
		if err != nil {
			return "", err
		}
		if used > s.config.MaxProposalBytes {
			return "", artifacts.ErrVersionQuota
		}
		out, err = scanNativeProposal(tx.QueryRowContext(ctx, `SELECT `+nativeProposalColumns+` FROM craft_native_proposals WHERE id=?`, id))
		return actor, err
	})
	if err != nil {
		return NativeProposal{}, err
	}
	return out, nil
}

// Proposals lists at most 50 proposals under current read authority. Open
// proposals come first so decided history cannot hide work awaiting a human;
// each group is newest first. Candidate bytes are only in ProposalContent.
func (s *NativeStore) Proposals(ctx context.Context, k EditLeaseScope) ([]NativeProposal, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var out []NativeProposal
	authorize := s.authority(k, nil, "read")
	listed := false
	_, err := s.versions.Quota(ctx, nativeVersionKey(k), func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := authorize(ctx, tx)
		if err != nil || listed {
			return actor, err
		}
		listed = true
		rows, err := tx.QueryContext(ctx, `SELECT `+nativeProposalColumns+` FROM craft_native_proposals WHERE page_id=? AND workspace_id=? AND engine_pin=? ORDER BY (state='open') DESC, created_at DESC, id DESC LIMIT 50`, k.ArtifactID, k.WorkspaceID, s.EnginePin())
		if err != nil {
			return "", err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanNativeProposal(rows)
			if err != nil {
				return "", err
			}
			out = append(out, p)
		}
		return actor, rows.Err()
	})
	return out, err
}

// ProposalContent returns one candidate's native bytes for comparison under
// current read authority.
func (s *NativeStore) ProposalContent(ctx context.Context, k EditLeaseScope, id string) (NativeProposal, error) {
	if err := s.ready(); err != nil {
		return NativeProposal{}, err
	}
	var out NativeProposal
	authorize := s.authority(k, nil, "read")
	_, err := s.versions.Quota(ctx, nativeVersionKey(k), func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := authorize(ctx, tx)
		if err != nil {
			return "", err
		}
		out, err = s.loadProposal(ctx, tx, k, id)
		if err == nil {
			err = tx.QueryRowContext(ctx, `SELECT content FROM craft_native_proposals WHERE id=?`, id).Scan(&out.Content)
		}
		return actor, err
	})
	return out, err
}

func (s *NativeStore) loadProposal(ctx context.Context, tx *sql.Tx, k EditLeaseScope, id string) (NativeProposal, error) {
	p, err := scanNativeProposal(tx.QueryRowContext(ctx, `SELECT `+nativeProposalColumns+` FROM craft_native_proposals WHERE id=? AND page_id=? AND workspace_id=? AND engine_pin=?`, id, k.ArtifactID, k.WorkspaceID, s.EnginePin()))
	if errors.Is(err, sql.ErrNoRows) {
		return NativeProposal{}, ErrNativeProposalNotFound
	}
	return p, err
}

func nativeProposalRevision(id string) string { return "native-proposal:" + id }

// AcceptProposal saves an open proposal as the human lease holder's own fenced
// write. Page rights, the immutable profile, the lease generation, the recorded
// base version and the decision are all checked and committed in one
// transaction. If the document content moved on, the proposal becomes stale and the
// document is untouched.
func (s *NativeStore) AcceptProposal(ctx context.Context, l EditLease, id string) (NativeProposal, NativeSaveReceipt, error) {
	if err := s.ready(); err != nil {
		return NativeProposal{}, NativeSaveReceipt{}, err
	}
	if !strings.HasPrefix(l.Holder.ActorID, "user:") {
		return NativeProposal{}, NativeSaveReceipt{}, ErrEditLeaseDenied
	}
	write := s.authority(l.Scope, &l, "write")
	var p NativeProposal
	var content []byte
	if _, err := s.versions.Quota(ctx, nativeVersionKey(l.Scope), func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := write(ctx, tx)
		if err != nil || content != nil {
			return actor, err
		}
		if p, err = s.loadProposal(ctx, tx, l.Scope, id); err != nil {
			return "", err
		}
		if p.State != "open" {
			return "", ErrNativeProposalDecided
		}
		return actor, tx.QueryRowContext(ctx, `SELECT content FROM craft_native_proposals WHERE id=?`, id).Scan(&content)
	}); err != nil {
		return NativeProposal{}, NativeSaveReceipt{}, err
	}
	// The pinned engine validates again outside the SQL writer.
	b, err := s.candidate(ctx, content)
	if err != nil {
		return NativeProposal{}, NativeSaveReceipt{}, err
	}
	sum := sha256.Sum256(b)
	if "sha256:"+hex.EncodeToString(sum[:]) != p.ContentHash {
		return NativeProposal{}, NativeSaveReceipt{}, ErrNativeValidation
	}
	base, err := s.proposalBase(ctx, l, p.BaseVersion, write)
	if err != nil {
		return NativeProposal{}, NativeSaveReceipt{}, err
	}
	revision := nativeProposalRevision(id)
	message, err := s.snapshotMessage(l, base, revision, p.ContentHash)
	if err != nil {
		return NativeProposal{}, NativeSaveReceipt{}, err
	}
	request := nativeManifest(base, b)
	request.Message = message
	decided := false
	receipt, err := s.versions.Apply(ctx, nativeVersionKey(l.Scope), request, func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := write(ctx, tx)
		if err != nil || decided {
			return actor, err
		}
		decided = true
		res, err := tx.ExecContext(ctx, `UPDATE craft_native_proposals SET state='accepted',decided_by=?,decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),result_version=? WHERE id=? AND page_id=? AND state='open'`, actor, base+1, id, l.Scope.ArtifactID)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", ErrNativeProposalDecided
		}
		return actor, nil
	})
	if errors.Is(err, artifacts.ErrVersionConflict) {
		// The human (or anyone) saved after the agent's base. Record that the
		// proposal no longer applies; never rebase it over changed content.
		if staleErr := s.decideProposal(ctx, l.Scope, &l, id, "stale"); staleErr != nil && !errors.Is(staleErr, ErrNativeProposalDecided) {
			return NativeProposal{}, NativeSaveReceipt{}, staleErr
		}
		p.State = "stale"
		return p, NativeSaveReceipt{}, artifacts.ErrVersionConflict
	}
	if err != nil {
		return NativeProposal{}, NativeSaveReceipt{}, err
	}
	p.State, p.ResultVersion = "accepted", receipt.Version
	return p, s.snapshotReceipt(l, base, revision, p.ContentHash, receipt.Version), nil
}

// proposalBase returns the version an accepted proposal applies on. The editor
// saves its holder's state before accepting; when every later save only
// repeated the base bytes, the document did not change and the proposal still
// applies on the current head. Any content change keeps the recorded base, so
// the commit conflicts and the proposal goes stale.
func (s *NativeStore) proposalBase(ctx context.Context, l EditLease, base int64, write artifacts.VersionAuthorize) (int64, error) {
	key := nativeVersionKey(l.Scope)
	head, err := s.versions.Read(ctx, key, write)
	if err != nil || head.LatestVersion <= base {
		return base, err
	}
	old, err := s.versions.ReadVersion(ctx, key, base, write)
	if err != nil {
		return base, err
	}
	if old.EntryPoint != head.Manifest.EntryPoint || len(old.Files) != len(head.Manifest.Files) {
		return base, nil
	}
	for name, f := range old.Files {
		g, ok := head.Manifest.Files[name]
		if !ok || f.MediaType != g.MediaType || !bytes.Equal(f.Content, g.Content) {
			return base, nil
		}
	}
	return head.LatestVersion, nil
}

// RejectProposal closes an open proposal under current write authority. It
// needs no lease: declining a suggestion never changes the document.
func (s *NativeStore) RejectProposal(ctx context.Context, k EditLeaseScope, id string) (NativeProposal, error) {
	if err := s.ready(); err != nil {
		return NativeProposal{}, err
	}
	if err := s.decideProposal(ctx, k, nil, id, "rejected"); err != nil {
		return NativeProposal{}, err
	}
	var out NativeProposal
	_, err := s.versions.Quota(ctx, nativeVersionKey(k), func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := s.authority(k, nil, "read")(ctx, tx)
		if err == nil {
			out, err = s.loadProposal(ctx, tx, k, id)
		}
		return actor, err
	})
	return out, err
}

func (s *NativeStore) decideProposal(ctx context.Context, k EditLeaseScope, l *EditLease, id, state string) error {
	write := s.authority(k, l, "write")
	decided := false
	_, err := s.versions.Quota(ctx, nativeVersionKey(k), func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := write(ctx, tx)
		if err != nil || decided {
			return actor, err
		}
		if !strings.HasPrefix(actor, "user:") {
			return "", ErrEditLeaseDenied
		}
		decided = true
		if _, err = s.loadProposal(ctx, tx, k, id); err != nil {
			return "", err
		}
		res, err := tx.ExecContext(ctx, `UPDATE craft_native_proposals SET state=?,decided_by=?,decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND page_id=? AND state='open'`, state, actor, id, k.ArtifactID)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", ErrNativeProposalDecided
		}
		return actor, nil
	})
	return err
}
