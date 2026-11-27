package api

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type craftRealtimeSubmission struct {
	AttemptID      string `json:"attempt_id"`
	BaseVersion    string `json:"base_version"`
	EditorRevision string `json:"editor_revision"`
	ContentHash    string `json:"content_hash"`
}

type craftRealtimeMerged struct {
	Kind            string                   `json:"kind"`
	Submitted       craftRealtimeSubmission  `json:"submitted"`
	Receipt         craftWorkbookSaveReceipt `json:"receipt"`
	HeadContentHash string                   `json:"head_content_hash"`
	Bytes           []byte                   `json:"bytes"`
	Overlap         []craftSheetRange        `json:"overlap"`
	AgentRanges     []craftSheetRange        `json:"agent_ranges"`
	HeadRanges      []craftSheetRange        `json:"head_ranges"`
}

type craftRealtimeAttemptMetadata struct {
	Merged      bool              `json:"merged"`
	Signed      bool              `json:"signed"`
	Overlap     []craftSheetRange `json:"overlap"`
	AgentRanges []craftSheetRange `json:"agent_ranges"`
	HeadRanges  []craftSheetRange `json:"head_ranges"`
}

// Both publication branches and reconciliation check the same wire limits.
func (m craftRealtimeAttemptMetadata) validate(candidate []byte) error {
	if len(candidate) == 0 || len(candidate) > 32<<20 {
		return artifacts.ErrVersionQuota
	}
	if m.Merged {
		for _, ranges := range [][]craftSheetRange{m.Overlap, m.AgentRanges, m.HeadRanges} {
			if len(ranges) > craftRealtimeRangeLimit {
				return errCraftRealtimeTooManyRanges
			}
			if ranges == nil {
				return errCraftRealtimeUnproved
			}
			for _, r := range ranges {
				if _, ok := craftWireSheetRange(r); !ok {
					return errCraftRealtimeUnproved
				}
			}
		}
	}
	return nil
}

type craftRealtimeAttempt struct {
	entry     *craftNativeSession
	submitted craftRealtimeSubmission
	base      int64
}

func newCraftRealtimeAttempt(entry *craftNativeSession, body craftNativeSnapshotBody, base int64) (craftRealtimeAttempt, error) {
	s := craftRealtimeSubmission{body.AttemptID, body.BaseVersion, body.EditorRevision, body.ContentHash}
	a := craftRealtimeAttempt{entry: entry, submitted: s, base: base}
	if !mooring.ValidNativeAttemptID(s.AttemptID) || len(s.EditorRevision) == 0 || len(s.EditorRevision) > 256 ||
		strings.ContainsRune(s.EditorRevision, 0) || !utf8.ValidString(s.EditorRevision) ||
		len(s.ContentHash) != len("sha256:")+64 || !strings.HasPrefix(s.ContentHash, "sha256:") ||
		strings.ToLower(s.ContentHash) != s.ContentHash {
		return a, mooring.ErrInvalid
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(s.ContentHash, "sha256:")); err != nil {
		return a, mooring.ErrInvalid
	}
	return a, nil
}

func (a craftRealtimeAttempt) record(base int64, hash string, meta craftRealtimeAttemptMetadata, record mooring.NativeRecord) mooring.NativeRecord {
	return func(ctx context.Context, tx *sql.Tx, rc artifacts.VersionReceipt, native []byte) error {
		raw, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		if err = a.insert(ctx, tx, base, rc.Version, hash, string(raw)); err != nil {
			return err
		}
		if record != nil {
			return record(ctx, tx, rc, native)
		}
		return nil
	}
}

func (a craftRealtimeAttempt) insert(ctx context.Context, tx *sql.Tx, actual, version, hash, meta any) error {
	e, s := a.entry, a.submitted
	result, err := tx.ExecContext(ctx, `INSERT INTO craft_realtime_attempts(page_id,workspace_id,session_id,attempt_id,boot_id,deadline_ns,submitted_base,editor_revision,submitted_hash,actual_base,version,content_hash,metadata)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(page_id,session_id,attempt_id) DO NOTHING`,
		e.scope.ArtifactID, e.scope.WorkspaceID, e.id, s.AttemptID, e.boot, int64(e.realtimeDeadline), a.base, s.EditorRevision, s.ContentHash, actual, version, hash, meta)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return artifacts.ErrVersionConflict
	}
	return err
}

func (a craftRealtimeAttempt) prune(ctx context.Context, tx *sql.Tx, clock mooring.OwnerClock) error {
	boot, now, err := clock.Now()
	if err != nil || boot != a.entry.boot || now >= a.entry.realtimeDeadline {
		return errCraftNativeAccess
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM craft_realtime_attempts WHERE boot_id<>? OR deadline_ns<=?`, boot, int64(now))
	return err
}

// lookup and retirement share the publication fence. A retirement denies
// that attempt even if its original candidate is still being computed.
func (a craftRealtimeAttempt) lookup(ctx context.Context, s *mooring.NativeStore, l mooring.EditLease, clock mooring.OwnerClock, retire bool) (any, error) {
	var meta craftRealtimeAttemptMetadata
	var receipt mooring.NativeSaveReceipt
	var absent bool
	e := a.entry
	err := s.WithConcurrentWorkbookAuthority(ctx, l, func(tx *sql.Tx) error {
		var submitted int64
		var revision, submittedHash string
		var base, version sql.NullInt64
		var hash, raw sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT submitted_base,editor_revision,submitted_hash,actual_base,version,content_hash,metadata
			FROM craft_realtime_attempts WHERE workspace_id=? AND page_id=? AND session_id=? AND attempt_id=?`,
			e.scope.WorkspaceID, e.scope.ArtifactID, e.id, a.submitted.AttemptID).Scan(&submitted, &revision, &submittedHash, &base, &version, &hash, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			if !retire {
				return nil
			}
			if err = a.prune(ctx, tx, clock); err != nil {
				return err
			}
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM craft_realtime_attempts WHERE workspace_id=? AND page_id=? AND session_id=? AND version IS NULL`, e.scope.WorkspaceID, e.scope.ArtifactID, e.id).Scan(&count); err != nil {
				return err
			}
			if count >= 8 {
				return mooring.ErrNativeRetirementLimit
			}
			absent = true
			return a.insert(ctx, tx, nil, nil, nil, nil)
		}
		if err != nil {
			return err
		}
		if submitted != a.base || revision != a.submitted.EditorRevision || submittedHash != a.submitted.ContentHash {
			return artifacts.ErrVersionConflict
		}
		if !version.Valid {
			absent = true
			return nil
		}
		if !base.Valid || !hash.Valid || !raw.Valid || json.Unmarshal([]byte(raw.String), &meta) != nil {
			return mooring.ErrNativeUnconfirmed
		}
		if base.Int64 < a.base || version.Int64 < base.Int64 || version.Int64-base.Int64 > 1 ||
			meta.Merged != (base.Int64 > a.base) || !meta.Merged && hash.String != submittedHash {
			return mooring.ErrNativeUnconfirmed
		}
		receipt = s.ConcurrentWorkbookReceipt(l, base.Int64, version.Int64, revision, hash.String)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if absent {
		if retire {
			return nil, mooring.ErrNativeAttemptAbsent
		}
		return nil, mooring.ErrNativeAttemptRetired
	}
	if receipt.Version == "" {
		return nil, nil
	}
	v, _ := strconv.ParseInt(receipt.Version, 10, 64)
	manifest, _, err := s.ReadVersion(ctx, e.scope, v)
	if err != nil {
		return nil, err
	}
	file, err := s.DocumentFile(manifest)
	if err != nil {
		return nil, err
	}
	if craftContentHash(file.Content) != receipt.ContentHash {
		return nil, mooring.ErrNativeUnconfirmed
	}
	if err = meta.validate(file.Content); err != nil {
		return nil, err
	}
	if err = s.WithConcurrentWorkbookAuthority(ctx, l, func(*sql.Tx) error { return nil }); err != nil {
		return nil, err
	}
	return a.response(receipt, file.Content, meta), nil
}

func (a craftRealtimeAttempt) response(receipt mooring.NativeSaveReceipt, bytes []byte, meta craftRealtimeAttemptMetadata) any {
	result := craftWorkbookSaveReceipt{NativeSaveReceipt: receipt, SignatureInvalidated: meta.Signed}
	if !meta.Merged {
		return result
	}
	return craftRealtimeMerged{
		Kind: "merged", Submitted: a.submitted, Receipt: result, HeadContentHash: receipt.ContentHash,
		Bytes: bytes, Overlap: meta.Overlap, AgentRanges: meta.AgentRanges, HeadRanges: meta.HeadRanges,
	}
}
