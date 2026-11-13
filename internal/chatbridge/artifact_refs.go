package chatbridge

import (
	"context"
	"errors"
	"regexp"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

// ErrArtifactRefUnavailable is the one answer for a reference the sender may
// not attach: missing, unreadable, elsewhere and malformed read the same.
var ErrArtifactRefUnavailable = errors.New("an attached artifact is not available")

// ArtifactRefsMetadataKey is the message metadata the composer sends for its
// attachment chips. Only these structured ids are references; text in the
// message, names and links never are.
const ArtifactRefsMetadataKey = "artifact_refs"

// maxArtifactRefs and artifactRefID mirror the server's bound and id shape.
const maxArtifactRefs = 10

var artifactRefID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ArtifactRefRecorder checks references before the human message is stored and
// records them after, both against the sending human (#5317).
type ArtifactRefRecorder interface {
	// ArtifactRefsEnabled is the workspace flag. Off, the metadata key is
	// ignored as it was before the flag existed.
	ArtifactRefsEnabled(context.Context, string) (bool, error)
	CheckArtifactRefs(context.Context, *chataudience.Receipt, []string) error
	RecordArtifactRefs(context.Context, *chataudience.Receipt, string, []string) error
}

func (b *Bridge) SetArtifactRefRecorder(recorder ArtifactRefRecorder) {
	b.artifactRefRecorder = recorder
}

// requestedArtifactRefs reads `artifact_refs: [{"page_id": "<id>"}, ...]`.
// Any other shape is refused, never partly used.
func requestedArtifactRefs(metadata map[string]any) ([]string, bool, error) {
	raw, exists := metadata[ArtifactRefsMetadataKey]
	if !exists {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 || len(list) > maxArtifactRefs {
		return nil, false, ErrArtifactRefUnavailable
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok || len(obj) != 1 {
			return nil, false, ErrArtifactRefUnavailable
		}
		id, ok := obj["page_id"].(string)
		if !ok || !artifactRefID.MatchString(id) || seen[id] {
			return nil, false, ErrArtifactRefUnavailable
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, true, nil
}
