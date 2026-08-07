package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is this package's path back to the checkout.
const repoRoot = "../.."

// --- the gate against the real tree ----------------------------------------

// TestRealTreeMatchesDeclaration is the gate itself, run by `go test` as well as
// by the CI binary. Having it here means the check survives someone forgetting
// to keep the workflow step — a check that only runs when a workflow remembers
// to run it is a check that eventually stops running.
func TestRealTreeMatchesDeclaration(t *testing.T) {
	surface, err := Scan(repoRoot)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if problems := Check(repoRoot, surface); len(problems) > 0 {
		t.Fatalf("the Docker surface no longer matches its declaration:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// TestRealTreeSurfaceIsNotEmpty guards the failure mode that would make every
// other assertion here vacuous: a scanner that finds nothing agrees with an
// allow-list about nothing.
func TestRealTreeSurfaceIsNotEmpty(t *testing.T) {
	surface, err := Scan(repoRoot)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(surface.Calls) < 100 {
		t.Fatalf("scan found %d call sites; the provider alone has more than that — the scan is broken", len(surface.Calls))
	}
	for _, want := range []string{"ContainerCreate", "ExecCreate", "CopyToContainer", "ContainerCommit"} {
		if _, ok := surface.Methods[want]; !ok {
			t.Errorf("scan did not find %s, which the provider demonstrably calls", want)
		}
	}
}

// TestExclusionListsNameRealSDKMethods keeps the two hand-maintained exclusion
// sets from rotting into no-ops. A typo in localMethods is silent: the name
// simply never matches, and the entry protects nothing.
func TestExclusionListsNameRealSDKMethods(t *testing.T) {
	sdk := mobyMethods()
	for name := range localMethods {
		if !sdk[name] {
			t.Errorf("localMethods lists %q, which is not a method on *client.Client — the exclusion does nothing", name)
		}
	}
	for name := range ambiguousMethods {
		if !sdk[name] {
			t.Errorf("ambiguousMethods lists %q, which is not a method on *client.Client", name)
		}
	}
}

// TestAllowListNamesRealSDKMethods catches an allow-list entry that no longer
// corresponds to an SDK method — after a moby upgrade renames one, say.
func TestAllowListNamesRealSDKMethods(t *testing.T) {
	sdk := mobyMethods()
	for _, e := range allowList {
		if !sdk[e.Method] {
			t.Errorf("allowList declares %q, which is not a method on *client.Client", e.Method)
		}
		if len(e.ProxyVars) == 0 {
			t.Errorf("%s declares no proxy variable; every endpoint needs at least one", e.Method)
		}
		if e.Tier != TierCore && e.Tier != TierDevcontainer {
			t.Errorf("%s has tier %q", e.Method, e.Tier)
		}
	}
}

// --- teeth: a new call site must fail the check ----------------------------

// TestNewCallSiteFailsTheCheck is the mutation this gate exists for. It writes
// a package that calls an endpoint nobody declared, scans it for real, and
// requires the check to name it. Without this, "the allow-list is gated" is a
// claim about a program that has never been shown to say no.
func TestNewCallSiteFailsTheCheck(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/rogue/rogue.go", `package rogue

import (
	"context"

	"github.com/moby/moby/client"
)

type H struct{ docker *client.Client }

func (h *H) Build(ctx context.Context) {
	_, _ = h.docker.ImageBuild(ctx, nil, client.ImageBuildOptions{})
}
`)

	surface, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, ok := surface.Methods["ImageBuild"]; !ok {
		t.Fatalf("scan missed the new ImageBuild call site entirely; methods found: %v", surface.Methods)
	}

	problems := checkMethods(surface)
	if !mentions(problems, "ImageBuild") {
		t.Fatalf("checkMethods stayed silent about an undeclared ImageBuild call: %v", problems)
	}
	if !mentions(problems, "internal/rogue/rogue.go") {
		t.Errorf("the failure should point at the call site; got: %v", problems)
	}
}

// TestControlCaseDoesNotFail is the other half of the mutation: the same shape
// of package, calling something the allow-list already covers, must not fail.
// A check that rejects everything proves nothing by rejecting the mutant.
func TestControlCaseDoesNotFail(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/provider/docker/x.go", `package docker

import (
	"context"

	"github.com/moby/moby/client"
)

type P struct{ client *client.Client }

func (p *P) Go(ctx context.Context) {
	_, _ = p.client.ContainerStats(ctx, "id", client.ContainerStatsOptions{})
}
`)

	surface, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if problems := checkMethods(surface); mentions(problems, "ContainerStats is called but not in the allow-list") {
		t.Fatalf("a declared endpoint was reported as undeclared: %v", problems)
	}
}

// TestCallFromNewPackageFailsTheCheck covers the subtler drift: an endpoint we
// already allow, reached from a package that never touched Docker before. The
// endpoint list is unchanged; the blast radius is not.
func TestCallFromNewPackageFailsTheCheck(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/newcomer/n.go", `package newcomer

import (
	"context"

	"github.com/moby/moby/client"
)

type N struct{ docker *client.Client }

func (n *N) Go(ctx context.Context) {
	_, _ = n.docker.ContainerStats(ctx, "id", client.ContainerStatsOptions{})
}
`)

	surface, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !mentions(checkPackages(surface), "internal/newcomer") {
		t.Fatalf("a new package importing the Docker SDK was not reported: %v", checkPackages(surface))
	}
	if !mentions(checkMethods(surface), "internal/newcomer") {
		t.Errorf("ContainerStats gaining a new caller was not reported: %v", checkMethods(surface))
	}
}

// TestStaleDeclarationFailsTheCheck covers the direction nobody notices: an
// endpoint we stopped calling, still published as one an operator must grant.
func TestStaleDeclarationFailsTheCheck(t *testing.T) {
	surface := &Surface{Methods: map[string][]string{}}
	problems := checkMethods(surface)
	if len(problems) != len(allowList) {
		t.Fatalf("an empty surface should report every declared endpoint as unused; got %d of %d", len(problems), len(allowList))
	}
	if !mentions(problems, "ContainerCreate") || !mentions(problems, "nothing calls it") {
		t.Errorf("the stale-entry failure should say the endpoint is unused: %v", problems[:1])
	}
}

// TestUndeclaredShelloutFailsTheCheck covers the path no type-checker sees: a
// subprocess handed DOCKER_HOST is a second client of the same socket.
func TestUndeclaredShelloutFailsTheCheck(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/tool/tool.go", `package tool

import (
	"context"
	"os/exec"
)

func Run(ctx context.Context, host string) error {
	cmd := exec.CommandContext(ctx, "docker", "system", "prune", "-f")
	cmd.Env = append(cmd.Env, "DOCKER_HOST="+host)
	return cmd.Run()
}
`)

	surface, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(surface.Shellouts) != 1 {
		t.Fatalf("expected 1 shellout, got %d: %+v", len(surface.Shellouts), surface.Shellouts)
	}
	if !mentions(checkShellouts(surface), "internal/tool/tool.go") {
		t.Errorf("an undeclared daemon-pinned shellout was not reported: %v", checkShellouts(surface))
	}
}

// TestReadingDockerHostIsNotAShellout is the control for the rule above. The
// provider reads DOCKER_HOST to find the daemon for the SDK; that is not a
// second client, and flagging it would train people to ignore the check.
func TestReadingDockerHostIsNotAShellout(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/tool/tool.go", `package tool

import (
	"os"
	"os/exec"
)

func Run() error {
	_ = os.Getenv("DOCKER_HOST")
	return exec.Command("git", "status").Run()
}
`)

	surface, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(surface.Shellouts) != 0 {
		t.Fatalf("reading DOCKER_HOST should not count as a shellout: %+v", surface.Shellouts)
	}
}

// TestAmbiguousNamesDoNotFalselyMatch keeps the check usable. slog.Logger.Info
// shares a name with the SDK's Info; a gate that fires on every log line gets
// switched off.
func TestAmbiguousNamesDoNotFalselyMatch(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/noisy/n.go", `package noisy

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/moby/moby/client"
)

type N struct {
	logger *slog.Logger
	db     *sql.DB
	client *client.Client
}

func (n *N) Go(ctx context.Context) {
	n.logger.Info("hello")
	_ = n.db.Ping()
	_, _ = n.client.ContainerList(ctx, client.ContainerListOptions{})
}
`)

	surface, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, ok := surface.Methods["Info"]; ok {
		t.Errorf("slog.Logger.Info was counted as a Docker call")
	}
	if _, ok := surface.Methods["Ping"]; ok {
		t.Errorf("sql.DB.Ping was counted as a Docker call")
	}
	if _, ok := surface.Methods["ContainerList"]; !ok {
		t.Errorf("the real Docker call in the same file was missed")
	}
}

// TestLocalMethodsAreNotCounted: Close() collides with io.Closer thousands of
// times and issues no request. Counting it would drown the signal.
func TestLocalMethodsAreNotCounted(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/c/c.go", `package c

import (
	"database/sql"

	"github.com/moby/moby/client"
)

func Go(rows *sql.Rows, cli *client.Client) {
	rows.Close()
	cli.Close()
}
`)

	surface, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(surface.Calls) != 0 {
		t.Fatalf("Close() should issue no HTTP request and count for nothing: %+v", surface.Calls)
	}
}

// --- teeth: the published deployment and guide -----------------------------

func TestComposeMissingPermissionFails(t *testing.T) {
	root := t.TempDir()
	compose := readRepoFile(t, composePath)
	mutated := strings.Replace(compose, "\n      EXEC: 1\n", "\n", 1)
	if mutated == compose {
		t.Fatalf("could not remove EXEC from %s — the fixture no longer matches the file", composePath)
	}
	writeFile(t, root, composePath, mutated)

	if !mentions(checkCompose(root), "EXEC") {
		t.Fatalf("dropping EXEC from the deployment was not reported: %v", checkCompose(root))
	}
}

func TestComposeExtraPermissionFails(t *testing.T) {
	root := t.TempDir()
	compose := readRepoFile(t, composePath)
	mutated := strings.Replace(compose, "\n      EXEC: 1\n", "\n      EXEC: 1\n      SWARM: 1\n", 1)
	if mutated == compose {
		t.Fatalf("could not add SWARM to %s", composePath)
	}
	writeFile(t, root, composePath, mutated)

	if !mentions(checkCompose(root), "SWARM") {
		t.Fatalf("granting an unneeded permission was not reported: %v", checkCompose(root))
	}
}

// TestComposeParseIsScopedToTheProxyService: the file has other services with
// their own settings. A permission parse that read the whole document would
// report whatever happened to look like a variable.
func TestComposeParseIsScopedToTheProxyService(t *testing.T) {
	root := t.TempDir()
	compose := readRepoFile(t, composePath)
	mutated := strings.Replace(compose, "\n  crewship:\n", "\n  crewship:\n    environment:\n      SWARM: 1\n", 1)
	if mutated == compose {
		t.Fatalf("could not add a decoy to the crewship service")
	}
	writeFile(t, root, composePath, mutated)

	if mentions(checkCompose(root), "SWARM") {
		t.Fatalf("a value outside the proxy service was read as a granted permission: %v", checkCompose(root))
	}
}

func TestDocsMissingEndpointFails(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, composePath, readRepoFile(t, composePath))

	guide := readRepoFile(t, docsPath)
	mutated := strings.ReplaceAll(guide, "ContainerUnpause", "ContainerResume")
	if mutated == guide {
		t.Fatalf("could not remove ContainerUnpause from %s", docsPath)
	}
	writeFile(t, root, docsPath, mutated)

	if !mentions(checkDocs(root), "ContainerUnpause") {
		t.Fatalf("an endpoint missing from the guide was not reported: %v", checkDocs(root))
	}
}

func TestDocsMissingProxyVariableFails(t *testing.T) {
	root := t.TempDir()
	guide := readRepoFile(t, docsPath)
	mutated := strings.ReplaceAll(guide, "COMMIT", "commit")
	if mutated == guide {
		t.Fatalf("could not remove COMMIT from %s", docsPath)
	}
	writeFile(t, root, docsPath, mutated)

	if !mentions(checkDocs(root), "COMMIT") {
		t.Fatalf("a proxy variable missing from the guide was not reported: %v", checkDocs(root))
	}
}

// --- helpers ---------------------------------------------------------------

func writeGo(t *testing.T, root, rel, src string) {
	t.Helper()
	writeFile(t, root, rel, src)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mentions(problems []string, substr string) bool {
	for _, p := range problems {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}
