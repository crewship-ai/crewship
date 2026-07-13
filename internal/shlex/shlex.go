// Package shlex provides a minimal, shell-quote-aware field splitter for launch
// lines a user types literally (e.g. the stdio `--command` of an MCP server).
//
// It is deliberately NOT a POSIX shell: quotes group, and there is no variable
// expansion, globbing, tilde/brace handling, or operator (|, &&, redirects)
// support. That is exactly the surface a literal launch line needs — enough to
// keep a quoted argument with spaces (or a bare executable at a spaced path)
// intact, and no more.
//
// Backslash handling is deliberately narrow, NOT full POSIX, because launch
// lines routinely carry Windows paths (`C:\Program Files\nodejs\npx.exe`)
// where a backslash is a path separator, not an escape:
//
//   - Unquoted: `\` escapes only when followed by space, tab, `"`, `'`, or
//     `\` itself (so `a\ b` is one field "a b", but `C:\npx.exe` survives
//     with its backslash intact).
//   - Inside double quotes: `\` escapes only `"` or `\` (so
//     `"C:\Program Files\nodejs\npx.exe"` survives intact, but `\"` still
//     lets a literal quote appear inside the field).
//   - Inside single quotes: everything is literal, including `\`.
//   - A trailing lone backslash (no following rune) is kept as a literal
//     backslash rather than being treated as an error.
package shlex

import "strings"

// Fields splits raw into fields, honoring single and double quotes.
//
//	Fields(`npx -y "@scope/pkg with space"`) => [npx -y @scope/pkg with space]
//	Fields(`"/opt/my app/bin/server"`)       => [/opt/my app/bin/server]
//
// strings.Fields, by contrast, is whitespace-only and shreds both. An
// explicitly empty quoted field (`""`) is preserved so an intentional empty
// argument is not silently dropped. An unterminated quote consumes the rest of
// the input as one field.
func Fields(raw string) []string {
	var (
		fields  []string
		cur     strings.Builder
		inField bool
		quote   rune // 0 when unquoted, else '\'' or '"'
	)
	runes := []rune(raw)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\\' && quote == 0:
			// Unquoted backslash: only an escape when followed by space, tab,
			// a quote rune, or another backslash. Anything else (notably a
			// Windows path separator like `C:\npx.exe`) is literal.
			if next, ok := peek(runes, i); ok && isEscapable(next) {
				cur.WriteRune(next)
				i++
			} else {
				cur.WriteRune(r)
			}
			inField = true
		case r == '\\' && quote == '"':
			// Inside double quotes: only `"` and `\` are escapable, so a
			// Windows path survives ("C:\Program Files\...") while `\"`
			// still lets a literal quote appear inside the field.
			if next, ok := peek(runes, i); ok && (next == '"' || next == '\\') {
				cur.WriteRune(next)
				i++
			} else {
				cur.WriteRune(r)
			}
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inField = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inField {
				fields = append(fields, cur.String())
				cur.Reset()
				inField = false
			}
		default:
			cur.WriteRune(r)
			inField = true
		}
	}
	if inField {
		fields = append(fields, cur.String())
	}
	return fields
}

// peek returns the rune following index i, if any.
func peek(runes []rune, i int) (rune, bool) {
	if i+1 < len(runes) {
		return runes[i+1], true
	}
	return 0, false
}

// isEscapable reports whether r is one of the runes an unquoted backslash may
// escape (space, tab, single quote, double quote, or another backslash).
func isEscapable(r rune) bool {
	return r == ' ' || r == '\t' || r == '\'' || r == '"' || r == '\\'
}
