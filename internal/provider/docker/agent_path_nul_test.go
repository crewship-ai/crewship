package docker

import (
	"strings"
	"testing"
)

// A loginPath persisted with an undemuxed stdcopy frame header (the exact dev1
// corruption) must be sanitized at use so the resulting PATH carries no NUL —
// otherwise runc rejects the container.
func TestApplyAgentLoginPath_StripsPersistedNul(t *testing.T) {
	clean := "/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin"
	corrupt := "\x01\x00\x00\x00\x00\x00\x00�" + clean

	out := applyAgentLoginPath([]string{"FOO=bar"}, corrupt, nil)

	var pathVal string
	for _, e := range out {
		if strings.HasPrefix(e, "PATH=") {
			pathVal = strings.TrimPrefix(e, "PATH=")
		}
	}
	if pathVal == "" {
		t.Fatal("no PATH entry produced")
	}
	if strings.ContainsRune(pathVal, 0x00) {
		t.Fatalf("PATH still contains a NUL byte: %q", pathVal)
	}
	if pathVal != clean {
		t.Fatalf("PATH = %q, want %q", pathVal, clean)
	}
}

func TestSanitizeEnvValue(t *testing.T) {
	if got := sanitizeEnvValue("/usr/bin:/bin"); got != "/usr/bin:/bin" {
		t.Fatalf("clean value altered: %q", got)
	}
	if got := sanitizeEnvValue("\x01\x00a\x07b"); got != "ab" {
		t.Fatalf("control bytes not stripped: %q", got)
	}
	if got := sanitizeEnvValue(""); got != "" {
		t.Fatalf("empty should stay empty, got %q", got)
	}
}
