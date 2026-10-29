package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"github.com/gowebpki/jcs"
)

// FullCheckpointBasis is a wire checkpoint identity. It is deliberately a
// distinct type from the internal opaque-content SessionBasis. Neither grants
// scope authority or proves a peer; sealed author/owner checks must run first.
type FullCheckpointBasis struct{ Revision, Digest string }

func validFullCheckpointBasis(b FullCheckpointBasis) bool {
	_, err := batchCounter(b.Revision)
	return err == nil && checkpointDigestShape(b.Digest)
}
func decodeRetainedCheckpointDescription(raw []byte, maximum int) (BatchCheckpointDescription, error) {
	var description BatchCheckpointDescription
	if canonicalPreflightLimit(raw, maximum) != nil {
		return description, ErrCheckpointIdentity
	}
	canonical, err := jcs.Transform(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return description, ErrCheckpointIdentity
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&description) != nil {
		return description, ErrCheckpointIdentity
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return description, ErrCheckpointIdentity
	}
	exact, err := canonicalCheckpointDescription(description, maximum)
	// Also rejects absent fields, null, case-folded Go aliases and unsorted
	// asset manifests; JSON decoder permissiveness cannot relabel metadata.
	if err != nil || !bytes.Equal(exact, raw) {
		return BatchCheckpointDescription{}, ErrCheckpointIdentity
	}
	return description, nil
}

// loadRetainedFullCheckpoint is writer-internal. Its caller must have already
// authorized all original sealed operations (or the actual external reader)
// and checked current ownership. It never infers unknown legacy interpretation.
func (s *BatchStore) loadRetainedFullCheckpoint(ctx context.Context, tx *sql.Tx, key SessionKey, revision string, current bool) (protocol.Checkpoint, CheckpointIdentity, error) {
	var empty protocol.Checkpoint
	var identity CheckpointIdentity
	if tx == nil || !s.checkpointIdentityEnabled() || s.ready() != nil {
		return empty, identity, ErrCheckpointIdentity
	}
	if _, err := batchCounter(revision); err != nil {
		return empty, identity, ErrCheckpointIdentity
	}
	var local bool
	var generation sql.NullString
	var size sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id=?,CASE WHEN length(CAST(checkpoint_generation AS BLOB)) BETWEEN 1 AND 20 THEN checkpoint_generation ELSE NULL END,length(CAST(checkpoint_metadata_json AS BLOB)) FROM mooring_collab_checkpoints WHERE page_id=? AND revision=?`, key.WorkspaceID, key.ArtifactID, revision).Scan(&local, &generation, &size); err != nil {
		return empty, identity, err
	}
	maximum := s.config.CheckpointIdentity.MaxMetadataBytes
	if !local || !generation.Valid || !size.Valid || size.Int64 < 1 || size.Int64 > int64(maximum) {
		return empty, identity, ErrCheckpointIdentity
	}
	creator, err := batchCounter(generation.String)
	if err != nil || creator == 0 {
		return empty, identity, ErrCheckpointIdentity
	}
	// SQL length checks precede scanning either metadata or encoded content.
	var retained string
	if err = tx.QueryRowContext(ctx, `SELECT checkpoint_metadata_json FROM mooring_collab_checkpoints WHERE page_id=? AND revision=?`, key.ArtifactID, revision).Scan(&retained); err != nil {
		return empty, identity, err
	}
	if int64(len(retained)) != size.Int64 {
		return empty, identity, ErrCheckpointIdentity
	}
	metadata, err := decodeRetainedCheckpointDescription([]byte(retained), maximum)
	if err != nil {
		return empty, identity, err
	}
	state, err := sessionCheckpoint(ctx, tx, key, revision, current)
	if err != nil {
		return empty, identity, err
	}
	cp := protocol.Checkpoint{Scope: protocol.Scope{ApplicationID: protocol.Identifier(key.ApplicationID), WorkspaceID: protocol.Identifier(key.WorkspaceID), ArtifactID: protocol.Identifier(key.ArtifactID)}, Revision: protocol.Counter(revision), Generation: protocol.Counter(generation.String), FormatVersion: metadata.FormatVersion, Versions: metadata.Versions, Encoding: "base64", State: base64.StdEncoding.EncodeToString(state), Assets: metadata.Assets}
	identity, err = ComputeCheckpointIdentity(cp, CheckpointIdentityLimits{DecodedBytes: maxPreparedCheckpoint, PreimageBytes: sessionOutputBytes})
	if err != nil {
		return empty, CheckpointIdentity{}, err
	}
	cp.Digest = protocol.Digest(identity.FullDigest)
	return cp, identity, nil
}
func (s *BatchStore) resolveFullCheckpointBasis(ctx context.Context, tx *sql.Tx, key SessionKey, basis FullCheckpointBasis) (SessionBasis, error) {
	if !validFullCheckpointBasis(basis) {
		return SessionBasis{}, ErrCheckpointIdentity
	}
	if err := ownerScopeExists(ctx, tx, key); err != nil {
		return SessionBasis{}, err
	}
	// Document scope is retained immutable by the collaboration type fence.
	var local bool
	var head string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id=? AND application_id=?,CASE WHEN length(CAST(revision AS BLOB))<=20 THEN revision ELSE '' END FROM mooring_collab_documents WHERE page_id=?`, key.WorkspaceID, key.ApplicationID, key.ArtifactID).Scan(&local, &head); err != nil {
		return SessionBasis{}, err
	}
	current, err := batchCounter(head)
	if err != nil || !local {
		return SessionBasis{}, ErrCheckpointIdentity
	}
	observed, err := batchCounter(basis.Revision)
	if err != nil || observed > current {
		return SessionBasis{}, ErrCheckpointIdentity
	}
	_, original, err := s.loadRetainedFullCheckpoint(ctx, tx, key, basis.Revision, basis.Revision == head)
	if err != nil || original.FullDigest != basis.Digest {
		return SessionBasis{}, ErrCheckpointIdentity
	}
	// An older qualified basis cannot disguise an unknown/corrupt current head.
	if basis.Revision != head {
		if _, _, err = s.loadRetainedFullCheckpoint(ctx, tx, key, head, true); err != nil {
			return SessionBasis{}, err
		}
	}
	return SessionBasis{Revision: basis.Revision, Digest: original.ContentDigest}, nil
}
