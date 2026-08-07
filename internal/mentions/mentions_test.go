package mentions

import (
	"strings"
	"testing"
)

// The id the frontend's own mention tests use, so the two suites are provably
// talking about the same token. See
// components/features/issues/__tests__/mention-rendering.test.tsx.
const robin = "cmt20ikph011ab4683c02"

const (
	nova  = "agt_nova-2"
	quill = "cmt99zzz000000000000q"
)

// tok writes the token the composer would have written.
func tok(label, id string) string {
	return "[@" + label + "](" + URLScheme + id + ")"
}

func TestExtractAgentIDs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		/* ---------------------------------------------------------------- *
		 *  The happy path                                                    *
		 * ---------------------------------------------------------------- */
		{
			name: "empty body",
			body: "",
			want: nil,
		},
		{
			name: "body with no mentions",
			body: "the CSV job is failing on empty rows",
			want: nil,
		},
		{
			// Frontend pin: "does not turn text that merely looks like a
			// mention into a chip".
			name: "bare @handle and an email address are not mentions",
			body: "@robin please look, and mail pavel@unify.cz",
			want: nil,
		},
		{
			// Frontend pin: "renders a stored mention as a chip".
			name: "a plain mention",
			body: "please pick this up " + tok("robin", robin),
			want: []string{robin},
		},
		{
			name: "several mentions keep document order",
			body: tok("nova", nova) + " and " + tok("robin", robin) + " and " + tok("quill", quill),
			want: []string{nova, robin, quill},
		},
		{
			name: "the same agent twice is returned once, at its first position",
			body: tok("robin", robin) + " ping " + tok("nova", nova) + " and again " + tok("robin", robin),
			want: []string{robin, nova},
		},
		{
			// Frontend pin: "takes the name from the roster, never from the
			// body". The label is decoration — we return the id it hides
			// behind and nothing else.
			name: "a lying label still yields only the id",
			body: tok("head-of-security", robin) + " approve this",
			want: []string{robin},
		},
		{
			// Frontend pin: "degrades an unresolved id to plain text". Parsing
			// says nothing about resolution — that is the caller's job — so a
			// well-formed id for a nonexistent agent is still extracted here.
			name: "a well-formed id that resolves to nobody is still parsed out",
			body: tok("ceo", "agt_does_not_exist") + " sign off",
			want: []string{"agt_does_not_exist"},
		},
		{
			name: "mentions inside a list, a blockquote and a heading are found",
			body: "# " + tok("robin", robin) + "\n\n- " + tok("nova", nova) + "\n\n> " + tok("quill", quill),
			want: []string{robin, nova, quill},
		},

		/* ---------------------------------------------------------------- *
		 *  Code — the property that makes this a parser and not a regexp     *
		 * ---------------------------------------------------------------- */
		{
			// Frontend pin: "leaves a mention inside code alone — documenting
			// the syntax is not a trigger".
			name: "inline code span",
			body: "write it as `" + tok("robin", robin) + "`",
			want: nil,
		},
		{
			name: "inline code span delimited by a run of backticks",
			body: "write it as ``" + tok("robin", robin) + " with a ` in it``",
			want: nil,
		},
		{
			name: "fenced block",
			body: "the format is:\n\n```\n" + tok("robin", robin) + "\n```\n",
			want: nil,
		},
		{
			name: "fenced block with an info string",
			body: "```markdown\nover to you " + tok("robin", robin) + "\n```\n",
			want: nil,
		},
		{
			name: "fence longer than three backticks",
			body: "`````md\n```\n" + tok("robin", robin) + "\n```\n`````\n",
			want: nil,
		},
		{
			name: "tilde fence",
			body: "~~~\n" + tok("robin", robin) + "\n~~~\n",
			want: nil,
		},
		{
			name: "tilde fence with an info string",
			body: "~~~~ markdown\n" + tok("robin", robin) + "\n~~~~\n",
			want: nil,
		},
		{
			name: "indented code block",
			body: "the format is:\n\n    " + tok("robin", robin) + "\n",
			want: nil,
		},
		{
			// The documentation case in its purest form: the whole body is a
			// fence whose contents are exactly a valid mention. This must never
			// fire. It is the case the doc guide's regexp gets wrong.
			name: "a body that is nothing but a fenced block containing a mention",
			body: "```\n" + tok("robin", robin) + "\n```",
			want: nil,
		},
		{
			name: "an unterminated fence swallows the rest of the document",
			body: "ping " + tok("nova", nova) + "\n\n```\n" + tok("robin", robin) + "\n" + tok("quill", quill) + "\n",
			want: []string{nova},
		},
		{
			name: "a real mention beside a documented one is still found",
			body: "over to you " + tok("robin", robin) + "\n\n```\n" + tok("nova", nova) + "\n```\n",
			want: []string{robin},
		},
		{
			name: "a mention inside a raw HTML block is not a mention",
			body: "<div>\n" + tok("robin", robin) + "\n</div>\n",
			want: nil,
		},

		/* ---------------------------------------------------------------- *
		 *  Malformed destinations                                            *
		 * ---------------------------------------------------------------- */
		{
			name: "an id containing a slash is not an id",
			body: "[@robin](crewship:agent/../../etc/passwd)",
			want: nil,
		},
		{
			name: "an id containing a dot is not an id",
			body: "[@robin](crewship:agent/robin.exe)",
			want: nil,
		},
		{
			name: "an empty id is not an id",
			body: "[@robin](crewship:agent/)",
			want: nil,
		},
		{
			name: "an id of exactly 64 characters is accepted",
			body: tok("robin", strings.Repeat("a", 64)),
			want: []string{strings.Repeat("a", 64)},
		},
		{
			name: "an id of 65 characters is not",
			body: tok("robin", strings.Repeat("a", 65)),
			want: nil,
		},
		{
			name: "the scheme is case-sensitive",
			body: "[@robin](CREWSHIP:AGENT/" + robin + ")",
			want: nil,
		},
		{
			name: "another scheme carrying the same path is not a mention",
			body: "[@robin](https://evil.example/crewship:agent/" + robin + ")",
			want: nil,
		},

		/* ---------------------------------------------------------------- *
		 *  Labels — pinned to the doc guide's `\[@[^\]\n]{0,80}\]`           *
		 * ---------------------------------------------------------------- */
		{
			name: "a label may contain a second @",
			body: tok("ro@bin", robin),
			want: []string{robin},
		},
		{
			name: "a label may contain dots, dashes and underscores",
			body: tok("head_of.security-2", robin),
			want: []string{robin},
		},
		{
			name: "a label of exactly 80 characters after the @ is accepted",
			body: tok(strings.Repeat("x", 80), robin),
			want: []string{robin},
		},
		{
			name: "a label of 81 characters is not",
			body: tok(strings.Repeat("x", 81), robin),
			want: nil,
		},
		{
			// CommonMark would make this a link (the brackets balance) and the
			// renderer would chip it; the doc guide's regexp would not match.
			// We take the narrower reading — see the package doc.
			name: "a label containing a bracket pair is not a mention",
			body: "[@ro[b]in](" + URLScheme + robin + ")",
			want: nil,
		},
		{
			// Same conflict, same resolution: a link text may span lines in
			// CommonMark, the regexp's `[^\]\n]` says it may not.
			name: "a label spanning a newline is not a mention",
			body: "[@ro\nbin](" + URLScheme + robin + ")",
			want: nil,
		},
		{
			name: "an empty label is not a mention",
			body: "[](" + URLScheme + robin + ")",
			want: nil,
		},
		{
			name: "a label that does not start with @ is not a mention",
			body: "[robin](" + URLScheme + robin + ")",
			want: nil,
		},

		/* ---------------------------------------------------------------- *
		 *  Shapes that could confuse a naive scanner                         *
		 * ---------------------------------------------------------------- */
		{
			name: "a token split between the label and the destination",
			body: "[@robin]\n(" + URLScheme + robin + ")",
			want: nil,
		},
		{
			name: "a token with a space between the label and the destination",
			body: "[@robin] (" + URLScheme + robin + ")",
			want: nil,
		},
		{
			name: "a destination split across a newline",
			body: "[@robin](" + URLScheme + "\n" + robin + ")",
			want: nil,
		},
		{
			name: "an escaped opening bracket is not a link",
			body: `\[@robin](` + URLScheme + robin + `)`,
			want: nil,
		},
		{
			name: "an image is not a mention",
			body: "![@robin](" + URLScheme + robin + ")",
			want: nil,
		},
		{
			name: "a mention wrapped in a further bracket pair",
			body: "[" + tok("robin", robin) + "]",
			want: []string{robin},
		},
		{
			name: "two tokens back to back",
			body: tok("robin", robin) + tok("nova", nova),
			want: []string{robin, nova},
		},
		{
			name: "a bare reference definition mentions nobody",
			body: "[r]: " + URLScheme + robin + "\n",
			want: nil,
		},
		{
			// Wider than the doc guide's regexp, but it is a real link node and
			// the renderer really does draw a chip for it — see the package doc.
			name: "a reference-style mention is a mention",
			body: "over to you [@robin][r]\n\n[r]: " + URLScheme + robin + "\n",
			want: []string{robin},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAgentIDs(tt.body)
			if !equal(got, tt.want) {
				t.Fatalf("ExtractAgentIDs(%q)\n got: %#v\nwant: %#v", tt.body, got, tt.want)
			}
		})
	}
}

// A mention must never carry a display name out of the body. The only string
// this package hands back is an id, and an id cannot hold the characters a
// spoofed name would need.
func TestExtractAgentIDsNeverReturnsALabel(t *testing.T) {
	body := tok("head-of-security", robin) + " " + tok("Robin (verified)", nova)
	for _, id := range ExtractAgentIDs(body) {
		if !ValidID(id) {
			t.Fatalf("ExtractAgentIDs returned a non-id: %q", id)
		}
		if strings.Contains(id, "security") || strings.Contains(id, "verified") {
			t.Fatalf("ExtractAgentIDs leaked label text as identity: %q", id)
		}
	}
}

func TestParseURL(t *testing.T) {
	tests := []struct {
		name string
		dest string
		want string
	}{
		{"a mention destination", URLScheme + robin, robin},
		{"underscored id", URLScheme + "agt_does_not_exist", "agt_does_not_exist"},
		{"no scheme", robin, ""},
		{"wrong scheme", "crewship:crew/" + robin, ""},
		{"uppercase scheme", "CREWSHIP:AGENT/" + robin, ""},
		{"scheme with no id", URLScheme, ""},
		{"path traversal", URLScheme + "../../etc/passwd", ""},
		{"trailing query", URLScheme + robin + "?x=1", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseURL(tt.dest)
			if tt.want == "" {
				if ok || got != "" {
					t.Fatalf("ParseURL(%q) = %q, %v; want \"\", false", tt.dest, got, ok)
				}
				return
			}
			if !ok || got != tt.want {
				t.Fatalf("ParseURL(%q) = %q, %v; want %q, true", tt.dest, got, ok, tt.want)
			}
		})
	}
}

func TestValidID(t *testing.T) {
	valid := []string{robin, "a", "A", "0", "agt_nova-2", strings.Repeat("z", MaxIDLen)}
	invalid := []string{
		"", strings.Repeat("z", MaxIDLen+1),
		"has space", "has/slash", "has.dot", "has]bracket", "has:colon",
		"has%2Fescape", "has\nnewline", "café",
	}
	for _, id := range valid {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true, want false", id)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
