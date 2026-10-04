package pagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

func TestBuilderWorkerProtocolAndCleanup(t *testing.T) {
	for _, mode := range []string{"valid", "malformed", "extra artifact", "invalid artifact", "process error", "log overflow", "output overflow", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			artifact := &Artifact{Format: ArtifactFormat, JavaScript: "void 0", Toolchain: "fixture"}
			artifact.ProfileSHA256 = pageprofile.Fingerprint()
			encoded, err := json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "malformed":
				encoded = []byte("{broken")
			case "extra artifact":
				encoded = append(encoded, []byte("\n{}")...)
			case "invalid artifact":
				artifact.JavaScript = ""
				encoded, err = json.Marshal(artifact)
				if err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(dir, "output")
			if err := os.WriteFile(output, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
printf '%s\n' "$@" >> "$TEST_PAGE_COMMANDS"
if [ "$1" = run ]; then
 cat > "$TEST_PAGE_INPUT"
 case "$TEST_PAGE_MODE" in
  'process error') printf 'compiler rejected source' >&2; exit 1 ;;
  'log overflow') head -c 70000 /dev/zero >&2; exit 1 ;;
  'output overflow') head -c 9000000 /dev/zero; exit 1 ;;
 esac
 cat "$TEST_PAGE_OUTPUT"
fi
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_PAGE_COMMANDS", filepath.Join(dir, "commands"))
			t.Setenv("TEST_PAGE_INPUT", filepath.Join(dir, "input"))
			t.Setenv("TEST_PAGE_OUTPUT", output)
			t.Setenv("TEST_PAGE_MODE", mode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			image := "sha256:" + strings.Repeat("a", 64)
			result, err := (&DockerBuilder{Image: image}).Build(ctx, pageprofile.Source())
			if mode == "valid" {
				if err != nil || result.Toolchain != image || result.JavaScript != "void 0" {
					t.Fatalf("worker result=%+v %v", result, err)
				}
			} else {
				if err == nil || result != nil {
					t.Fatalf("invalid worker succeeded: %+v %v", result, err)
				}
				if mode == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel cause lost: %v", err)
				}
				if (mode == "log overflow" || mode == "output overflow") && !strings.Contains(err.Error(), "output limit") {
					t.Fatalf("unbounded output diagnosis=%.160s", err)
				}
				if mode == "process error" && !strings.Contains(err.Error(), "compiler rejected source") {
					t.Fatalf("compiler diagnostics lost: %v", err)
				}
			}
			commands, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
			if len(lines) < 3 || lines[len(lines)-3] != "rm" || lines[len(lines)-2] != "--force" || !strings.HasPrefix(lines[len(lines)-1], "crewship-page-build-") {
				t.Fatalf("owned worker not cleaned up: %q", commands)
			}
			if mode != "canceled" {
				var source map[string]any
				input, err := os.ReadFile(filepath.Join(dir, "input"))
				if err != nil || json.Unmarshal(input, &source) != nil || source["files"] == nil {
					t.Fatalf("source not delivered to isolated worker: %s %v", input, err)
				}
				if !strings.Contains(string(commands), "--network=none\n") || !strings.Contains(string(commands), "--pull=never\n") {
					t.Fatal("worker isolation flags lost")
				}
			}
		})
	}
}
