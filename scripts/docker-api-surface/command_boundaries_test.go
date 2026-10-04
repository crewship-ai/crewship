package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandListsDerivedSurfaceAndChecksRealDeclaration(t *testing.T) {
	for _, list := range []bool{true, false} {
		t.Run(map[bool]string{true: "list", false: "check"}[list], func(t *testing.T) {
			output, err := os.CreateTemp(t.TempDir(), "surface-output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			oldArgs, oldFlags, oldOut := os.Args, flag.CommandLine, os.Stdout
			defer func() { os.Args, flag.CommandLine, os.Stdout = oldArgs, oldFlags, oldOut }()
			flag.CommandLine = flag.NewFlagSet("surface-test", flag.ContinueOnError)
			os.Args = []string{"docker-api-surface", "-root", repoRoot}
			if list {
				os.Args = append(os.Args, "-list")
			}
			os.Stdout = output
			main()
			if _, err := output.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(output)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if list {
				for _, want := range []string{"packages importing the Docker SDK", "SDK methods reached", "call sites", "daemon-pinned CLI shellouts", "ContainerCreate", "internal/provider/docker"} {
					if !strings.Contains(text, want) {
						t.Fatalf("list missing %q: %s", want, text)
					}
				}
				if strings.Index(text, "ContainerCreate") > strings.Index(text, "ContainerStats") {
					t.Fatal("method list is not sorted")
				}
			} else if !strings.Contains(text, "docs and compose in sync") {
				t.Fatalf("check output: %s", text)
			}
		})
	}
}

func TestScanRejectsMalformedImportsAndBodies(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"imports", "package broken\nimport ( \"unterminated"},
		{"body", "package broken\nimport \"github.com/moby/moby/client\"\nfunc broken( {"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeGo(t, root, "internal/broken/broken.go", tc.source)
			if _, err := Scan(root); err == nil || !strings.Contains(err.Error(), "internal/broken/broken.go") {
				t.Fatalf("malformed source accepted: %v", err)
			}
		})
	}
}

func TestMissingDeploymentDocumentationFailsActionably(t *testing.T) {
	root := t.TempDir()
	if got := checkCompose(root); !mentions(got, "cannot read the supported deployment") {
		t.Fatalf("missing compose: %v", got)
	}
	if got := checkDocs(root); len(got) == 0 {
		t.Fatal("missing documentation accepted")
	}
	path := filepath.Join(root, composePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("services:\n  unrelated:\n    image: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := checkCompose(root); len(got) == 0 || !mentions(got, composeService) {
		t.Fatalf("missing proxy service: %v", got)
	}
}
