package runtimestage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactsUnavailableSourcesKeepInstallPaths(t *testing.T) {
	root := t.TempDir()
	sidecar := filepath.Join(root, "absent-sidecar")
	entry := filepath.Join(root, "absent-entrypoint")
	gotSidecar, gotEntry := Artifacts(filepath.Join(root, "data"), sidecar, entry, nil)
	if gotSidecar != sidecar || gotEntry != entry {
		t.Fatalf("failed staging changed paths: %q, %q", gotSidecar, gotEntry)
	}
}

func TestArtifactsUnavailableDestinationKeepsInstallPaths(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "regular-file")
	if err := os.WriteFile(blocked, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	sidecar, entry := Artifacts(blocked, "installed-sidecar", "installed-entrypoint", nil)
	if sidecar != "installed-sidecar" || entry != "installed-entrypoint" {
		t.Fatalf("failed mkdir changed paths: %q, %q", sidecar, entry)
	}
	if b, err := os.ReadFile(blocked); err != nil || string(b) != "keep me" {
		t.Fatalf("existing file damaged: %q, %v", b, err)
	}
}

func TestStageArtifactFailureNeverPublishesPartialCopy(t *testing.T) {
	for _, mode := range []string{"source-directory", "missing-parent", "destination-directory"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "source")
			dst := filepath.Join(root, "destination")
			if mode == "source-directory" {
				if err := os.Mkdir(src, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dst, []byte("previous"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(src, []byte("new artifact"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing-parent" {
				dst = filepath.Join(root, "absent", "destination")
			}
			if mode == "destination-directory" {
				if err := os.Mkdir(dst, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path, err := stageArtifact(src, dst)
			if err == nil || path != "" {
				t.Fatalf("failed copy published: %q, %v", path, err)
			}
			matches, err := filepath.Glob(filepath.Join(root, ".stage-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary copy leaked: %v, %v", matches, err)
			}
			if mode == "source-directory" {
				b, err := os.ReadFile(dst)
				if err != nil || string(b) != "previous" {
					t.Fatalf("previous artifact damaged: %q, %v", b, err)
				}
			}
		})
	}
}
