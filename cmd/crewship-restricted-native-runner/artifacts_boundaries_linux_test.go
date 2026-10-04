//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestArtifactAggregateSizeBoundary(t *testing.T) {
	for _, count := range []int{4, 5} {
		t.Run(fmt.Sprintf("%d_megabytes", count), func(t *testing.T) {
			root := t.TempDir()
			content := bytes.Repeat([]byte{0xff}, 1<<20)
			for i := range count {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("result-%d", i)), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			files, err := collectArtifacts(root)
			if count == 5 {
				if err == nil {
					t.Fatal("accepted artifacts exceeding the aggregate size limit")
				}
				return
			}
			if err != nil || len(files) != count {
				t.Fatalf("exact aggregate limit: count=%d err=%v", len(files), err)
			}
			for _, file := range files {
				if !bytes.Equal(file.Content, content) {
					t.Fatalf("artifact %q was truncated at the allowed size", file.Name)
				}
			}
		})
	}
}

func TestArtifactNameAndCountBoundaries(t *testing.T) {
	t.Run("32 files accepted", func(t *testing.T) {
		root := t.TempDir()
		for i := range 32 {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("result-%02d", i)), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		files, err := collectArtifacts(root)
		if err != nil || len(files) != 32 {
			t.Fatalf("exact file count limit: count=%d err=%v", len(files), err)
		}
	})
	for _, length := range []int{240, 241} {
		t.Run(fmt.Sprintf("name_length_%d", length), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", length)), nil, 0600); err != nil {
				t.Fatal(err)
			}
			files, err := collectArtifacts(root)
			if length == 241 {
				if err == nil {
					t.Fatal("accepted an overlong artifact name")
				}
			} else if err != nil || len(files) != 1 || len(files[0].Name) != length {
				t.Fatalf("exact name limit: files=%v err=%v", files, err)
			}
		})
	}
}

func TestArtifactEmissionRejectsInvalidTreeBeforeWriting(t *testing.T) {
	for _, kind := range []string{"missing root", "fifo", "directory symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "a-valid"), []byte("result"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing root":
				root = filepath.Join(root, "missing")
			case "fifo":
				if err := syscall.Mkfifo(filepath.Join(root, "z-fifo"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "z-foreign")); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			if err := emitArtifacts(&output, root); err == nil {
				t.Fatal("invalid artifact tree was emitted")
			}
			if output.Len() != 0 {
				t.Fatalf("emitted %d bytes before validating the entire artifact tree", output.Len())
			}
		})
	}
}

type closedArtifactWriter struct{}

func (closedArtifactWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestArtifactEmissionWireFormatAndWriterFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte{0, 0xff, '\n', 'x'}
	if err := os.WriteFile(filepath.Join(root, "nested", "result.bin"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".private"), []byte("not exported"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := emitArtifacts(&output, root); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var got artifact
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "artifact" || got.Name != "nested/result.bin" || !bytes.Equal(got.Content, content) {
		t.Fatalf("artifact did not survive the JSON wire format: %+v", got)
	}
	if err := decoder.Decode(new(artifact)); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected extra output: %v", err)
	}
	if err := emitArtifacts(closedArtifactWriter{}, root); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost output writer failure: %v", err)
	}
}
