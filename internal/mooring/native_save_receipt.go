package mooring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

var ErrNativeUnconfirmed = errors.New("native snapshot has no matching committed receipt")

// ErrNativeAttemptAbsent: reconcile proved that the identified save attempt
// is not committed and retired it, so it can never commit later (#4899).
var ErrNativeAttemptAbsent = errors.New("native save attempt did not commit and never will")

// ErrNativeAttemptRetired refuses a save whose attempt an earlier reconcile retired.
var ErrNativeAttemptRetired = errors.New("native save attempt was retired by reconcile")

// ErrNativeRetirementLimit: the page already holds the maximum number of
// retired attempts for its live lease. Nothing was recorded, so the attempt
// stays unconfirmed (never "absent").
var ErrNativeRetirementLimit = errors.New("native save attempt retirement limit reached; outcome stays unconfirmed")

// nativeRetiredAttemptsPerPage bounds retirement rows for a page's one live
// lease generation. An editor has at most one unconfirmed save at a time.
const nativeRetiredAttemptsPerPage = 8

// validNativeAttemptID accepts an opaque client nonce (a UUID in the editor).
func validNativeAttemptID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// NativeSaveReceipt binds confirmation to one exact editor snapshot and its
// successor version. All SQL int64 values cross the browser boundary as strings.
// SessionID is a public correlation ID, never an admission bearer.
type NativeSaveReceipt struct {
	WorkspaceID    string `json:"workspaceId"`
	PageID         string `json:"pageId"`
	ApplicationID  string `json:"applicationId"`
	EnginePin      string `json:"enginePin"`
	BaseVersion    string `json:"baseVersion"`
	Generation     string `json:"generation"`
	SessionID      string `json:"sessionId"`
	EditorRevision string `json:"editorRevision"`
	ContentHash    string `json:"contentHash"`
	CandidateHash  string `json:"candidateHash"`
	Version        string `json:"version"`
	Durability     string `json:"durability"`
	IssuedBy       string `json:"issuedBy"`
	Phase          string `json:"phase"`
}

// EnginePin identifies the immutable application/engine/editor compatibility profile.
func (s *NativeStore) EnginePin() string {
	if s == nil {
		return ""
	}
	c := s.config
	sum := sha256.Sum256([]byte(strings.Join([]string{c.Application, c.EngineRevision, c.EngineSHA256, c.EditorBundle}, "\x00")))
	return hex.EncodeToString(sum[:])
}
func (s *NativeStore) snapshotMessage(l EditLease, base int64, revision, hash string) (string, error) {
	if base < 1 || base == math.MaxInt64 || l.Generation < 1 || len(revision) == 0 || len(revision) > 256 || strings.ContainsRune(revision, 0) || !utf8.ValidString(revision) || !strings.HasPrefix(hash, "sha256:") || !nativeHex(strings.TrimPrefix(hash, "sha256:"), 64) {
		return "", ErrInvalid
	}
	// This is immutable audit DATA in the existing version, never an admission
	// capability or a second receipt ledger. Generic saves cannot forge this path.
	b, err := json.Marshal(struct {
		Kind, Application, EnginePin, SessionID, Generation, BaseVersion, EditorRevision, ContentHash string
	}{"craft-native-save-v1", s.config.Application, s.EnginePin(), l.Holder.SessionID, strconv.FormatInt(l.Generation, 10), strconv.FormatInt(base, 10), revision, hash})
	return string(b), err
}
func (s *NativeStore) snapshotReceipt(l EditLease, base int64, revision, hash string, version int64) NativeSaveReceipt {
	return NativeSaveReceipt{WorkspaceID: l.Scope.WorkspaceID, PageID: l.Scope.ArtifactID, ApplicationID: s.config.Application, EnginePin: s.EnginePin(), BaseVersion: strconv.FormatInt(base, 10), Generation: strconv.FormatInt(l.Generation, 10), SessionID: l.Holder.SessionID, EditorRevision: revision, ContentHash: hash, CandidateHash: hash, Version: strconv.FormatInt(version, 10), Durability: "node", IssuedBy: "crewship", Phase: "committed"}
}

func (s *NativeStore) SaveSnapshot(ctx context.Context, l EditLease, base int64, editorRevision, contentHash string, raw []byte) (NativeSaveReceipt, error) {
	return s.SaveAttempt(ctx, l, base, editorRevision, contentHash, raw, "")
}

// SaveAttempt is SaveSnapshot for one identified attempt. A non-empty
// attemptID that a reconcile retired is refused inside the publication
// transaction, after the writer fence, so validation that was still running
// when the reconcile answered cannot publish afterwards.
func (s *NativeStore) SaveAttempt(ctx context.Context, l EditLease, base int64, editorRevision, contentHash string, raw []byte, attemptID string) (NativeSaveReceipt, error) {
	if err := s.ready(); err != nil {
		return NativeSaveReceipt{}, err
	}
	if attemptID != "" && !validNativeAttemptID(attemptID) {
		return NativeSaveReceipt{}, ErrInvalid
	}
	message, err := s.snapshotMessage(l, base, editorRevision, contentHash)
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	a := s.authority(l.Scope, &l, "write")
	if attemptID != "" {
		write := a
		a = func(ctx context.Context, tx *sql.Tx) (string, error) {
			actor, err := write(ctx, tx)
			if err != nil {
				return "", err
			}
			var retired int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM craft_native_retired_attempts WHERE page_id=? AND session_id=? AND attempt_id=?`, l.Scope.ArtifactID, l.Holder.SessionID, attemptID).Scan(&retired); err != nil {
				return "", err
			}
			if retired > 0 {
				return "", ErrNativeAttemptRetired
			}
			return actor, nil
		}
	}
	if _, err = s.versions.Quota(ctx, nativeVersionKey(l.Scope), a); err != nil {
		return NativeSaveReceipt{}, err
	}
	b, err := s.candidate(ctx, raw)
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	hash := sha256.Sum256(b)
	if "sha256:"+hex.EncodeToString(hash[:]) != contentHash {
		return NativeSaveReceipt{}, ErrInvalid
	}
	request := nativeManifest(base, b)
	request.Message = message
	receipt, err := s.versions.Apply(ctx, nativeVersionKey(l.Scope), request, a)
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	return s.snapshotReceipt(l, base, editorRevision, contentHash, receipt.Version), nil
}

// ReconcileSnapshot proves only the exact attempted successor, including when
// the head has since advanced. It never retries a mutation or confirms a later
// matching document. Current write rights and the original admission are
// checked again; the lease need not be live, so a reply lost during an outage
// longer than the lease TTL stays provable. That lease still cannot save.
func (s *NativeStore) ReconcileSnapshot(ctx context.Context, l EditLease, base int64, editorRevision, contentHash string) (NativeSaveReceipt, error) {
	return s.ReconcileAttempt(ctx, l, base, editorRevision, contentHash, "")
}

// ReconcileAttempt is ReconcileSnapshot for one identified attempt. When
// version base+1 is not exactly that attempt, it records the attempt as
// retired in the same fenced transaction and reports ErrNativeAttemptAbsent:
// the attempt has not committed and now never will (#4899). Without an
// attemptID nothing can be retired, and the answer stays ErrNativeUnconfirmed.
func (s *NativeStore) ReconcileAttempt(ctx context.Context, l EditLease, base int64, editorRevision, contentHash, attemptID string) (NativeSaveReceipt, error) {
	if err := s.ready(); err != nil {
		return NativeSaveReceipt{}, err
	}
	if attemptID != "" && !validNativeAttemptID(attemptID) {
		return NativeSaveReceipt{}, ErrInvalid
	}
	message, err := s.snapshotMessage(l, base, editorRevision, contentHash)
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	var version int64
	absent := false
	err = s.versions.ReadVersionFenced(ctx, nativeVersionKey(l.Scope), base+1, s.holderAuthority(l, "write"), func(tx *sql.Tx, m artifacts.VersionManifest, readErr error) error {
		if readErr == nil {
			file, ok := m.Files["document.json"]
			hash := sha256.Sum256(file.Content)
			if m.Message == message && m.Actor == l.Holder.ActorID && m.EntryPoint == "document.json" && len(m.Files) == 1 && ok && file.MediaType == "application/json" && "sha256:"+hex.EncodeToString(hash[:]) == contentHash {
				version = m.Version
				return nil
			}
		}
		if attemptID == "" {
			return ErrNativeUnconfirmed
		}
		return s.retireAttempt(ctx, tx, l, base, attemptID, &absent)
	})
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	if absent {
		return NativeSaveReceipt{}, ErrNativeAttemptAbsent
	}
	return s.snapshotReceipt(l, base, editorRevision, contentHash, version), nil
}

// retireAttempt records attemptID as retired, inside the writer-fenced
// reconcile transaction. A retired row only matters while its attempt could
// still publish, and publication requires the live lease of exactly that
// holder session and generation (CheckWrite). A lapsed lease never becomes
// live again: renewal needs a live lease and acquisition bumps the
// generation. So:
//   - only the current live holder may retire (any other answer: unconfirmed);
//   - every row whose lease is no longer live for its session and generation
//     is deleted first (admission expiry, release, handoff, takeover, restart);
//   - a page keeps at most nativeRetiredAttemptsPerPage rows; beyond that
//     nothing is recorded and the answer is ErrNativeRetirementLimit (409).
//
// The table therefore holds at most nativeRetiredAttemptsPerPage rows per page
// with a live lease after every retirement, independent of request volume.
func (s *NativeStore) retireAttempt(ctx context.Context, tx *sql.Tx, l EditLease, base int64, attemptID string, absent *bool) error {
	if err := s.config.Leases.CheckWrite(ctx, tx, l); err != nil {
		if errors.Is(err, ErrEditLeaseStale) || errors.Is(err, ErrEditLeaseBusy) || errors.Is(err, ErrEditLeaseDenied) {
			return ErrNativeUnconfirmed
		}
		return err
	}
	boot, now, err := s.config.Leases.clock()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM craft_native_retired_attempts WHERE NOT EXISTS (
 SELECT 1 FROM mooring_edit_leases e WHERE e.page_id=craft_native_retired_attempts.page_id AND e.workspace_id=craft_native_retired_attempts.workspace_id
  AND e.session_id=craft_native_retired_attempts.session_id AND e.generation=craft_native_retired_attempts.generation
  AND e.actor_id<>'' AND e.session_id<>'' AND e.boot_id=? AND e.expires_ns>?)`, boot, now); err != nil {
		return err
	}
	var existing, rows int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM craft_native_retired_attempts WHERE page_id=? AND session_id=? AND attempt_id=?`, l.Scope.ArtifactID, l.Holder.SessionID, attemptID).Scan(&existing); err != nil {
		return err
	}
	if existing == 0 {
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM craft_native_retired_attempts WHERE page_id=?`, l.Scope.ArtifactID).Scan(&rows); err != nil {
			return err
		}
		if rows >= nativeRetiredAttemptsPerPage {
			return ErrNativeRetirementLimit
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO craft_native_retired_attempts(page_id,workspace_id,session_id,attempt_id,generation,base_version) VALUES(?,?,?,?,?,?)`, l.Scope.ArtifactID, l.Scope.WorkspaceID, l.Holder.SessionID, attemptID, l.Generation, base); err != nil {
			return err
		}
	}
	*absent = true
	return nil
}
