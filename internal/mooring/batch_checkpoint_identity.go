package mooring

import (
	"context"
	"encoding/json"
	"slices"
	"unicode/utf16"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"github.com/gowebpki/jcs"
)

const maxCheckpointMetadataBytes = 2 * 1024 * 1024

// Description is application-qualified interpretation of these EXACT bytes.
// There is no default format, current-version substitution or empty manifest.
// This metadata grants no authority and does not authenticate imported state.
type BatchCheckpointDescription struct {
	FormatVersion protocol.Identifier `json:"format_version"`
	Versions      protocol.Versions   `json:"versions"`
	Assets        []protocol.Asset    `json:"assets"`
}
type BatchCheckpointDescriber interface {
	DescribeCheckpoint(context.Context, []byte) (BatchCheckpointDescription, error)
}

// Explicit dormant opt-in; live constructors must additionally verify the
// concrete adapter/process capability. An arbitrary interface is not admission.
type BatchCheckpointIdentityConfig struct {
	Enabled          bool
	Adapter          BatchCheckpointDescriber
	MaxMetadataBytes int
}

func (s *BatchStore) checkpointIdentityEnabled() bool {
	return s.config.CheckpointIdentity != nil && s.config.CheckpointIdentity.Enabled
}
func (s *BatchStore) checkpointIdentityConfigValid() bool {
	if !s.checkpointIdentityEnabled() {
		return true
	}
	c := s.config.CheckpointIdentity
	return c.Adapter != nil && c.MaxMetadataBytes > 0 && c.MaxMetadataBytes <= maxCheckpointMetadataBytes
}
func canonicalCheckpointDescription(description BatchCheckpointDescription, maximum int) ([]byte, error) {
	if maximum < 1 || maximum > maxCheckpointMetadataBytes || !ValidWireIdentifier(string(description.FormatVersion)) || description.Assets == nil || len(description.Assets) > 4096 {
		return nil, ErrInvalid
	}
	for _, v := range []string{description.Versions.ProtocolVersion, description.Versions.ModelVersion, description.Versions.EngineVersion} {
		if !ValidWireVersion(v) {
			return nil, ErrInvalid
		}
	}
	type ordered struct {
		asset protocol.Asset
		key   []uint16
	}
	assets := make([]ordered, 0, len(description.Assets))
	seen := make(map[string]bool, len(description.Assets))
	for _, asset := range description.Assets {
		id := string(asset.AssetID)
		if !ValidWireIdentifier(id) || !checkpointDigestShape(string(asset.Digest)) || seen[id] {
			return nil, ErrInvalid
		}
		if _, err := batchCounter(string(asset.SizeBytes)); err != nil {
			return nil, ErrInvalid
		}
		seen[id] = true
		assets = append(assets, ordered{asset, utf16.Encode([]rune(id))})
	}
	slices.SortFunc(assets, func(a, b ordered) int { return slices.Compare(a.key, b.key) })
	// Do not mutate the app's returned/cached manifest backing array.
	description.Assets = make([]protocol.Asset, 0, len(assets))
	for _, entry := range assets {
		description.Assets = append(description.Assets, entry.asset)
	}
	raw, err := json.Marshal(description)
	if err != nil {
		return nil, ErrInvalid
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(canonical) > maximum {
		return nil, ErrBatchCapacity
	}
	return canonical, nil
}
func (s *BatchStore) describeCheckpoint(ctx context.Context, checkpoint []byte) ([]byte, error) {
	if !s.checkpointIdentityEnabled() {
		return nil, nil
	}
	description, err := s.config.CheckpointIdentity.Adapter.DescribeCheckpoint(ctx, append([]byte(nil), checkpoint...))
	if err != nil {
		return nil, err
	}
	return canonicalCheckpointDescription(description, s.config.CheckpointIdentity.MaxMetadataBytes)
}
func checkpointMetadataSQL(metadata []byte) any {
	if metadata == nil {
		return nil
	}
	return string(metadata)
}
