package access

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"
)

// DecodePublicationAuthority accepts only the exact canonical schema2 encoding
// and its expected fingerprint. Identity is not authorization, graph completeness
// or evidence of current grant/provider liveness. This performs no persistence.
func DecodePublicationAuthority(raw []byte, expectedFP string) (PublicationAuthority, error) {
	deny := func() (PublicationAuthority, error) { return PublicationAuthority{}, ErrDenied }
	if len(raw) == 0 || len(raw) > 256<<10 || !utf8.Valid(raw) || !publicationDigest(expectedFP) {
		return deny()
	}
	// Variable-length tuples are intentional: JSON decoding fixed arrays silently
	// discards extra elements and zero-fills short arrays. Validate before mapping.
	var wire struct {
		Schema      int        `json:"schema"`
		Workspace   string     `json:"workspace"`
		Pipeline    string     `json:"pipeline"`
		Publisher   string     `json:"publisher"`
		Membership  string     `json:"membership"`
		Crew        string     `json:"crew"`
		AuthorAgent string     `json:"author_agent"`
		ActingAgent string     `json:"acting_agent"`
		RootOrigin  string     `json:"root_origin"`
		Authoring   [][]string `json:"authoring"`
		Execution   [][]string `json:"execution"`
		Recipes     [][]string `json:"recipes"`
		Agents      [][]string `json:"agents"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil {
		return deny()
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return deny()
	}
	a := PublicationAuthority{Schema: wire.Schema, WorkspaceID: wire.Workspace, PipelineID: wire.Pipeline, PublisherID: wire.Publisher, PublisherMembershipID: wire.Membership, RootCrewID: wire.Crew, AuthorAgentID: wire.AuthorAgent, ActingAgentID: wire.ActingAgent, RootOriginID: wire.RootOrigin}
	grants := func(tuples [][]string) ([]PublicationGrantBinding, bool) {
		out := make([]PublicationGrantBinding, 0, len(tuples))
		for _, v := range tuples {
			if len(v) != 9 {
				return nil, false
			}
			out = append(out, PublicationGrantBinding{WorkspaceID: v[0], PublisherID: v[1], PublisherMembershipID: v[2], Right: Right{Kind: v[3], ID: v[4], Operation: v[5]}, GrantID: v[6], IssuerID: v[7], IssuerMembershipID: v[8]})
		}
		return out, true
	}
	var ok bool
	if a.Authoring, ok = grants(wire.Authoring); !ok {
		return deny()
	}
	if a.Execution, ok = grants(wire.Execution); !ok {
		return deny()
	}
	for _, v := range wire.Recipes {
		if len(v) != 8 {
			return deny()
		}
		version, err := strconv.Atoi(v[4])
		if err != nil {
			return deny()
		}
		a.Recipes = append(a.Recipes, PublicationRecipeBinding{ParentVersionID: v[0], StepID: v[1], PipelineID: v[2], VersionID: v[3], Version: version, ContentHash: v[5], AuthorityFingerprint: v[6], VersionOriginID: v[7]})
	}
	for _, v := range wire.Agents {
		if len(v) != 13 {
			return deny()
		}
		tokens, err := strconv.ParseInt(v[12], 10, 64)
		if err != nil {
			return deny()
		}
		a.Agents = append(a.Agents, PublicationAgentBinding{ParentVersionID: v[0], StepID: v[1], AgentID: v[2], CrewID: v[3], Profile: v[4], PolicyHash: v[5], CredentialID: v[6], GrantID: v[7], CredentialRevision: v[8], Model: v[9], GrantExpiry: v[10], KeyExpiry: v[11], MaxOutputTokens: tokens})
	}
	canonical, fp, err := CanonicalPublicationAuthority(a)
	// Exact equality also rejects duplicate keys, omitted/null fields, alternate
	// ordering/escaping, whitespace and noncanonical numeric/expiry spellings.
	if err != nil || fp != expectedFP || !bytes.Equal(canonical, raw) {
		return deny()
	}
	return a, nil
}
