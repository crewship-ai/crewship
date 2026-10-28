package mooring

import (
	"context"
	"crypto/sha256"
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
	if err := s.ready(); err != nil {
		return NativeSaveReceipt{}, err
	}
	message, err := s.snapshotMessage(l, base, editorRevision, contentHash)
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	a := s.authority(l.Scope, &l, "write")
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
	request := s.nativeManifest(base, b)
	request.Message = message
	receipt, err := s.versions.Apply(ctx, nativeVersionKey(l.Scope), request, a)
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	return s.snapshotReceipt(l, base, editorRevision, contentHash, receipt.Version), nil
}

// ReconcileSnapshot proves only the exact attempted successor, including when
// the head has since advanced. It never retries a mutation or confirms a later
// matching document. The current original admission and lease are checked again.
func (s *NativeStore) ReconcileSnapshot(ctx context.Context, l EditLease, base int64, editorRevision, contentHash string) (NativeSaveReceipt, error) {
	if err := s.ready(); err != nil {
		return NativeSaveReceipt{}, err
	}
	message, err := s.snapshotMessage(l, base, editorRevision, contentHash)
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	m, err := s.versions.ReadVersion(ctx, nativeVersionKey(l.Scope), base+1, s.authority(l.Scope, &l, "write"))
	if errors.Is(err, artifacts.ErrVersionNotFound) {
		return NativeSaveReceipt{}, ErrNativeUnconfirmed
	}
	if err != nil {
		return NativeSaveReceipt{}, err
	}
	name, media, _ := NativeDocumentFormat(s.config.Application)
	file, ok := m.Files[name]
	hash := sha256.Sum256(file.Content)
	if m.Message != message || m.Actor != l.Holder.ActorID || m.EntryPoint != name || len(m.Files) != 1 || !ok || file.MediaType != media || "sha256:"+hex.EncodeToString(hash[:]) != contentHash {
		return NativeSaveReceipt{}, ErrNativeUnconfirmed
	}
	return s.snapshotReceipt(l, base, editorRevision, contentHash, m.Version), nil
}
