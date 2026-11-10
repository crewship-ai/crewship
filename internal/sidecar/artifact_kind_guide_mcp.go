package sidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/artifacts/kindguide"
)

const artifactKindGuideTool = "artifact_kind_guide"

func artifactKindGuideMCPCatalog() []memoryMCPToolDescriptor {
	schema, _ := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"kind"},
		"properties": map[string]any{"kind": map[string]any{"type": "string", "enum": kindguide.Kinds(), "description": "The artifact kind named in the Page context, or docs, slides, design, library, dashboard or app."}},
	})
	return []memoryMCPToolDescriptor{{
		Name: artifactKindGuideTool,
		Description: "Read the versioned agent guide for an artifact kind before you change an artifact of that kind: exact data shapes and limits, " +
			"write order, the version check, the conflict rule (stop after the third refusal and report), agent modes and when not to verify. " +
			"Comment is not Propose, and only the artifact's Owner delegates Edit. The guide is static product text: it grants nothing and " +
			"changes nothing; the server still checks every write.",
		InputSchema: schema,
	}}
}

// artifactKindGuideArguments accepts exactly {"kind": "<string>"}.
func artifactKindGuideArguments(raw []byte) (string, error) {
	refused := errors.New("artifact_kind_guide accepts exactly {\"kind\": string}")
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if len(raw) > 1024 || decoder.Decode(&fields) != nil || len(fields) != 1 {
		return "", refused
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", refused
	}
	value, ok := fields["kind"] // exact key: encoding/json would fold "KIND" into a struct field
	var kind string
	if !ok || json.Unmarshal(value, &kind) != nil {
		return "", refused
	}
	return kind, nil
}

// respondArtifactKindGuideMCPToolCall serves the embedded guide without a host
// call. The caller has already presented a recognised agent token.
func (s *Server) respondArtifactKindGuideMCPToolCall(w http.ResponseWriter, req memoryMCPRequest, name string, raw []byte) bool {
	if name != artifactKindGuideTool {
		return false
	}
	kind, err := artifactKindGuideArguments(raw)
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest, mustJSON(map[string]string{"error": err.Error()}))
		return true
	}
	// Only the canonical kind names the schema lists; aliases and padding are
	// refused so the tool contract stays exactly its schema.
	guide, err := kindguide.Lookup(kind)
	if err == nil && guide.Kind != kind {
		err = kindguide.ErrUnknownKind
	}
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, http.StatusNotFound, mustJSON(map[string]any{"error": "no agent guide for this artifact kind", "kinds": kindguide.Kinds()}))
		return true
	}
	s.writeRoutinesMCPToolResult(w, req, http.StatusOK, mustJSON(guide))
	return true
}
