package main

import (
	"crypto/sha256"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func licenseFixture(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module "+selfModule+"\ngo 1.24\nrequire github.com/mattn/go-localereader "+version+"\nreplace github.com/mattn/go-localereader => ./dependency\n")
	write("dependency/go.mod", "module github.com/mattn/go-localereader\ngo 1.24\n")
	write("dependency/library.go", "package localereader\nconst Fixture = true\n")
	for _, name := range []string{"cmd/crewship/main.go", "cmd/crewship-sidecar/main.go"} {
		write(name, "package main\nimport _ \"github.com/mattn/go-localereader\"\nfunc main() {}\n")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "")
	t.Chdir(root)
	return root
}

func readLicenseManifest(t *testing.T, root string) [][]string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "manifest.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := csv.NewReader(f)
	reader.Comma = '\t'
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestLicenseBundleCopiesTextsAndAuditsBuildMatrix(t *testing.T) {
	root := licenseFixture(t, "v0.0.2")
	text := []byte("Fixture license text\nCopyright fixture contributors\n")
	if err := os.WriteFile(filepath.Join(root, "dependency/LICENSE"), text, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dependency/NOTICE"), 0700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	previousArgs, previousFlags := os.Args, flag.CommandLine
	defer func() { os.Args = previousArgs; flag.CommandLine = previousFlags }()
	os.Args = []string{"gen-licenses", "-out", out}
	flag.CommandLine = flag.NewFlagSet("gen-licenses", flag.ContinueOnError)
	main()
	rows := readLicenseManifest(t, out)
	if len(rows) != 2 {
		t.Fatalf("manifest rows: %#v", rows)
	}
	row := rows[1]
	if row[0] != "github.com/mattn/go-localereader" || row[1] != "v0.0.2" || row[2] != "LICENSE" || row[4] != fmt.Sprintf("%x", sha256.Sum256(text)) {
		t.Fatalf("manifest identity/digest changed: %#v", row)
	}
	cells := strings.Split(row[3], ",")
	if len(cells) != 12 {
		t.Fatalf("lost or duplicate matrix provenance: %v", cells)
	}
	for _, cell := range []string{"linux/amd64", "linux/arm64+clionly", "darwin/amd64", "windows/arm64+clionly"} {
		if !strings.Contains(","+row[3]+",", ","+cell+",") {
			t.Fatalf("missing provenance %s: %s", cell, row[3])
		}
	}
	copied := filepath.Join(out, row[0], row[2])
	got, err := os.ReadFile(copied)
	if err != nil || string(got) != string(text) {
		t.Fatalf("changed license bytes: %q %v", got, err)
	}
	info, err := os.Stat(copied)
	if err != nil || info.Mode().Perm()&0200 == 0 {
		t.Fatalf("output cannot be regenerated: %v %v", info, err)
	}
	if err := run(out); err != nil {
		t.Fatalf("regeneration failed: %v", err)
	}
}

func TestLicenseBundleExceptionIsScopedToReviewedVersion(t *testing.T) {
	for _, version := range []string{"v0.0.1", "v0.0.2"} {
		t.Run(version, func(t *testing.T) {
			licenseFixture(t, version)
			out := t.TempDir()
			err := run(out)
			if version == "v0.0.2" {
				if err == nil || !strings.Contains(err.Error(), "no license text") {
					t.Fatalf("unreviewed version accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rows := readLicenseManifest(t, out)
			if len(rows) != 2 || rows[1][2] != "EXCEPTION-NOTICE.md" {
				t.Fatalf("exception lost: %#v", rows)
			}
			data, err := os.ReadFile(filepath.Join(out, rows[1][0], rows[1][2]))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "NOT a copy") || !strings.Contains(string(data), "@v0.0.1") || rows[1][4] != fmt.Sprintf("%x", sha256.Sum256(data)) {
				t.Fatalf("exception provenance lost: %s", data)
			}
		})
	}
}

func TestLicenseBundleReportsStorageFailures(t *testing.T) {
	for _, kind := range []string{"output file", "manifest directory", "module destination file", "license destination directory", "exception parent file", "exception destination directory"} {
		t.Run(kind, func(t *testing.T) {
			root := licenseFixture(t, "v0.0.1")
			out := filepath.Join(t.TempDir(), "output")
			if !strings.HasPrefix(kind, "exception") {
				if err := os.WriteFile(filepath.Join(root, "dependency/LICENSE"), []byte("fixture license"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "output file" {
				if err := os.WriteFile(out, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "manifest directory":
					if err := os.Mkdir(filepath.Join(out, "manifest.tsv"), 0700); err != nil {
						t.Fatal(err)
					}
				case "module destination file", "exception parent file":
					if err := os.WriteFile(filepath.Join(out, "github.com"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				case "license destination directory", "exception destination directory":
					name := "LICENSE"
					if kind == "exception destination directory" {
						name = "EXCEPTION-NOTICE.md"
					}
					if err := os.MkdirAll(filepath.Join(out, "github.com/mattn/go-localereader", name), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := run(out); err == nil {
				t.Fatal("reported successful bundle despite storage failure")
			}
		})
	}
}

func TestLicenseBundleMissingBuildMatrixFailsBeforeOutput(t *testing.T) {
	root := licenseFixture(t, "v0.0.1")
	if err := os.Remove(filepath.Join(root, "cmd/crewship/main.go")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "not-created")
	if err := run(out); err == nil || !strings.Contains(err.Error(), "go list -deps") {
		t.Fatalf("incomplete matrix accepted: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("wrote output before validating matrix: %v", err)
	}
}

func TestLicenseFileCopyAndHashRefuseUnreadableSources(t *testing.T) {
	root := t.TempDir()
	for _, source := range []string{filepath.Join(root, "missing"), root} {
		if _, err := fileSHA256(source); err == nil {
			t.Fatalf("hashed unreadable source %s", source)
		}
		if err := copyFile(source, filepath.Join(t.TempDir(), "copy")); err == nil {
			t.Fatalf("copied unreadable source %s", source)
		}
	}
	if err := copyFile(filepath.Join(root, "missing"), root); err == nil {
		t.Fatal("accepted missing copy source")
	}
	t.Chdir(root)
	if got := moduleVersions(); len(got) != 0 {
		t.Fatalf("invented versions outside a module: %#v", got)
	}
}
