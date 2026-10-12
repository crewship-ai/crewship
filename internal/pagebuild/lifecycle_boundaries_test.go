package pagebuild

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

func artifactDirectory(t *testing.T, s *Store, workspace string) string {
	t.Helper()
	root, err := s.root(workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	return filepath.Join(s.Directory, fmt.Sprintf("%x", sha256.Sum256([]byte(workspace))))
}
func testArtifact() *Artifact {
	return &Artifact{Format: ArtifactFormat, JavaScript: "void 0", Toolchain: "fixture"}
}

func TestArtifactRetentionPreservesRootsUnknownFilesAndOtherWorkspaces(t *testing.T) {
	s := &Store{Directory: t.TempDir()}
	kept, err := s.Put(t.Context(), "workspace", testArtifact())
	if err != nil {
		t.Fatal(err)
	}
	stale := testArtifact()
	stale.JavaScript = "void 1"
	gone, err := s.Put(t.Context(), "workspace", stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), "other", stale); err != nil {
		t.Fatal(err)
	}
	dir := artifactDirectory(t, s, "workspace")
	for _, name := range []string{".staging-abandoned", "operator-note", "invalid.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep unless staging"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".staging-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(t.Context(), "workspace", map[string]bool{kept: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), "workspace", kept); err != nil {
		t.Fatalf("retained root lost: %v", err)
	}
	if _, err := s.Get(t.Context(), "workspace", gone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced artifact survived: %v", err)
	}
	if _, err := s.Get(t.Context(), "other", gone); err != nil {
		t.Fatalf("other workspace damaged: %v", err)
	}
	for _, name := range []string{"operator-note", "invalid.json", ".staging-directory"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("unknown entry removed: %s %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".staging-abandoned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging remains: %v", err)
	}
	if err := s.Prune(t.Context(), "never-created", nil); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactMaintenanceRefusesUnboundedAndCanceledScans(t *testing.T) {
	s := &Store{Directory: t.TempDir()}
	dir := artifactDirectory(t, s, "workspace")
	for i := range 1024 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".staging-%d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune(t.Context(), "workspace", nil); err == nil || !strings.Contains(err.Error(), "directory size") {
		t.Fatalf("unbounded prune=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1024 {
		t.Fatalf("partial prune occurred: %d %v", len(entries), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.StoragePressure(ctx, "workspace"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission=%v", err)
	}
	small := &Store{Directory: t.TempDir()}
	if _, err := small.Put(t.Context(), "workspace", testArtifact()); err != nil {
		t.Fatal(err)
	}
	if err := small.Prune(ctx, "workspace", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prune=%v", err)
	}
}

func TestArtifactStoragePressureUsesWorkspaceCountAndBytes(t *testing.T) {
	for _, limit := range []string{"count", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			s := &Store{Directory: t.TempDir()}
			pressure, err := s.StoragePressure(t.Context(), "workspace")
			if err != nil || pressure {
				t.Fatalf("empty store=%v %v", pressure, err)
			}
			dir := artifactDirectory(t, s, "workspace")
			if limit == "count" {
				for i := range 224 {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("retained-%d", i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				path := filepath.Join(dir, "large-retained")
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, 96<<20); err != nil {
					t.Fatal(err)
				}
			}
			pressure, err = s.StoragePressure(t.Context(), "workspace")
			if err != nil || !pressure {
				t.Fatalf("pressure missing=%v %v", pressure, err)
			}
			pressure, err = s.StoragePressure(t.Context(), "other")
			if err != nil || pressure {
				t.Fatalf("pressure leaked workspace=%v %v", pressure, err)
			}
		})
	}
}

func TestArtifactMaintenanceRejectsUnconfiguredAndUnusableRoots(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Store{{}, {Directory: file}} {
		if err := s.Prune(t.Context(), "workspace", nil); err == nil {
			t.Fatal("invalid prune root accepted")
		}
		if _, err := s.StoragePressure(t.Context(), "workspace"); err == nil {
			t.Fatal("invalid pressure root accepted")
		}
	}
}

func TestBuilderWorkerProtocolAndCleanup(t *testing.T) {
	for _, mode := range []string{"valid", "malformed", "extra artifact", "invalid artifact", "process error", "log overflow", "output overflow", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			artifact := testArtifact()
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

func TestRuntimeHostMatchingRequiresExactAuthority(t *testing.T) {
	for _, tc := range []struct {
		origin, host string
		want         bool
	}{{"https://runtime.example:8443", "RUNTIME.EXAMPLE:8443", true}, {"https://runtime.example:8443", "runtime.example", false}, {"https://runtime.example", "evil.runtime.example", false}, {"://broken", "runtime.example", false}} {
		if got := RuntimeMatchesHost(tc.origin, tc.host); got != tc.want {
			t.Errorf("%q vs %q = %v", tc.origin, tc.host, got)
		}
	}
}

func TestBuilderRejectsInvalidSourceBeforeLaunchingWorker(t *testing.T) {
	for _, bad := range []string{"image", "path", "dependencies"} {
		t.Run(bad, func(t *testing.T) {
			image := "sha256:" + strings.Repeat("a", 64)
			source := pageprofile.Source()
			switch bad {
			case "image":
				image = "node:latest"
			case "path":
				source.Files[0].Path = "../escape"
			case "dependencies":
				for i := range source.Files {
					if source.Files[i].Path == "pnpm-lock.yaml" {
						source.Files[i].Content += "\n# incompatible profile\n"
					}
				}
			}
			result, err := (&DockerBuilder{Image: image}).Build(t.Context(), source)
			if err == nil || result != nil {
				t.Fatalf("invalid %s built: %+v %v", bad, result, err)
			}
		})
	}
}

func TestArtifactStorageValidatesBeforePublicationAndDeduplicates(t *testing.T) {
	s := &Store{Directory: t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Put(ctx, "workspace", testArtifact()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled put=%v", err)
	}
	if _, err := s.Get(ctx, "workspace", strings.Repeat("a", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=%v", err)
	}
	if _, err := s.Get(t.Context(), "workspace", "../escape"); err == nil {
		t.Fatal("unsafe digest accepted")
	}
	malformed := testArtifact()
	malformed.ProfileSHA256 = "not-a-fingerprint"
	if _, err := s.Put(t.Context(), "workspace", malformed); err == nil {
		t.Fatal("malformed profile persisted")
	}
	escaped := testArtifact()
	escaped.JavaScript = strings.Repeat("\x00", MaxArtifactBytes)
	if _, _, err := escaped.Encode(); err == nil || !strings.Contains(err.Error(), "encoded") {
		t.Fatalf("encoded size bound bypassed: %v", err)
	}
	first, err := s.Put(t.Context(), "workspace", testArtifact())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(t.Context(), "workspace", testArtifact())
	if err != nil || second != first {
		t.Fatalf("artifact deduplication=%s %v", second, err)
	}
	entries, err := os.ReadDir(artifactDirectory(t, s, "workspace"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("duplicate store entries=%d %v", len(entries), err)
	}
}

func TestRuntimeOriginRejectsMalformedStudioAndMixedContent(t *testing.T) {
	for _, tc := range []struct{ runtime, studio string }{{"http://127.0.0.1:8080", "https://studio.example"}, {"https://runtime.example", "://invalid"}, {"https://runtime.example", "https://studio.example/private"}} {
		if err := ValidateRuntimeOrigin(tc.runtime, tc.studio); err == nil {
			t.Errorf("unsafe origin pair accepted: %+v", tc)
		}
	}
}
