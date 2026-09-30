// Command gen-licenses collects the license/NOTICE texts of every Go module
// linked into the distributed binaries, across the full GOOS/GOARCH/tags
// build matrix that GoReleaser ships, and writes them plus a SHA-256
// manifest used by the artifact-content checks.
//
// It is used on the host via `make licenses`
// (scripts/gen-license-bundle.sh) and inside the Docker backend build stage,
// so it must depend only on the Go toolchain. It executes `go list` with
// the matrix environment rather than linking golang.org/x/tools, and FAILS
// when a distributed module ships no recognizable license text — an absent
// text is a packaging defect, not a warning (version-scoped reviewed
// exceptions below are the only escape hatch).
package main

import (
	"crypto/sha256"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// matrix is the set of (GOOS, GOARCH, build tags, main package)
// combinations GoReleaser actually distributes (see .goreleaser.yml): the
// full daemon and the CLI (-tags=clionly) for linux/darwin/windows on
// amd64+arm64, and the linux-only sidecar on both architectures. A module
// that only a single architecture pulls in (arch-specific imports) is
// therefore part of the union and its missing text fails the build.
var matrix = []struct {
	goos   string
	goarch string
	tags   string
	mainP  string
}{
	{"linux", "amd64", "", "./cmd/crewship"},
	{"linux", "arm64", "", "./cmd/crewship"},
	{"darwin", "amd64", "", "./cmd/crewship"},
	{"darwin", "arm64", "", "./cmd/crewship"},
	{"windows", "amd64", "", "./cmd/crewship"},
	{"windows", "arm64", "", "./cmd/crewship"},
	{"linux", "amd64", "clionly", "./cmd/crewship"},
	{"linux", "arm64", "clionly", "./cmd/crewship"},
	{"darwin", "amd64", "clionly", "./cmd/crewship"},
	{"darwin", "arm64", "clionly", "./cmd/crewship"},
	{"windows", "amd64", "clionly", "./cmd/crewship"},
	{"windows", "arm64", "clionly", "./cmd/crewship"},
	{"linux", "amd64", "", "./cmd/crewship-sidecar"},
	{"linux", "arm64", "", "./cmd/crewship-sidecar"},
}

const selfModule = "github.com/crewship-ai/crewship"

// exceptions lists distributed modules that ship no license FILE in the
// module root, scoped to the EXACT version the evidence was reviewed for.
// The generated EXCEPTION-NOTICE records the cited declaration — it is NOT
// a copy of the upstream license text. A different version fails hard.
var exceptions = map[string]string{
	// v0.0.1 has no LICENSE/COPYING file at the module root; its README has
	// a "## License" section stating "MIT" and nothing else. (2026-09-28
	// review; windows-only dependency of the distributed CLI/daemon.)
	"github.com/mattn/go-localereader@v0.0.1": "README section '## License' states 'MIT'; no license file ships in the module",
}

// isLicenseFileName reports whether a module-root file name carries license
// or NOTICE material worth bundling. Exact standard names only — anything
// else needs a human decision (or an exceptions entry with evidence).
func isLicenseFileName(name string) bool {
	switch name {
	case "LICENSE", "LICENSE.md", "LICENSE.txt", "LICENSE-MIT", "LICENSE-APACHE",
		"COPYING", "COPYING.txt", "COPYING.md", "NOTICE", "NOTICE.txt", "NOTICE.md",
		"PATENTS":
		return true
	}
	return false
}

func main() {
	out := flag.String("out", "build/licenses/go", "output directory for texts + manifest.tsv")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintf(os.Stderr, "gen-licenses: %v\n", err)
		os.Exit(1)
	}
}

func run(outDir string) error {
	// go mod download may leave module zips unextracted. Enumerate every
	// target's imports first; go list -deps materializes the module cache,
	// so the subsequent go list -m can report a real .Dir for each module.
	prov, mods, err := linkedModules()
	if err != nil {
		return err
	}
	dirs := moduleDirs()
	versions := moduleVersions()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	manifest, err := os.Create(filepath.Join(outDir, "manifest.tsv"))
	if err != nil {
		return err
	}
	defer manifest.Close()
	w := csv.NewWriter(manifest)
	w.Comma = '\t'
	if err := w.Write([]string{"module", "version", "file", "provenance", "sha256"}); err != nil {
		return err
	}

	sort.Strings(mods)
	var missing []string
	copied := 0
	for _, mod := range mods {
		if mod == selfModule {
			continue
		}
		dir := dirs[mod]
		if dir == "" {
			missing = append(missing, mod+"@"+versions[mod]+" (no module-cache dir)")
			continue
		}
		found := 0
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !isLicenseFileName(e.Name()) {
				continue
			}
			name := e.Name()
			src := filepath.Join(dir, name)
			dstDir := filepath.Join(outDir, mod)
			if err := os.MkdirAll(dstDir, 0o755); err != nil {
				return err
			}
			if err := copyFile(src, filepath.Join(dstDir, name)); err != nil {
				return err
			}
			sum, err := fileSHA256(src)
			if err != nil {
				return err
			}
			if err := w.Write([]string{mod, versions[mod], name, prov[mod], sum}); err != nil {
				return err
			}
			found++
			copied++
		}
		if found == 0 {
			if reason, ok := exceptions[mod+"@"+versions[mod]]; ok {
				dstDir := filepath.Join(outDir, mod)
				if err := os.MkdirAll(dstDir, 0o755); err != nil {
					return err
				}
				note := fmt.Sprintf(
					"# License notice for %s@%s\n\n"+
						"This module ships no license file in its root. Evidence for its\n"+
						"license declaration: %s.\n\n"+
						"This notice records the declaration; it is NOT a copy of the\n"+
						"upstream license text and not a complete license settlement.\n"+
						"The canonical text is published by OSI/SPDX.\n",
					mod, versions[mod], reason)
				if err := os.WriteFile(filepath.Join(dstDir, "EXCEPTION-NOTICE.md"), []byte(note), 0o644); err != nil {
					return err
				}
				sum, err := fileSHA256(filepath.Join(dstDir, "EXCEPTION-NOTICE.md"))
				if err != nil {
					return err
				}
				if err := w.Write([]string{mod, versions[mod], "EXCEPTION-NOTICE.md", prov[mod], sum}); err != nil {
					return err
				}
				found++
				copied++
				fmt.Printf("gen-licenses: EXCEPTION %s@%s — %s\n", mod, versions[mod], reason)
			}
		}
		if found == 0 {
			missing = append(missing, mod+"@"+versions[mod]+" (no license file in module root)")
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	fmt.Printf("gen-licenses: %d modules, %d texts -> %s\n", len(mods)-1, copied, outDir)
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "gen-licenses: MISSING license texts for %d distributed modules:\n", len(missing))
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		return fmt.Errorf("%d distributed modules ship no license text; add the text upstream or a version-scoped reviewed exception", len(missing))
	}
	return nil
}

// moduleDirs maps module path → cache dir via `go list -m`.
func moduleDirs() map[string]string {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}|{{.Path}}", "all").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-licenses: go list -m: %v\n", err)
		os.Exit(1)
	}
	dirs := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 && parts[0] != "" {
			dirs[parts[1]] = parts[0]
		}
	}
	return dirs
}

// moduleVersions resolves module path → version from the locked module graph.
func moduleVersions() map[string]string {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Path}}|{{.Version}}", "all").Output()
	if err != nil {
		return map[string]string{}
	}
	versions := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 {
			versions[parts[0]] = parts[1]
		}
	}
	return versions
}

// linkedModules returns the union of module paths across the build matrix
// together with each module's provenance (the GOOS/GOARCH/tags cells that
// pulled it in — recorded in the manifest so coverage is auditable).
func linkedModules() (map[string]string, []string, error) {
	union := map[string]bool{}
	prov := map[string]string{}
	for _, m := range matrix {
		args := []string{"list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}"}
		if m.tags != "" {
			args = append(args, "-tags", m.tags)
		}
		args = append(args, m.mainP)
		cmd := exec.Command("go", args...)
		cmd.Env = append(os.Environ(), "GOOS="+m.goos, "GOARCH="+m.goarch, "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			// A matrix cell that cannot even be listed is a build-matrix
			// defect: the release pipeline builds it, so listing must work.
			return nil, nil, fmt.Errorf("go list -deps GOOS=%s GOARCH=%s tags=%q %s: %w\n%s",
				m.goos, m.goarch, m.tags, m.mainP, err, out)
		}
		cell := m.goos + "/" + m.goarch
		if m.tags != "" {
			cell += "+" + m.tags
		}
		for _, line := range strings.Split(string(out), "\n") {
			if line == "" {
				continue
			}
			union[line] = true
			// One module spans many packages, so its path appears once per
			// package in -deps output; record each cell exactly once.
			if prev, ok := prov[line]; ok {
				if !strings.Contains(","+prev+",", ","+cell+",") {
					prov[line] = prev + "," + cell
				}
			} else {
				prov[line] = cell
			}
		}
	}
	mods := make([]string, 0, len(union))
	for m := range union {
		mods = append(mods, m)
	}
	return prov, mods, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// 0644, deliberately NOT the source mode: module-cache files are marked
	// read-only (0444) by the Go toolchain, and preserving that makes every
	// later regeneration fail to overwrite its own output.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
