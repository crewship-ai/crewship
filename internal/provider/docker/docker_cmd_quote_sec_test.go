package docker

import (
	"strings"
	"testing"
)

// These tests pin the shell-quoting behaviour used when EnsureCrewRuntime
// assembles the root-owned init container's `sh -c "..."` chown command.
// Crew IDs are server-generated CUIDs today, but the command string is still
// built by interpolating filesystem paths derived from them; shellQuote makes
// that robust against any future where a path component becomes user-influenced.

func TestSecDockerCmdShellQuoteNormalPath(t *testing.T) {
	// A normal CUID-style path must round-trip to the same literal value
	// (wrapped in single quotes, nothing escaped, meaning unchanged).
	got := shellQuote("/crew/abc123")
	want := "'/crew/abc123'"
	if got != want {
		t.Fatalf("shellQuote(normal) = %q, want %q", got, want)
	}
}

func TestSecDockerCmdShellQuoteNeutralizesMetachars(t *testing.T) {
	cases := []string{
		"/crew/abc; rm -rf /",
		"/crew/$(id)",
		"/crew/`id`",
		"/crew/a b c",
		"/crew/a&&b",
		"/crew/a|b",
		"/crew/a>b",
		"/crew/x'y", // embedded single quote — the tricky one
	}
	for _, in := range cases {
		q := shellQuote(in)
		// Must be wrapped in single quotes.
		if !strings.HasPrefix(q, "'") || !strings.HasSuffix(q, "'") {
			t.Errorf("shellQuote(%q) = %q: not single-quote wrapped", in, q)
			continue
		}
		// Inside a single-quoted string, the only special character is the
		// single quote itself, which must be expressed as the '\'' idiom.
		// Strip the leading/trailing wrapping quote, then assert no bare
		// single quote survives except as part of that idiom.
		inner := q[1 : len(q)-1]
		stripped := strings.ReplaceAll(inner, `'\''`, "")
		if strings.Contains(stripped, "'") {
			t.Errorf("shellQuote(%q) = %q: leaves an unescaped single quote", in, q)
		}
		// The dangerous metacharacters must not appear outside the quotes:
		// since the whole payload is inside single quotes (modulo the '\''
		// idiom), no metachar can reach the shell as syntax.
		for _, meta := range []string{";", "$", "`", "&", "|", ">", " "} {
			if meta == " " { // spaces are fine inside single quotes
				continue
			}
			// Reconstruct what the shell would parse outside quoted regions.
			outside := metacharsOutsideQuotes(q)
			if strings.Contains(outside, meta) {
				t.Errorf("shellQuote(%q) = %q: metachar %q reaches shell syntax", in, q, meta)
			}
		}
	}
}

func TestSecDockerCmdBuildChownInitCmd(t *testing.T) {
	// The assembled command must keep its structure (chown -R, the find
	// invocations, the && / ; sequencing) but every interpolated path must
	// be single-quoted. A path carrying an injection payload must NOT create
	// new shell syntax.
	dirs := []string{"/out/x", "/out/x/.memory"}
	crewPath := "/crew/abc; rm -rf /"
	cmd := buildChownInitCmd(dirs, crewPath)

	if !strings.Contains(cmd, "chown -R 1001:1001") {
		t.Fatalf("buildChownInitCmd missing chown: %q", cmd)
	}
	if !strings.Contains(cmd, "-name .memory") {
		t.Fatalf("buildChownInitCmd missing .memory find: %q", cmd)
	}
	// The crewPath payload must be quoted, not raw. It is interpolated as
	// "/mnt"+crewPath, so the quoted form carries the /mnt prefix.
	if !strings.Contains(cmd, `'/mnt/crew/abc; rm -rf /'`) {
		t.Fatalf("buildChownInitCmd interpolated crewPath unquoted: %q", cmd)
	}
	// No bare injection: the `; rm -rf /` must only ever appear inside a
	// single-quoted region.
	if strings.Contains(metacharsOutsideQuotes(cmd), "rm -rf") {
		t.Fatalf("buildChownInitCmd lets injection reach shell syntax: %q", cmd)
	}
	// Each dir must be quoted in the chown prefix.
	for _, d := range dirs {
		if !strings.Contains(cmd, shellQuote("/mnt"+d)) {
			t.Fatalf("buildChownInitCmd dir %q not quoted in: %q", d, cmd)
		}
	}
}

// metacharsOutsideQuotes returns the parts of s that are OUTSIDE single-quoted
// regions, i.e. what the shell would interpret as syntax. Single quotes toggle
// quoting; everything between a matched pair is treated as literal data.
func metacharsOutsideQuotes(s string) string {
	var b strings.Builder
	inQuote := false
	for _, r := range s {
		if r == '\'' {
			inQuote = !inQuote
			continue
		}
		if !inQuote {
			b.WriteRune(r)
		}
	}
	return b.String()
}
