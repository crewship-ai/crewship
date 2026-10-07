package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func invariantFile(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func validInvariantTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	invariantFile(t, root, "internal/provider/identity.go", "package provider\nconst AgentUID = 1001\nconst SidecarUID = 1002\n")
	invariantFile(t, root, "app/page.tsx", "export default function Page() { return null }")
	invariantFile(t, root, "internal/database/open.go", `package database; var _ = sql.Open("sqlite", "db")`)
	invariantFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	// The private-working-files invariant examines the index, so model a real
	// public checkout rather than an unversioned directory.
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func TestRepositoryInvariantChecksAcceptSupportedTree(t *testing.T) {
	root := validInvariantTree(t)
	for _, check := range []func(string) []violation{noAPIRoutesUnderApp, noSqlite3DriverName, noNpmOrYarnLockfile, sidecarAndAgentUIDsUnchanged, noPrivateWorkingFiles} {
		if got := check(root); len(got) != 0 {
			t.Fatalf("valid checkout rejected: %#v", got)
		}
	}
	if got := noAPIRoutesUnderApp(t.TempDir()); len(got) != 0 {
		t.Fatalf("optional app directory rejected: %#v", got)
	}
}

func TestRepositoryInvariantFindsStaticExportRoutes(t *testing.T) {
	root := validInvariantTree(t)
	for _, name := range []string{"app/api/one/route.ts", "app/(dashboard)/two/route.tsx", "app/three/route.js"} {
		invariantFile(t, root, name, "export function GET() {}")
	}
	invariantFile(t, root, "app/components/route.test.ts", "test fixture")
	violations := noAPIRoutesUnderApp(root)
	if len(violations) != 3 {
		t.Fatalf("lost forbidden routes: %#v", violations)
	}
	for _, v := range violations {
		if v.rule != "never add API routes under app/" || !strings.Contains(v.detail, "static export drops") {
			t.Fatalf("unactionable diagnostic: %#v", v)
		}
	}
}

func TestRepositoryInvariantFindsWrongDriverAndPackageManager(t *testing.T) {
	root := validInvariantTree(t)
	bad := invariantFile(t, root, "internal/database/bad.go", "package database\nvar _ = sql.Open(\n \"sqlite3\", \"db\")\n")
	invariantFile(t, root, "internal/database/bad_test.go", `package database; var _ = sql.Open("sqlite3", "test")`)
	got := noSqlite3DriverName(root)
	if len(got) != 1 || !strings.Contains(got[0].detail, bad) {
		t.Fatalf("driver violation missing/wrong: %#v", got)
	}
	for _, name := range []string{"package-lock.json", "yarn.lock", "npm-shrinkwrap.json"} {
		invariantFile(t, root, name, "{}")
	}
	if got := noNpmOrYarnLockfile(root); len(got) != 3 {
		t.Fatalf("lockfile violations: %#v", got)
	}
}

func TestRepositoryInvariantRequiresBothRuntimeIdentities(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		missing      int
	}{
		{"neither", "const UID = 1000", 2},
		{"agent only", "const UID = 1001", 1},
		{"sidecar only", "const UID = 1002", 1},
		{"both", "const Agent = 1001; const Sidecar = 1002", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			invariantFile(t, root, "internal/provider/ids.go", tc.source)
			invariantFile(t, root, "internal/unrelated/ids.go", "1001 1002")
			if got := sidecarAndAgentUIDsUnchanged(root); len(got) != tc.missing {
				t.Fatalf("identity diagnostics: %#v", got)
			}
		})
	}
}

func TestRepositoryInvariantTraversalSkipsBuildDependenciesAndTests(t *testing.T) {
	root := t.TempDir()
	want := []string{invariantFile(t, root, "internal/provider/main.go", "package provider"), invariantFile(t, root, "cmd/server/main.go", "package main")}
	for _, dir := range []string{"node_modules", ".git", "web", "out", ".next"} {
		invariantFile(t, root, dir+"/nested/fixture.go", "package fixture")
	}
	invariantFile(t, root, "internal/provider/main_test.go", "package provider")
	invariantFile(t, root, "README.md", "documentation")
	got := goFiles(root)
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source inventory %v, want %v", got, want)
	}
	if got := goFiles(filepath.Join(root, "absent")); len(got) != 0 {
		t.Fatalf("invented missing files: %v", got)
	}
}

func TestRepositoryInvariantMainReportsSuccess(t *testing.T) {
	root := validInvariantTree(t)
	t.Chdir(root)
	previousArgs := os.Args
	previousStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer func() { os.Args = previousArgs; os.Stdout = previousStdout; w.Close() }()
	os.Args = []string{"agents-invariants"}
	os.Stdout = w
	main()
	os.Args = []string{"agents-invariants", root}
	main()
	w.Close()
	os.Stdout = previousStdout
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "5 checkable NEVER DO entries hold") != 2 {
		t.Fatalf("unexpected success output: %s", got)
	}
}

func TestRepositoryInvariantCommandFailure(t *testing.T) {
	if root := os.Getenv("CREWSHIP_TEST_INVARIANT_HELPER_ROOT"); root != "" {
		os.Args = []string{"agents-invariants", root}
		main()
		return
	}
	root := validInvariantTree(t)
	invariantFile(t, root, "app/api/example/route.ts", "export function GET() {}")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRepositoryInvariantCommandFailure$")
	cmd.Env = append(os.Environ(), "CREWSHIP_TEST_INVARIANT_HELPER_ROOT="+root)
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("CLI did not fail closed: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "AGENTS.md invariant violated: never add API routes under app/") || !strings.Contains(stderr.String(), "route.ts") {
		t.Fatalf("missing actionable refusal: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), "entries hold") {
		t.Fatalf("failure claimed success: %s", stdout.String())
	}
}

func TestRepositoryInvariantIdentityScopeIgnoresCheckoutName(t *testing.T) {
	for _, name := range []string{"clone", "sidecar", "provider", "orchestrator"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name)
			invariantFile(t, root, "internal/unrelated/ids.go", "const Agent = 1001; const Sidecar = 1002")
			invariantFile(t, root, "internal/provider-copy/ids.go", "const Agent = 1001; const Sidecar = 1002")
			if got := sidecarAndAgentUIDsUnchanged(root); len(got) != 2 {
				t.Fatalf("unrelated numbers satisfied runtime identity check: %#v", got)
			}
			invariantFile(t, root, "internal/orchestrator/ids.go", "const Agent = 1001")
			invariantFile(t, root, "internal/sidecar/ids.go", "const Sidecar = 1002")
			if got := sidecarAndAgentUIDsUnchanged(root); len(got) != 0 {
				t.Fatalf("runtime identities missed: %#v", got)
			}
		})
	}
}
