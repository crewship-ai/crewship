// Package shlex provides a minimal, shell-quote-aware field splitter for launch
// lines a user types literally (e.g. the stdio `--command` of an MCP server).
//
// It is deliberately NOT a POSIX shell: quotes group, a backslash outside
// single quotes escapes the next rune, and there is no variable expansion,
// globbing, tilde/brace handling, or operator (|, &&, redirects) support. That
// is exactly the surface a literal launch line needs — enough to keep a quoted
// argument with spaces (or a bare executable at a spaced path) intact, and no
// more.
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
		escaped bool
	)
	for _, r := range raw {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			// Backslash escapes the next rune everywhere except inside single
			// quotes, matching POSIX single-quote semantics.
			escaped = true
			inField = true
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
	if inField || escaped {
		fields = append(fields, cur.String())
	}
	return fields
}
