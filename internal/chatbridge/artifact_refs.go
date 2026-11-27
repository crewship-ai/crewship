package chatbridge

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

// ErrArtifactRefUnavailable is the one answer for a reference the sender may
// not attach: missing, unreadable, elsewhere and malformed read the same.
var ErrArtifactRefUnavailable = errors.New("an attached artifact is not available")

// ErrArtifactConsentStale refuses a message whose attached Sheet is no longer
// at the revision the person saw when they allowed the agent to change it.
var ErrArtifactConsentStale = errors.New("the Sheet changed after you allowed the agent to change it")

// ArtifactConsentStaleText is what the person is told; the UI keys on the reason.
const ArtifactConsentStaleText = "The Sheet changed after you allowed the agent to change it. Allow it again."

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
	// The versions map is the revision the person saw for each Sheet they let
	// the agent change (metadata version); it may be nil or partial.
	CheckArtifactRefVersions(context.Context, *chataudience.Receipt, []string, map[string]int64) error
	RecordArtifactRefVersions(context.Context, *chataudience.Receipt, string, []string, map[string]int64) error
}

func (b *Bridge) SetArtifactRefRecorder(recorder ArtifactRefRecorder) {
	b.artifactRefRecorder = recorder
}

// artifactRefVersion reads a reference's version: a positive decimal string.
func artifactRefVersion(raw any) (int64, bool) {
	text, ok := raw.(string)
	if !ok || !artifactRefVersionText.MatchString(text) {
		return 0, false
	}
	v, err := strconv.ParseInt(text, 10, 64)
	return v, err == nil && v > 0
}

var artifactRefVersionText = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

// requestedArtifactRefVersions is the revision each reference names, for the
// references requestedArtifactRefs accepted.
func requestedArtifactRefVersions(metadata map[string]any) map[string]int64 {
	list, _ := metadata[ArtifactRefsMetadataKey].([]any)
	out := map[string]int64{}
	for _, item := range list {
		obj, _ := item.(map[string]any)
		id, _ := obj["page_id"].(string)
		if v, ok := artifactRefVersion(obj["version"]); ok && id != "" {
			out[id] = v
		}
	}
	return out
}

// requestedArtifactRefs reads `artifact_refs: [{"page_id": "<id>", "version": "<n>"}, ...]`;
// version is optional and names the revision the person saw.
// Any other shape is refused, never partly used.
func requestedArtifactRefs(metadata map[string]any) ([]string, bool, error) {
	raw, exists := metadata[ArtifactRefsMetadataKey]
	if !exists {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	// An explicit empty list is the person's choice of no Sheet: the message goes without one.
	if ok && len(list) == 0 {
		return nil, false, nil
	}
	if !ok || len(list) > maxArtifactRefs {
		return nil, false, ErrArtifactRefUnavailable
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok || len(obj) < 1 || len(obj) > 2 {
			return nil, false, ErrArtifactRefUnavailable
		}
		if _, hasVersion := obj["version"]; len(obj) == 2 && !hasVersion {
			return nil, false, ErrArtifactRefUnavailable
		}
		if raw, hasVersion := obj["version"]; hasVersion {
			if _, valid := artifactRefVersion(raw); !valid {
				return nil, false, ErrArtifactRefUnavailable
			}
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

// attachedSheetRunPrompt tells the run which attached pages are open beside the chat.
// The person sees the Sheet next to the conversation; the uploaded file in the working directory
// is only the copy it was opened from, and changing that copy changes nothing they see.
func attachedSheetRunPrompt(content string, pageIDs []string) string {
	if len(pageIDs) == 0 {
		return content
	}
	refs := make([]string, len(pageIDs))
	for i, id := range pageIDs {
		refs[i] = "@" + id
	}
	return content + "\n\nThe person attached " + strings.Join(refs, ", ") + " to this message; a Sheet among them is open beside the chat, where they see it. " +
		"Work on that Sheet, not the attached file in your working directory: read it with craft_sheet_read (document.inspect for the sheets, sheet.read for values) and change it with craft_sheet_acquire, then craft_sheet_apply, then craft_sheet_release. " +
		"Name the sheet in every write with params.sheet (sheet L, cell B8 is 'L'!B8); a write without its sheet is refused. " +
		"Each saved change is a new version the person can undo. If craft_sheet_apply is refused as not allowed, use craft_sheet_propose and say so. " +
		"Never create new .xlsx or .xlsm files and never change a copy of the workbook with Python or any other tool: the Sheet changes only through craft_sheet_*. " +
		"If a craft_sheet tool refuses, tell the person at once what it said and stop; do not work around it."
}
