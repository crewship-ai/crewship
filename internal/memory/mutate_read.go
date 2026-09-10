package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ReadCanonical is §8's read: "Read vrací content, revision, content_sha256,
// scope a provenance. Revision je monotónní per scope/key; hash se kontroluje
// vůči skutečnému souboru."
//
// Three things about it are load-bearing:
//
//   - It reads the CANONICAL FILE, never the search index. That is what makes
//     "read-after-write reads the confirmed canonical content even when the
//     index lags" true by construction rather than by timing. The FTS5 index is
//     a search accelerator; it is never an answer to "what does this file say".
//     (The existing memory.read tool already did this — tools.go:413 — so this
//     half of §8 was satisfied before the contract existed. memory.search is
//     the index-served surface, and it is the one that can lag.)
//
//   - It does NOT normalise. §8 is explicit that existing files go through an
//     explicit import with a new revision rather than a silent normalisation on
//     read, so content_sha256 hashes the exact bytes on disk. Canonical reports
//     whether those bytes are already in the contract's canonical form; a
//     caller that wants to diff against them needs Canonical to be true, or has
//     to import first.
//
//   - Revision comes from the anchor, and the anchor's hash is checked against
//     the file. When they disagree the file was edited outside the contract,
//     and Drift says so. §8 forbids resolving that silently, so ReadCanonical
//     returns the real content with Drift set rather than hiding either half.
//
// db may be nil: the content, its hash and Canonical are still returned, with
// Revision 0 and LedgerRecorded false. That is the ledgerless mode the
// in-container callers run in.
type ReadRequest struct {
	WorkspaceID string
	// Path is the resolved canonical absolute path. AuditPath is the scoped
	// workspace-unique key the anchor is stored under.
	Path      string
	AuditPath string
}

// ReadResult is what §8 says a read returns.
type ReadResult struct {
	Content       []byte
	ContentSHA256 string
	Bytes         int
	Exists        bool

	Revision       int64
	LedgerRecorded bool

	Scope string
	Tier  Tier

	// Canonical is false when the on-disk bytes are not in the contract's
	// canonical form — invalid UTF-8, or CRLF that has never been imported.
	// A replace against a non-canonical base needs MutateRequest.Import.
	Canonical bool

	// Drift is true when a revision anchor exists and its recorded hash does
	// not match the file. The content returned is the FILE's, because that is
	// what any reader of the filesystem sees; the revision is the ledger's
	// last confirmed one, and the two do not describe the same bytes.
	Drift          bool
	AnchorSHA256   string
	LastMutationID string

	// Provenance is the §3 metadata for the last confirmed mutation of this
	// key: who wrote it, on which run, and when it was recorded.
	Provenance Provenance
}

// Provenance is the §3 "source, actor/run, scope, recorded_at, nullable
// valid_from/valid_to and correction/retraction refs" block, for the last
// confirmed mutation of a key.
type Provenance struct {
	MutationID  string
	OperationID string
	Source      string
	ActorType   string
	ActorID     string
	RunID       string
	Generation  int64
	Op          string
	RecordedAt  string
	ConfirmedAt string
	ValidFrom   string
	ValidTo     string
	Corrects    string
	Retracts    string
}

// ReadCanonical performs the §8 read. A missing file is not an error: Exists is
// false, Content is empty, and the hash is the hash of no bytes — which is what
// the first write's base hash will be, so a client can CAS against "not there
// yet" the same way it CASes against any other state.
func ReadCanonical(ctx context.Context, db *sql.DB, req ReadRequest) (ReadResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.Path) == "" {
		return ReadResult{}, fmt.Errorf("read: path required")
	}
	if strings.TrimSpace(req.AuditPath) == "" {
		return ReadResult{}, fmt.Errorf("read: audit_path required")
	}

	content, err := readRegularNoFollow(req.Path)
	exists := true
	if errors.Is(err, os.ErrNotExist) {
		content, exists, err = nil, false, nil
	}
	if err != nil {
		return ReadResult{}, fmt.Errorf("read canonical %s: %w", req.AuditPath, err)
	}

	out := ReadResult{
		Content:       content,
		ContentSHA256: sha256Hex(content),
		Bytes:         len(content),
		Exists:        exists,
		Canonical:     isCanonical(content),
	}
	if db == nil {
		return out, nil
	}
	out.LedgerRecorded = true

	var (
		scope, tier, anchorSHA, lastMutation string
		revision                             int64
	)
	err = db.QueryRowContext(ctx, `
		SELECT revision, content_sha256, scope, tier, last_mutation_id
		  FROM memory_revisions
		 WHERE workspace_id = ? AND path = ?`,
		req.WorkspaceID, req.AuditPath).Scan(&revision, &anchorSHA, &scope, &tier, &lastMutation)
	if errors.Is(err, sql.ErrNoRows) {
		// No anchor: nothing has been written through the contract yet. The
		// file may still exist — that is the pre-contract file the first
		// Mutate adopts.
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("load revision anchor: %w", err)
	}
	out.Revision = revision
	out.Scope = scope
	out.Tier = Tier(tier)
	out.AnchorSHA256 = anchorSHA
	out.LastMutationID = lastMutation
	out.Drift = anchorSHA != out.ContentSHA256

	if lastMutation != "" {
		p, perr := loadProvenance(ctx, db, lastMutation)
		if perr != nil {
			return out, perr
		}
		out.Provenance = p
	}
	return out, nil
}

func loadProvenance(ctx context.Context, db *sql.DB, mutationID string) (Provenance, error) {
	var (
		p                               Provenance
		validFrom, validTo              sql.NullString
		corrects, retracts, confirmedAt sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, operation_id, source, actor_type, actor_id, run_id, generation, op,
		       recorded_at, confirmed_at, valid_from, valid_to,
		       corrects_mutation_id, retracts_mutation_id
		  FROM memory_mutations
		 WHERE id = ?`, mutationID).
		Scan(&p.MutationID, &p.OperationID, &p.Source, &p.ActorType, &p.ActorID,
			&p.RunID, &p.Generation, &p.Op, &p.RecordedAt, &confirmedAt,
			&validFrom, &validTo, &corrects, &retracts)
	if errors.Is(err, sql.ErrNoRows) {
		return Provenance{}, nil
	}
	if err != nil {
		return Provenance{}, fmt.Errorf("load mutation provenance: %w", err)
	}
	p.ConfirmedAt = confirmedAt.String
	p.ValidFrom = validFrom.String
	p.ValidTo = validTo.String
	p.Corrects = corrects.String
	p.Retracts = retracts.String
	return p, nil
}

// isCanonical reports whether b is already in the form §8 versions: valid UTF-8
// with no CRLF. It deliberately says nothing about the trailing newline —
// either answer is canonical, and which one it is changes the hash of the final
// line's removal, so inventing one would invalidate a client's declarations.
func isCanonical(b []byte) bool {
	if !utf8Valid(b) {
		return false
	}
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return false
		}
	}
	return true
}
