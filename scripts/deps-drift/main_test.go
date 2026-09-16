package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lock builds a minimal v9 lockfile with one root importer. Each entry is
// "name specifier version" in the block named by the map key.
func lock(blocks map[string][]string) string {
	var b strings.Builder
	b.WriteString("lockfileVersion: '9.0'\n\nsettings:\n  autoInstallPeers: true\n\nimporters:\n\n  .:\n")
	for _, block := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
		entries := blocks[block]
		if len(entries) == 0 {
			continue
		}
		b.WriteString("    " + block + ":\n")
		for _, e := range entries {
			f := strings.Fields(e)
			b.WriteString("      '" + f[0] + "':\n        specifier: " + f[1] + "\n        version: " + f[2] + "\n")
		}
	}
	// A packages section the decoder must ignore.
	b.WriteString("\npackages:\n\n  '@sentry/nextjs@10.72.0':\n    resolution: {integrity: sha512-x}\n")
	return b.String()
}

func TestParseLockfile(t *testing.T) {
	src := lock(map[string][]string{
		"dependencies":    {"@sentry/nextjs ^10.70.0 10.71.0(next@16.0.0)", "react ^19.0.0 19.3.0"},
		"devDependencies": {"vitest ^4.0.0 4.1.11(@types/node@26.5.1)(jiti@2.7.0)", "local link:../x link:../x"},
	})
	got, err := parseLockfile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]dep{
		"@sentry/nextjs": {Spec: "^10.70.0", Resolved: "10.71.0"},
		"react":          {Spec: "^19.0.0", Resolved: "19.3.0"},
		"vitest":         {Spec: "^4.0.0", Resolved: "4.1.11"},
		"local":          {Spec: "link:../x", Resolved: "link:../x"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d: %v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
}

func TestParseLockfileMultipleImporters(t *testing.T) {
	src := "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      a:\n        specifier: ^1\n        version: 1.0.0\n  packages/web:\n    dependencies:\n      a:\n        specifier: ^2\n        version: 2.0.0\n"
	got, err := parseLockfile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].Resolved != "1.0.0" || got["packages/web:a"].Resolved != "2.0.0" {
		t.Fatalf("importers collapsed: %v", got)
	}
}

func TestParseLockfileEmpty(t *testing.T) {
	got, err := parseLockfile(nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty lockfile: %v, %v", got, err)
	}
}

func TestParseGoMod(t *testing.T) {
	src := `module example.com/m

go 1.27

require (
	github.com/a/direct v1.2.3
	github.com/b/indirect v0.1.0 // indirect
	github.com/c/replaced/v2 v2.0.0
	github.com/d/pinned v1.0.0
	github.com/e/local v1.0.0
)

replace github.com/c/replaced/v2 => github.com/fork/replaced/v2 v2.0.1

replace github.com/d/pinned v1.0.0 => github.com/d/pinned v1.0.0-fix

replace github.com/d/pinned => github.com/d/other v9.9.9

replace github.com/e/local => ../local
`
	got, err := parseGoMod([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]dep{
		"github.com/a/direct":      {Spec: "v1.2.3", Resolved: "v1.2.3"},
		"github.com/c/replaced/v2": {Spec: "v2.0.0", Resolved: "=> github.com/fork/replaced/v2@v2.0.1"},
		// The versioned replace wins over the wildcard, as in the go command.
		"github.com/d/pinned": {Spec: "v1.0.0", Resolved: "=> github.com/d/pinned@v1.0.0-fix"},
		"github.com/e/local":  {Spec: "v1.0.0", Resolved: "=> ../local"},
	}
	if _, ok := got["github.com/b/indirect"]; ok {
		t.Error("indirect requirement counted as direct")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d: %v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name       string
		base, head dep
		inBase     bool
		inHead     bool
		want       verdict
		unchanged  bool
	}{
		{
			name: "spec and resolution moved together",
			base: dep{"^10.70.0", "10.71.0"}, head: dep{"^10.72.0", "10.72.0"},
			inBase: true, inHead: true, want: verdictDeclared,
		},
		{
			name: "resolution moved, spec unchanged (the #2237 case)",
			base: dep{"^10.70.0", "10.71.0"}, head: dep{"^10.70.0", "10.72.0"},
			inBase: true, inHead: true, want: verdictUndeclared,
		},
		{
			name: "resolution moved DOWN, spec unchanged",
			base: dep{"^10.70.0", "10.72.0"}, head: dep{"^10.70.0", "10.71.0"},
			inBase: true, inHead: true, want: verdictUndeclared,
		},
		{
			name: "spec widened, resolution unchanged",
			base: dep{"^10.70.0", "10.71.0"}, head: dep{"^10.71.0", "10.71.0"},
			inBase: true, inHead: true, want: verdictDeclared,
		},
		{
			name: "unchanged",
			base: dep{"^10.70.0", "10.71.0"}, head: dep{"^10.70.0", "10.71.0"},
			inBase: true, inHead: true, unchanged: true,
		},
		{
			name:   "added",
			head:   dep{"^1.0.0", "1.0.0"},
			inHead: true, want: verdictAdded,
		},
		{
			name:   "removed",
			base:   dep{"^1.0.0", "1.0.0"},
			inBase: true, want: verdictRemoved,
		},
		{
			name: "go replace added without touching the require line",
			base: dep{"v2.0.0", "v2.0.0"}, head: dep{"v2.0.0", "=> github.com/fork/x@v2.0.1"},
			inBase: true, inHead: true, want: verdictUndeclared,
		},
		{
			name: "go replace target moved with the require",
			base: dep{"v2.0.0", "=> github.com/fork/x@v2.0.1"}, head: dep{"v2.1.0", "=> github.com/fork/x@v2.1.1"},
			inBase: true, inHead: true, want: verdictDeclared,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, head := map[string]dep{}, map[string]dep{}
			if tt.inBase {
				base["x"] = tt.base
			}
			if tt.inHead {
				head["x"] = tt.head
			}
			rows, unchanged := diff("npm", base, head)
			if tt.unchanged {
				if len(rows) != 0 || unchanged != 1 {
					t.Fatalf("rows=%v unchanged=%d, want no rows and 1 unchanged", rows, unchanged)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want 1: %v", len(rows), rows)
			}
			if rows[0].Verdict != tt.want {
				t.Fatalf("verdict = %s, want %s", rows[0].Verdict, tt.want)
			}
		})
	}
}

// writeFixture materialises a base and a head directory from lockfile and
// go.mod contents and returns their paths.
func writeFixture(t *testing.T, baseLock, baseMod, headLock, headMod string) (string, string) {
	t.Helper()
	root := t.TempDir()
	base, head := filepath.Join(root, "base"), filepath.Join(root, "head")
	for dir, files := range map[string]map[string]string{
		base: {lockfileName: baseLock, goModName: baseMod},
		head: {lockfileName: headLock, goModName: headMod},
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if content == "" {
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return base, head
}

const goModBase = "module example.com/m\n\ngo 1.27\n\nrequire github.com/a/direct v1.2.3\n"

func TestRunEndToEnd(t *testing.T) {
	baseLock := lock(map[string][]string{
		"dependencies":    {"@sentry/nextjs ^10.70.0 10.71.0", "left-pad ^1.0.0 1.0.0"},
		"devDependencies": {"@types/node ^26.4.0 26.4.0"},
	})
	tests := []struct {
		name     string
		headLock string
		headMod  string
		args     []string
		wantExit int
		wantOut  []string
		wantErr  []string
	}{
		{
			name:     "identical sides",
			headLock: baseLock,
			headMod:  goModBase,
			wantExit: 0,
			wantOut:  []string{"drift: none", "4 direct dependencies compared, 4 unchanged"},
		},
		{
			name: "declared bump passes, added and removed are printed",
			headLock: lock(map[string][]string{
				"dependencies":    {"@sentry/nextjs ^10.70.0 10.71.0", "new-dep ^2.0.0 2.0.0"},
				"devDependencies": {"@types/node ^26.5.0 26.5.1"},
			}),
			headMod:  goModBase,
			wantExit: 0,
			wantOut:  []string{"| npm | `@types/node` | `26.4.0` | `26.5.1` | `^26.4.0` | `^26.5.0` | declared |", "| removed |", "| added |", "all declared"},
		},
		{
			name: "undeclared drift fails",
			headLock: lock(map[string][]string{
				"dependencies":    {"@sentry/nextjs ^10.70.0 10.72.0", "left-pad ^1.0.0 1.0.0"},
				"devDependencies": {"@types/node ^26.5.0 26.5.1"},
			}),
			headMod:  goModBase,
			wantExit: 1,
			wantOut:  []string{"1 undeclared", "| npm | `@sentry/nextjs` | `10.71.0` | `10.72.0` | `^10.70.0` | `^10.70.0` | **UNDECLARED** |"},
			wantErr:  []string{"::error::npm @sentry/nextjs resolved 10.71.0 → 10.72.0 while its spec stayed ^10.70.0"},
		},
		{
			name: "undeclared drift with the label is a warning",
			headLock: lock(map[string][]string{
				"dependencies":    {"@sentry/nextjs ^10.70.0 10.72.0", "left-pad ^1.0.0 1.0.0"},
				"devDependencies": {"@types/node ^26.4.0 26.4.0"},
			}),
			headMod:  goModBase,
			args:     []string{"-allow-drift"},
			wantExit: 0,
			wantOut:  []string{"allowed by label", "**UNDECLARED**"},
			wantErr:  []string{"::warning::npm @sentry/nextjs"},
		},
		{
			name:     "go replace without a require change fails",
			headLock: baseLock,
			headMod:  goModBase + "\nreplace github.com/a/direct => github.com/fork/direct v1.2.4\n",
			wantExit: 1,
			wantOut:  []string{"| go | `github.com/a/direct` | `v1.2.3` | `=> github.com/fork/direct@v1.2.4` | `v1.2.3` | `v1.2.3` | **UNDECLARED** |"},
		},
		{
			name:     "go require bump passes",
			headLock: baseLock,
			headMod:  "module example.com/m\n\ngo 1.27\n\nrequire github.com/a/direct v1.3.0\n",
			wantExit: 0,
			wantOut:  []string{"| go | `github.com/a/direct` | `v1.2.3` | `v1.3.0` | `v1.2.3` | `v1.3.0` | declared |"},
		},
		{
			name:     "marker is written first for the comment upsert",
			headLock: baseLock,
			headMod:  goModBase,
			args:     []string{"-marker", "<!-- crewship:deps-drift -->"},
			wantExit: 0,
			wantOut:  []string{"<!-- crewship:deps-drift -->\n### "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, head := writeFixture(t, baseLock, goModBase, tt.headLock, tt.headMod)
			summary := filepath.Join(t.TempDir(), "summary.md")
			var stdout, stderr bytes.Buffer
			args := append([]string{"-base", base, "-head", head, "-summary", summary}, tt.args...)
			if got := run(args, &stdout, &stderr); got != tt.wantExit {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", got, tt.wantExit, stdout.String(), stderr.String())
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(stdout.String(), w) {
					t.Errorf("stdout missing %q:\n%s", w, stdout.String())
				}
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(stderr.String(), w) {
					t.Errorf("stderr missing %q:\n%s", w, stderr.String())
				}
			}
			written, err := os.ReadFile(summary)
			if err != nil {
				t.Fatal(err)
			}
			if string(written) != stdout.String() {
				t.Error("-summary file differs from stdout")
			}
		})
	}
}

func TestRunRejectsMissingBase(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run(nil, &stdout, &stderr); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}

// An unresolvable revision must be an error, never an empty base that reads
// as "no drift".
func TestRunRejectsUnknownRevision(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-base", "no-such-rev-" + t.Name(), "-head", "."}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit = %d, want 2\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "::error::") {
		t.Fatalf("no error annotation:\n%s", stderr.String())
	}
}

// A base that has no lockfile at all (a directory on disk rather than a
// revision, to keep the test hermetic) is an empty manifest: every head
// dependency is "added" and nothing fails.
func TestRunTreatsAbsentManifestAsEmpty(t *testing.T) {
	base, head := writeFixture(t, "", "", lock(map[string][]string{"dependencies": {"a ^1 1.0.0"}}), goModBase)
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-base", base, "-head", head}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit = %d, want 0\n%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "| added |") {
		t.Fatalf("expected added rows:\n%s", stdout.String())
	}
}
