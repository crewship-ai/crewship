package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This controlled executable probes process isolation only. Actual engine
// semantics are exercised by the separately tagged original-CLI qualification.
func nativeAgentProcessProbe(t *testing.T, body string) (CraftNativeDocumentProfile, string) {
	t.Helper()
	p := craftNativeProfileFixture(t)
	marker := filepath.Join(t.TempDir(), "working-directory")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	script := `#!/bin/sh
set -eu
[ "$0" = /proc/self/fd/3 ]
[ "${CRAFT_AGENT_SECRET_SENTINEL:-}" = "" ]
[ "$HOME" = "$PWD" ]
[ "$TMPDIR" = "$PWD" ]
[ "$LANG" = C.UTF-8 ]
[ "$(/usr/bin/stat -c %a "$PWD")" = 700 ]
if [ "$1" = convert ]; then
 /bin/cp "$2" "$3"
 printf 'wrote %s\n' "$3" >&2
 exit 0
fi
[ "$1" = run ]
[ "$(/usr/bin/stat -c %a "$3")" = 600 ]
printf '%s' "$PWD" > ` + quoted + `
for last in "$@"; do :; done
` + body
	bytes := []byte(script)
	if err := os.WriteFile(p.CLIPath, bytes, 0700); err != nil {
		t.Fatal(err)
	}
	p.CLISHA256 = fmt.Sprintf("%x", sha256.Sum256(bytes))
	return p, marker
}

func TestCraftNativeAgentCLIPrivateProcessAndLargeNativeBytes(t *testing.T) {
	// CRAFT_AGENT_SECRET_SENTINEL is a test-only environment isolation canary.
	t.Setenv("CRAFT_AGENT_SECRET_SENTINEL", "must-never-reach-engine")
	p, marker := nativeAgentProcessProbe(t, "/bin/cp \"$3\" \"$last\"\nprintf 'wrote %s\\n' \"$last\" >&2\n")
	// Native payloads use their 32MiB contract, not the protocol's 1MiB JCS
	// envelope limit. The adapter does not rewrite native integer spellings.
	raw := []byte(`{"body":[],"retained":9223372036854775807,"large":"` + strings.Repeat("x", (1<<20)+10) + `"}`)
	a := NewCraftNativeAgentCLIAdapter()
	got, err := a.Apply(context.Background(), p, raw, json.RawMessage(`[{"command":"text.insert","params":{"text":"literal $(not executed)"}}]`))
	if err != nil || string(got) != string(raw) {
		t.Fatalf("private FD execution lost bytes or failed: %v", err)
	}
	dir, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(dir)); !os.IsNotExist(err) {
		t.Fatal("private native document directory survived the operation")
	}
}

func TestCraftNativeAgentCLIProcessFailuresAreClosed(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"stdout overflow", "exec /usr/bin/head -c 2097152 /dev/zero\n"},
		{"stderr overflow", "exec /usr/bin/head -c 131072 /dev/zero >&2\n"},
		{"unexpected warning", "/bin/cp \"$3\" \"$last\"\nprintf 'wrote %s\\nwarning\\n' \"$last\" >&2\n"},
		{"symlink output", "/bin/ln -s \"$3\" \"$last\"\nprintf 'wrote %s\\n' \"$last\" >&2\n"},
		{"timeout", "exec /bin/sleep 5\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, marker := nativeAgentProcessProbe(t, test.body)
			a := NewCraftNativeAgentCLIAdapter()
			a.timeout = 2 * time.Second
			if test.name == "timeout" {
				a.timeout = 500 * time.Millisecond
			}
			started := time.Now()
			_, err := a.Apply(context.Background(), p, []byte(`{"body":[]}`), json.RawMessage(`[{"command":"text.insert","params":{"text":"x"}}]`))
			if err == nil || time.Since(started) > 3*time.Second {
				t.Fatalf("unbounded or failed process accepted: %v", err)
			}
			dir, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal("probe did not reach the intended engine operation")
			}
			if _, err := os.Stat(string(dir)); !os.IsNotExist(err) {
				t.Fatal("failed operation retained private files")
			}
		})
	}
}
