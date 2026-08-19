package pages

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/schemas"
)

// narrative.v1 — the panel an AI agent writes.
//
// Every other schema in this package is defended because a producer script can
// be wrong. This one is defended because its producer can be TALKED INTO being
// wrong: an agent's context may already hold injected content read from a
// container or an integration (§8 rule 8), so the payload is treated exactly
// like a string that arrived from the internet. §8's ten rules are this file's
// specification, and the ones with teeth here are:
//
//	1. The agent fills a schema; it never emits markup, HTML, CSS or code.
//	   Hence typed blocks with a `kind` from a closed enum, and never a
//	   markdown blob. (Adaptive Cards: "no code behind"; the host owns the
//	   look and feel.)
//	2. No images. None — absent from the schema, not sanitised. CamoLeak
//	   exfiltrated through a TRUSTED FIRST-PARTY image proxy, so neither an
//	   allow-list nor CSP was the control that would have stopped it. The
//	   control is having no field.
//	3. No free-form links. A block may reference an internal entity by id and
//	   the renderer builds the URL. Slack AI's private-channel exfiltration
//	   was a rendered link, so EntityRef carries an id and a kind and has
//	   nowhere to put a destination.
//	10. Text renders through React elements, never innerHTML — enforced on the
//	   other side of the wire, and pinned by a test that reads the panel
//	   sources.
//
// Rules 4-7 (actions and their friction) have no representation here on
// purpose: narrative.v1 ships text-only per §12, and `actions` is not a field
// of this payload. Because the published schema is additionalProperties:false,
// an agent that invents one is refused at the boundary rather than quietly
// rendered without buttons.

// NarrativeBlockKind is the closed set of block types. Two members, both prose:
// there is no "html", no "code" and no "image", and adding one is a server
// release with this comment in front of it.
type NarrativeBlockKind string

const (
	// BlockParagraph is one paragraph.
	BlockParagraph NarrativeBlockKind = "paragraph"
	// BlockList is one bullet. Consecutive list blocks render as a single
	// unordered list — which is how a list exists at all in a schema whose
	// block carries exactly one string.
	BlockList NarrativeBlockKind = "list"
)

// EntityKind names which internal noun an EntityRef points at. Closed, because
// the renderer holds one route per member: a kind it did not know could only
// become a dead link or an unvalidated path.
type EntityKind string

const (
	EntityIssue EntityKind = "issue"
	EntityRun   EntityKind = "run"
	EntityPage  EntityKind = "page"
	EntityAgent EntityKind = "agent"
	EntityCrew  EntityKind = "crew"
)

var knownEntityKinds = map[EntityKind]bool{
	EntityIssue: true,
	EntityRun:   true,
	EntityPage:  true,
	EntityAgent: true,
	EntityCrew:  true,
}

// Known reports whether k is a member of the closed set.
func (k EntityKind) Known() bool { return knownEntityKinds[k] }

func (k EntityKind) String() string { return string(k) }

// EntityRef is §8 rule 3 as a type: an id and a kind, and no field in which a
// URL could travel. The renderer maps Kind to a route it already knows and
// interpolates ID.
type EntityRef struct {
	Kind EntityKind `json:"kind"`
	ID   string     `json:"id"`
}

// NarrativeBlock is one typed block of prose.
type NarrativeBlock struct {
	Kind NarrativeBlockKind `json:"kind"`
	Text string             `json:"text"`
	Ref  *EntityRef         `json:"ref,omitempty"`
}

// NarrativePayload is narrative.v1.
type NarrativePayload struct {
	// Blocks is required. An empty array is a measured "the agent ran and had
	// nothing to say" and renders the panel's own sentence; an absent one is a
	// producer that stopped halfway, and is refused.
	Blocks []NarrativeBlock `json:"blocks"`
	// Verdict is the one-line conclusion, omitted when there is none. It is
	// NOT nullable: the em dash means "no basis to compute a value" (§9b.4)
	// and a missing sentence is not a missing measurement, so borrowing the
	// glyph here would blunt the one distinction the product rests on.
	Verdict string `json:"verdict,omitempty"`
}

// Schema implements Payload.
func (p *NarrativePayload) Schema() PanelSchema { return SchemaNarrative }

// HasVerdict reports whether the agent reached a conclusion.
func (p *NarrativePayload) HasVerdict() bool {
	return p != nil && strings.TrimSpace(p.Verdict) != ""
}

// maxNarrativeBlocks mirrors the published schema. Kept here as well because
// decodeStrict is the belt to the schema's braces, and a cap that exists in
// only one of the two is a cap that drifts.
const maxNarrativeBlocks = 40

// ValidateNarrative validates and decodes a narrative.v1 payload, including
// the §8 checks JSON Schema cannot express in a message a producer can act on.
func ValidateNarrative(raw []byte) (*NarrativePayload, error) {
	if err := checkSize(SchemaNarrative, raw); err != nil {
		return nil, err
	}
	if err := validateAgainst(SchemaNarrative, schemas.PanelNarrativeV1, raw); err != nil {
		return nil, err
	}
	var p NarrativePayload
	if err := decodeStrict(raw, &p); err != nil {
		return nil, newError(CodeSchemaViolation, SchemaNarrative, "%v", err)
	}

	if len(p.Blocks) > maxNarrativeBlocks {
		return nil, newError(CodeSchemaViolation, SchemaNarrative,
			"payload carries %d blocks; the cap is %d", len(p.Blocks), maxNarrativeBlocks)
	}

	// The verdict and every block's text are the two places an injected
	// instruction reaches a human's eye, so both go through the same check.
	if err := checkAgentText(SchemaNarrative, "verdict", p.Verdict); err != nil {
		return nil, err
	}
	for i := range p.Blocks {
		b := &p.Blocks[i]
		switch b.Kind {
		case BlockParagraph, BlockList:
		default:
			// Unreachable while the schema's enum holds; kept because the
			// struct is also built in-process by tests and future callers,
			// and a block kind the renderer does not know renders as nothing.
			return nil, newError(CodeSchemaViolation, SchemaNarrative,
				"block %d declares kind %q; the set is paragraph and list", i, b.Kind)
		}
		if err := checkAgentText(SchemaNarrative, fmt.Sprintf("block %d text", i), b.Text); err != nil {
			return nil, err
		}
		if b.Ref != nil && !b.Ref.Kind.Known() {
			return nil, newError(CodeSchemaViolation, SchemaNarrative,
				"block %d references entity kind %q; the renderer holds one route per kind and does not know that one",
				i, b.Ref.Kind)
		}
	}
	return &p, nil
}

// urlishPrefixes are the shapes a reader's client — or a reader — turns into a
// destination. §8 rule 3 removes the URL FIELD; this removes the URL smuggled
// into prose, which is what §14 asks for in as many words: *"a narrative
// payload containing an image block, an external URL, or an undeclared action
// index is rejected at the API boundary."*
//
// Detection is on the lowercased text and deliberately narrow. A bare domain
// ("see the crewship.io docs") is inert prose and stays legal; a scheme, a
// protocol-relative host or a `www.` is what an auto-linkifier, a terminal, a
// copy-paste or a preview fetcher acts on.
var urlishPrefixes = []string{
	"://",         // any scheme: https, ftp, gopher, anything a client resolves
	"//",          // protocol-relative
	"javascript:", // the classic
	"data:",       // CamoLeak's family: content that fetches on render
	"vbscript:",
	"file:",
	"blob:",
	"mailto:",
	"tel:",
	"www.",
}

// checkAgentText applies §8's text rules to one agent-authored string. Empty is
// always fine — the schema decides whether a field may be empty; this decides
// what may be IN it.
func checkAgentText(schema PanelSchema, field, text string) error {
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	for _, p := range urlishPrefixes {
		if containsSchemeAtABoundary(lower, p) {
			return newError(CodeInconsistentPayload, schema,
				"%s carries %q, which is a URL; §8 rule 3 gives a block no field for one — "+
					"reference the entity with `ref` and let the renderer build the link", field, p)
		}
	}
	if r, ok := firstUnsafeRune(text); ok {
		return newError(CodeInconsistentPayload, schema,
			"%s carries the control character U+%04X; a sentence that can reorder or truncate "+
				"itself on screen does not say what was stored", field, r)
	}
	return nil
}

// containsSchemeAtABoundary reports whether needle appears in text as a scheme
// rather than as the tail of a word.
//
// A bare substring match is what this replaced, and it refused prose: "Profile:
// 42 active users" carries "file:", "Metadata: 3 tables" carries "data:",
// "Hotel: 5 free rooms" carries "tel:". narrative.v1 is the panel an AGENT
// writes and the refusal is a hard 400, so an incident summary could be
// rejected for containing an ordinary English word.
//
// A scheme is only a scheme at a boundary: start of string, or after something
// that is not a letter or a digit. "see https://x" still matches, "Profile:"
// does not. The needles that are not schemes — "://", "//", "www." — are left
// as plain substring matches, because they have no leading word to be confused
// with and "x://y" must still be caught.
func containsSchemeAtABoundary(text, needle string) bool {
	if !strings.HasSuffix(needle, ":") {
		return strings.Contains(text, needle)
	}
	for i := 0; ; {
		j := strings.Index(text[i:], needle)
		if j < 0 {
			return false
		}
		at := i + j
		if at == 0 {
			return true
		}
		prev, _ := utf8.DecodeLastRuneInString(text[:at])
		if !unicode.IsLetter(prev) && !unicode.IsDigit(prev) {
			return true
		}
		i = at + 1
	}
}

// firstUnsafeRune finds a character that changes what a rendered sentence says
// without changing what it stores.
//
// Three families, listed rather than taken as a whole Unicode category. C0/C1
// controls minus the whitespace prose legitimately uses — a NUL or an ESC in a
// panel is either a bug or an attempt at a terminal that will eventually print
// it. The bidirectional OVERRIDES (Trojan Source), U+202A-202E and
// U+2066-2069, which let an agent write a sentence that renders backwards, so
// what a reviewer approves and what a reader sees are different strings. And
// the invisibles — zero-width space, the word-joiner block, the BOM and the
// deprecated tag characters — which can hide a word inside another word.
//
// Deliberately NOT rejected: the whole Cf category. It holds U+0600-0605,
// which are ordinary Arabic prose, and the LRM/RLM marks that mixed-direction
// text needs to lay out correctly. §10b.7 says a page renders the workspace's
// language; a check that refuses Arabic is not a security control.
func firstUnsafeRune(s string) (rune, bool) {
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r' || r == ' ':
			continue
		case r < 0x20, r >= 0x7f && r <= 0x9f:
			return r, true
		case r >= 0x202a && r <= 0x202e: // LRE RLE PDF LRO RLO
			return r, true
		case r >= 0x2066 && r <= 0x2069: // LRI RLI FSI PDI
			return r, true
		case r == 0x200b, r == 0xfeff: // zero-width space, BOM
			return r, true
		case r >= 0x2060 && r <= 0x2064: // word joiner and the invisible operators
			return r, true
		case r >= 0xe0000 && r <= 0xe007f: // deprecated tag characters
			return r, true
		}
	}
	return 0, false
}
