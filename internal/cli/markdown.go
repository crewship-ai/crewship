package cli

import (
	"bytes"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

// MarkdownRenderer applies lightweight ANSI styling to markdown text.
//
// Two modes:
//   - Streaming: feed chunks via Write; output emerges line-buffered (chunks
//     that span line boundaries are split safely). Inline spans like **bold**
//     and `code` are rendered only when their delimiters appear on the same
//     line, so a delimiter split across chunks never breaks output.
//   - One-shot: call Render(s) to style a complete document at once.
//
// The renderer intentionally implements a *subset* of CommonMark — enough
// for typical agent responses (headings, lists, code fences, bold, italic,
// inline code, links). It is not a full parser. Anything it doesn't
// recognise is passed through unchanged.
type MarkdownRenderer struct {
	// inFence tracks whether we're currently inside a ``` fenced block.
	inFence bool
	// fenceLang remembers the language hint of the current fence (for future use).
	fenceLang string
	// pending holds bytes received since the last newline (carry across chunks).
	pending bytes.Buffer
}

// NewMarkdownRenderer returns a fresh renderer with no buffered state.
func NewMarkdownRenderer() *MarkdownRenderer {
	return &MarkdownRenderer{}
}

// ResolveMarkdown returns whether streamed agent text should be rendered as
// markdown. Order of precedence:
//
//  1. forceOn    (e.g. --markdown flag)              -> true
//  2. forceOff   (e.g. --no-markdown flag, --no-color) -> false
//  3. setting    ("on", "off", "auto", or empty)
//
// "auto" / empty falls back to: render only when stdout is a TTY (so piped
// output stays plain text) AND ANSI colors are not disabled.
func ResolveMarkdown(setting string, forceOn, forceOff, noColor bool) bool {
	if forceOff || noColor {
		return false
	}
	if forceOn {
		return true
	}
	switch strings.ToLower(setting) {
	case "on", "true", "1", "yes":
		return true
	case "off", "false", "0", "no":
		return false
	}
	// auto / empty
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Reset clears any buffered state. Call between independent documents.
func (m *MarkdownRenderer) Reset() {
	m.inFence = false
	m.fenceLang = ""
	m.pending.Reset()
}

// Write consumes a chunk and returns its rendered form.
// Output is line-aligned: bytes after the last '\n' in the chunk are buffered
// internally and emitted on the next Write/Flush.
func (m *MarkdownRenderer) Write(chunk string) string {
	m.pending.WriteString(chunk)
	data := m.pending.Bytes()

	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		// No complete line yet — keep buffering.
		return ""
	}

	completed := string(data[:lastNL+1])
	leftover := append([]byte(nil), data[lastNL+1:]...)
	m.pending.Reset()
	m.pending.Write(leftover)

	return m.renderLines(completed)
}

// Flush returns any leftover buffered text (rendered as-is at line scope).
// Call when the stream ends and the trailing partial line still needs output.
func (m *MarkdownRenderer) Flush() string {
	if m.pending.Len() == 0 {
		return ""
	}
	out := m.renderLines(m.pending.String())
	m.pending.Reset()
	return out
}

// Render styles a complete document in one call. Equivalent to Write+Flush.
func (m *MarkdownRenderer) Render(s string) string {
	m.Reset()
	out := m.Write(s)
	return out + m.Flush()
}

// renderLines walks `text` line-by-line and applies styling.
func (m *MarkdownRenderer) renderLines(text string) string {
	var out strings.Builder
	for _, line := range splitKeepNewline(text) {
		out.WriteString(m.renderLine(line))
	}
	return out.String()
}

func (m *MarkdownRenderer) renderLine(line string) string {
	// Strip the trailing newline (if any) so we can append it after styling.
	nl := ""
	body := line
	if strings.HasSuffix(body, "\n") {
		nl = "\n"
		body = body[:len(body)-1]
	}

	// Fence open/close — pass-through with subtle dim styling.
	trim := strings.TrimSpace(body)
	if strings.HasPrefix(trim, "```") {
		if m.inFence {
			m.inFence = false
			m.fenceLang = ""
		} else {
			m.inFence = true
			m.fenceLang = strings.TrimPrefix(trim, "```")
		}
		return Dim + body + Reset + nl
	}

	if m.inFence {
		// Inside a code fence: subtle gray to distinguish from prose.
		return Gray + body + Reset + nl
	}

	// Heading (only at start of line; up to 6 #).
	if h := headingLevel(body); h > 0 {
		stripped := strings.TrimLeft(body[h:], " ")
		switch h {
		case 1:
			return Bold + Cyan + stripped + Reset + nl
		case 2:
			return Bold + Blue + stripped + Reset + nl
		default:
			return Bold + stripped + Reset + nl
		}
	}

	// List markers — color the bullet, render the rest with inline spans.
	if marker, rest, ok := splitListMarker(body); ok {
		return Yellow + marker + Reset + " " + renderInline(rest) + nl
	}

	// Block quote.
	if strings.HasPrefix(body, "> ") {
		return Dim + "│ " + Reset + renderInline(body[2:]) + nl
	}

	return renderInline(body) + nl
}

// headingLevel returns 1..6 if `body` starts with that many '#' followed by a space,
// 0 otherwise.
func headingLevel(body string) int {
	n := 0
	for n < 6 && n < len(body) && body[n] == '#' {
		n++
	}
	if n == 0 {
		return 0
	}
	if n >= len(body) || body[n] != ' ' {
		return 0
	}
	return n
}

// splitListMarker returns the marker and rest if `body` begins with one.
// Recognised: "- ", "* ", "+ ", and "<digits>. ".
var orderedListRe = regexp.MustCompile(`^(\s*)(\d+)\.\s`)

func splitListMarker(body string) (marker, rest string, ok bool) {
	// Allow up to 4 leading spaces of indentation.
	leading := 0
	for leading < len(body) && leading < 4 && body[leading] == ' ' {
		leading++
	}
	stripped := body[leading:]

	if len(stripped) >= 2 {
		c := stripped[0]
		if (c == '-' || c == '*' || c == '+') && stripped[1] == ' ' {
			return body[:leading] + string(c), stripped[2:], true
		}
	}
	if loc := orderedListRe.FindStringSubmatchIndex(body); loc != nil {
		// loc[0]:loc[1] = whole match; the marker is up to the space.
		end := loc[1]
		return body[:end-1], body[end:], true
	}
	return "", "", false
}

// renderInline applies bold/italic/inline-code/link styling on a single line.
// Crucially, all delimiters must appear on the SAME line — we never look across
// line boundaries — which is what makes streaming safe.
var (
	boldRe       = regexp.MustCompile(`\*\*([^\*\n]+?)\*\*`)
	italicRe     = regexp.MustCompile(`(^|\W)\*([^\*\n]+?)\*(\W|$)`)
	inlineCodeRe = regexp.MustCompile("`([^`\n]+)`")
	linkRe       = regexp.MustCompile(`\[([^\]\n]+)\]\(([^\)\n]+)\)`)
)

func renderInline(s string) string {
	s = inlineCodeRe.ReplaceAllString(s, Gray+"`$1`"+Reset)
	s = boldRe.ReplaceAllString(s, Bold+"$1"+Reset)
	s = italicRe.ReplaceAllString(s, "$1"+Dim+"$2"+Reset+"$3")
	s = linkRe.ReplaceAllString(s, Cyan+"$1"+Reset+Dim+" ($2)"+Reset)
	return s
}

// splitKeepNewline splits on '\n' but keeps the newline at the end of each
// element. The final element has no newline if the input doesn't end with one.
func splitKeepNewline(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			if s != "" {
				out = append(out, s)
			}
			return out
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
}
