// Package mentions reads agent @mentions out of a Markdown body.
//
// The wire format is a plain Markdown inline link behind a private scheme:
//
//	[@<label>](crewship:agent/<agentId>)
//
// The authoritative design rationale lives in `lib/mentions.ts` and the
// user-facing contract in `docs/guides/issue-mentions.mdx`. Two properties of
// that format are load-bearing here:
//
//  1. Only the id is identity. The label is decoration — the chip's name and
//     avatar come from the workspace roster at render time — so this package
//     returns ids and never labels. There is no API here that hands a caller a
//     display name read out of a comment body, because there must not be one.
//
//  2. A mention inside code is not a mention. Documenting the syntax, in an
//     issue comment, in a fenced block or a code span, must not fire an agent
//     run. This is a security property, not a cosmetic one: a trigger will
//     eventually hang off the result of ExtractAgentIDs.
//
// # Why a Markdown parser and not a regexp
//
// The doc guide currently suggests a bare regexp over the raw body:
//
//	regexp.MustCompile(`\[@[^\]\n]{0,80}\]\(crewship:agent/([A-Za-z0-9_-]{1,64})\)`)
//
// That regexp cannot see code, so it contradicts property 2 and the very table
// row in the same guide that promises "documenting this syntax does not fire a
// trigger". `extractMentionAgentIds` in `lib/mentions.ts` has the same hole; the
// frontend gets away with it because the thing that actually renders a chip
// (`components/features/issues/markdown-content.tsx`) walks the *parsed*
// document and only ever converts a link node.
//
// This package therefore does what the renderer does: parse the body with
// goldmark (CommonMark), walk the AST, and consider only `ast.Link` nodes whose
// destination is a well-formed mention URL. Code spans, fenced blocks, indented
// code blocks and raw HTML blocks contain no link nodes, so property 2 falls out
// of the parse instead of being a scanner rule somebody has to keep correct.
//
// goldmark is already in the module graph (glamour depends on it); no new
// third-party dependency was introduced.
//
// # What is guaranteed
//
//   - A returned id matched the strict id class `[A-Za-z0-9_-]{1,64}`, so it can
//     never be a path, a second token, or a URL to somewhere else. It is safe to
//     put in a SQL parameter, a URL path segment or a log line.
//   - Results are de-duplicated and in first-seen (document) order.
//   - Nothing lexically inside a fenced code block (``` or ~~~, any info string,
//     any fence length), an indented code block, an inline code span, or a raw
//     HTML block is ever returned.
//   - An unterminated code fence swallows the rest of the document, exactly as
//     CommonMark says it does. Mentions before the fence are still returned;
//     mentions after it are not. That is the fail-safe direction.
//
// # What is NOT guaranteed, and is deliberately out of scope
//
//   - Resolution. A returned id is a *claim*. It has not been checked against
//     any workspace. A mention of an agent in another workspace is a probe, not
//     a mention: the caller MUST resolve every id inside the comment's own
//     workspace and drop the ones that do not resolve.
//   - Authorization. Parsing says nothing about whether the comment's author may
//     make that agent work. Dispatch under the same authorization an "assign
//     this agent" action would take, never merely because the token parsed.
//   - Persistence and activity emission. Callers should persist the resolved set
//     rather than re-parsing on read.
//
// # Where this is deliberately stricter than the frontend
//
// Where the doc guide's regexp and the frontend renderer disagree, this package
// takes the intersection — it fires only where *both* would. The failure mode is
// "miss a mention", never "fire on documentation":
//
//   - The label must conform to the doc guide's regexp: it must start with `@`,
//     and the remainder must be at most 80 bytes with no `]`, `\n` or `\r`. The
//     renderer would happily chip a link whose text spans a line or contains a
//     bracket; this package will not.
//   - A link whose label cannot be located in the source (an autolink or an
//     HTML entity inside the label, neither of which carries a source position)
//     is rejected rather than guessed at.
//   - An empty label (`[](crewship:agent/…)`) is rejected. The renderer chips it,
//     the doc guide's regexp does not, and a mention nobody can read is not one.
//   - An image (`![@robin](crewship:agent/…)`) is not a mention.
//
// # Where it is deliberately wider than the doc guide's regexp
//
// Both of these are real link nodes, so the reader genuinely sees a chip; the
// regexp missing them is the regexp's bug, not a safety margin:
//
//   - A reference-style link (`[@robin][r]` plus `[r]: crewship:agent/…`).
//     A bare definition with no reference to it produces no link node and so is
//     not a mention.
//   - Whitespace inside the destination parens (`[@robin]( crewship:agent/… )`).
package mentions

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// URLScheme is the private URL scheme a mention's destination must start with.
// Case-sensitive on purpose: `CREWSHIP:AGENT/` is not a thing we ever write, so
// accepting it only widens what has to be reasoned about.
const URLScheme = "crewship:agent/"

// MaxIDLen is the longest agent id a mention can express.
const MaxIDLen = 64

// MaxLabelLen is the longest label (excluding the leading `@`) a mention can
// carry. Taken from the doc guide's regexp, not invented here.
const MaxLabelLen = 80

// mdParser is CommonMark with no extensions. goldmark guards its one-time
// initialisation with a sync.Once and allocates a fresh parse context per call,
// so a package-level parser is safe for concurrent use.
var mdParser = goldmark.New().Parser()

// ValidID reports whether id is expressible as a mention destination.
//
// The class is deliberately narrow — CUIDs are `[a-z0-9]+` and this is a little
// wider so a seeded or prefixed id still round-trips, but not wide enough to
// hold a slash, a dot, a space or a bracket. That is what stops a destination
// from ever being a path (`../../etc/passwd`), a second token, or a URL
// somewhere else.
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > MaxIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ParseURL returns the agent id behind a link destination, and whether the
// destination is a mention at all. It does not resolve the id.
func ParseURL(dest string) (string, bool) {
	if !strings.HasPrefix(dest, URLScheme) {
		return "", false
	}
	id := dest[len(URLScheme):]
	if !ValidID(id) {
		return "", false
	}
	return id, true
}

// ExtractAgentIDs returns every agent id mentioned in a Markdown body,
// de-duplicated and in first-seen order.
//
// The ids are unresolved claims: see the package doc for what the caller still
// owes before any of them may trigger work. Anything inside code is ignored.
func ExtractAgentIDs(body string) []string {
	if body == "" {
		return nil
	}

	src := []byte(body)
	doc := mdParser.Parse(text.NewReader(src))

	var ids []string
	seen := make(map[string]struct{})

	// ast.Walk only returns an error if the callback does; this one never does.
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		id, ok := ParseURL(string(link.Destination))
		if !ok {
			return ast.WalkContinue, nil
		}
		if !labelConforms(link, src) {
			return ast.WalkContinue, nil
		}
		if _, dup := seen[id]; dup {
			return ast.WalkContinue, nil
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		return ast.WalkContinue, nil
	})

	return ids
}

// labelConforms reports whether a link's text, as it was written in the source,
// satisfies the `\[@[^\]\n]{0,80}\]` half of the specified token shape.
//
// The check runs on the raw source span the label occupies rather than on the
// decoded text, so it sees what the doc guide's regexp would have seen. If the
// span cannot be determined the link is rejected — see the package doc.
func labelConforms(link *ast.Link, src []byte) bool {
	start, stop, ok := sourceSpan(link)
	if !ok || start < 0 || stop > len(src) || start >= stop {
		return false
	}
	label := src[start:stop]
	if label[0] != '@' {
		return false
	}
	rest := label[1:]
	if len(rest) > MaxLabelLen {
		return false
	}
	for _, c := range rest {
		if c == ']' || c == '\n' || c == '\r' {
			return false
		}
	}
	return true
}

// sourceSpan returns the half-open source range a node's children occupy.
//
// ok is false when any descendant carries no source position (an autolink, or an
// HTML entity, both of which goldmark materialises without a segment). Callers
// treat that as "cannot verify" and reject, rather than measuring a span that is
// narrower than what was actually written.
func sourceSpan(n ast.Node) (start, stop int, ok bool) {
	child := n.FirstChild()
	if child == nil {
		return 0, 0, false
	}
	start, stop = -1, -1
	for ; child != nil; child = child.NextSibling() {
		s, e, childOK := nodeSpan(child)
		if !childOK {
			return 0, 0, false
		}
		if start < 0 || s < start {
			start = s
		}
		if e > stop {
			stop = e
		}
	}
	return start, stop, start >= 0 && stop > start
}

// nodeSpan returns the source range a single inline node occupies.
func nodeSpan(n ast.Node) (start, stop int, ok bool) {
	switch v := n.(type) {
	case *ast.Text:
		return v.Segment.Start, v.Segment.Stop, true
	case *ast.RawHTML:
		if v.Segments == nil || v.Segments.Len() == 0 {
			return 0, 0, false
		}
		first := v.Segments.At(0)
		last := v.Segments.At(v.Segments.Len() - 1)
		return first.Start, last.Stop, true
	default:
		// Emphasis, code spans, nested links: recurse into the children, which
		// bottom out at Text nodes.
		return sourceSpan(n)
	}
}
