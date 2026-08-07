package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover #1686: a binary built in a git worktree that lives INSIDE
// the parent clone's working tree is stamped with the PARENT clone's VCS state.
//
// The cause is in the Go toolchain and we cannot change it. cmd/go's git VCS
// descriptor declares its root marker as `rootName{filename: ".git", isDir:
// true}`, and isVCSRoot only accepts a match when `fi.IsDir() == root.isDir`. A
// linked worktree's `.git` is a FILE ("gitdir: …"), so the worktree directory
// is not recognised as a repository root and the search walks up to the
// enclosing clone, whose `.git` IS a directory. Everything downstream —
// vcs.revision, vcs.time, vcs.modified — then describes a tree that was not
// built.
//
// So the fix cannot live in Go: it lives in the build drivers, which ask *git*
// (which honours the `.git` file) and stamp the answer explicitly. These tests
// pin that chokepoint.

const stampScript = "../../scripts/build-stamp.sh"

// requireStampScript fails — deliberately never skips — when the chokepoint is
// absent. build-stamp.sh is a tracked file in this repository, not an optional
// piece of the environment, so "not there" is never a platform condition to
// tolerate: it means the thing these tests exist to pin has been deleted or
// renamed, which is exactly the regression they guard against. A skip would
// report that as `ok`.
func requireStampScript(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(stampScript); err != nil {
		t.Fatalf("%s missing: the build drivers have nowhere to get a worktree-correct stamp from (#1686): %v", stampScript, err)
	}
}

// nestedWorktree builds the exact failing layout: a clone with a linked
// worktree inside its own working tree, the two on different commits, and the
// PARENT dirty while the worktree is clean. Returns (parentDir, worktreeDir).
func nestedWorktree(t *testing.T) (string, string) {
	t.Helper()
	// Fail, don't skip. git is a hard requirement of this repo — the Makefile
	// and dev.sh both shell out to it for the stamp, and every CI job obtains
	// the tree with it — so its absence means the build tooling is broken, not
	// that this platform cannot run the test. The repo's other git-plumbing
	// tests (scripts/lint-migrations, scripts/lint-tsformat) already fail on
	// the same condition; a skip here would quietly retire #1686's only gate.
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git not on PATH: %v", err)
	}
	root := t.TempDir()
	parent := filepath.Join(root, "clone")

	git := func(dir string, args ...string) string {
		t.Helper()
		// Identity, signing and hooks are all pinned rather than inherited: the
		// fixture must not depend on (or be broken by) whatever the machine
		// running the tests has in its global git config. core.hooksPath in
		// particular would otherwise let a contributor's pre-commit hook run —
		// and fail, or hang — inside a unit test.
		full := append([]string{
			"-c", "user.name=test", "-c", "user.email=test@example.com",
			"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
			"-C", dir,
		}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	git(parent, "init", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(parent, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(parent, "add", "a.txt")
	git(parent, "commit", "-m", "first")

	// The layout this repo actually uses: .claude/worktrees/<name>/ — a linked
	// worktree nested inside the parent's own working tree.
	wt := filepath.Join(parent, ".claude", "worktrees", "agent-x")
	git(parent, "worktree", "add", "-b", "feature", wt)

	// Diverge: the worktree gets a commit the parent never sees.
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(wt, "commit", "-am", "second")

	// Parent is dirty (untracked file — which is what the toolchain counts as
	// modified too), worktree is clean. This is the inversion from the issue:
	// "uncommitted changes" reported for a tree with zero modifications.
	if err := os.WriteFile(filepath.Join(parent, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Precondition, and the root cause in one assertion: the toolchain's
	// isVCSRoot demands a `.git` DIRECTORY, and the worktree only has a file.
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("fixture invalid: worktree .git must be a FILE (got err=%v, isDir=%v)", err, fi != nil && fi.IsDir())
	}
	if fi, err := os.Stat(filepath.Join(parent, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf("fixture invalid: parent .git must be a DIRECTORY (got err=%v)", err)
	}
	return parent, wt
}

func stamp(t *testing.T, field, dir string) string {
	t.Helper()
	requireStampScript(t)
	out, err := exec.Command(stampScript, field, dir).Output()
	if err != nil {
		t.Fatalf("build-stamp.sh %s %s: %v", field, dir, err)
	}
	return strings.TrimSpace(string(out))
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// TestBuildStamp_ReportsTheWorktreeNotTheParentClone is #1686's evidence,
// turned into a gate. In the issue the worktree was clean at 3fbe705a while a
// `go build` inside it stamped the parent's 496c8c1a and vcs.modified=true —
// ten commits off, and "uncommitted changes" on a tree with none.
func TestBuildStamp_ReportsTheWorktreeNotTheParentClone(t *testing.T) {
	parent, wt := nestedWorktree(t)

	parentHead, wtHead := headOf(t, parent), headOf(t, wt)
	if parentHead == wtHead {
		t.Fatal("fixture invalid: the two trees must be on different commits")
	}

	if got := stamp(t, "commit", wt); got != wtHead {
		t.Errorf("commit stamp = %q; want the worktree's own %q (the parent clone is at %q)", got, wtHead, parentHead)
	}
	// The dirty bit is the worse half: it has no ldflags source at all today,
	// so even a `make build` — which gets the commit right — inherits the
	// parent tree's bit and prints "(uncommitted changes)" from a clean tree.
	if got := stamp(t, "dirty", wt); got != "false" {
		t.Errorf("dirty stamp = %q for a CLEAN worktree; the parent clone is the dirty one", got)
	}

	// And the stamp is not simply hard-coded: dirty the worktree, and it says so.
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("v3-uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stamp(t, "dirty", wt); got != "true" {
		t.Errorf("dirty stamp = %q after modifying the worktree; want true", got)
	}
}

// TestBuildStamp_UntrackedCountsAsDirty locks the stamp to the toolchain's own
// semantics. cmd/go treats any `git status --porcelain` output as modified,
// untracked files included — that is exactly how the parent clone in the issue
// got vcs.modified=true from nothing but a new docs/prd/ directory. A stamp
// that disagreed would replace one wrong answer with a different one.
func TestBuildStamp_UntrackedCountsAsDirty(t *testing.T) {
	_, wt := nestedWorktree(t)

	if got := stamp(t, "dirty", wt); got != "false" {
		t.Fatalf("precondition: clean worktree reported %q", got)
	}
	if err := os.WriteFile(filepath.Join(wt, "brand-new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stamp(t, "dirty", wt); got != "true" {
		t.Errorf("dirty = %q with an untracked file present; the toolchain counts that as modified", got)
	}
}

// TestBuildStamp_NonRepoIsUnknownNotClean: outside a repository the honest
// answer is "nobody knows", and buildinfo already models that as distinct from
// clean. Emitting "false" here would ship a confident wrong answer — the exact
// failure mode the package was written to avoid.
func TestBuildStamp_NonRepoIsUnknownNotClean(t *testing.T) {
	requireStampScript(t)
	dir := t.TempDir()
	out, err := exec.Command(stampScript, "dirty", dir).Output()
	if err != nil {
		t.Fatalf("build-stamp.sh must not fail outside a repo: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Errorf("dirty = %q outside a git repo; want empty (unknown), never a confident %q", got, got)
	}
}

// dirtyLdflagVar is the link-time target the build drivers must set. Spelled
// out here rather than derived, because a rename on either side that this test
// followed automatically would silently stop stamping every real binary.
const dirtyLdflagVar = "github.com/crewship-ai/crewship/internal/buildinfo.buildDirty"

// TestBuildStamp_LdflagsCarryTheWorktreesIdentity checks the form the build
// drivers actually consume: a ready-made `-X` list. Asserting on the fields
// individually would pass even if the assembled flags named the wrong linker
// symbols, which is the only part a `go build` ever sees.
func TestBuildStamp_LdflagsCarryTheWorktreesIdentity(t *testing.T) {
	_, wt := nestedWorktree(t)
	got := stamp(t, "ldflags", wt)

	for _, want := range []string{
		"-X main.commit=" + headOf(t, wt),
		"-X " + dirtyLdflagVar + "=false",
		"-X main.date=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ldflags missing %q:\n%s", want, got)
		}
	}
}

// TestBuildStamp_LdflagsOmitWhatItCannotKnow: outside a repository the flags
// must simply not mention commit or dirty. `-X main.commit=` would still read
// as "stamped" downstream, and buildinfo would have to guess which empty
// meant what.
func TestBuildStamp_LdflagsOmitWhatItCannotKnow(t *testing.T) {
	requireStampScript(t)
	got := stamp(t, "ldflags", t.TempDir())

	if strings.Contains(got, "main.commit") {
		t.Errorf("ldflags claim a commit outside a git repo:\n%s", got)
	}
	if strings.Contains(got, dirtyLdflagVar) {
		t.Errorf("ldflags claim a dirty bit outside a git repo:\n%s", got)
	}
	if !strings.Contains(got, "main.date=") {
		t.Errorf("the build time is always knowable and must still be stamped:\n%s", got)
	}
}

// TestBuildDriversStampTheirOwnTree is the half that actually fixes the bug for
// users. The Go change alone only makes a correct stamp *expressible*; if no
// build driver passes one, every dev slot keeps reporting the parent clone.
//
// dev.sh is the load-bearing row named in the package comment: it builds every
// dev slot, so `commit` on GET /api/v1/system/version — and therefore
// `crewship version --remote` — comes entirely from what that build stamped.
func TestBuildDriversStampTheirOwnTree(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []string
	}{
		// dev.sh had NO ldflags at all, so it takes the whole assembled list
		// from the script.
		{"../../dev.sh", []string{"build-stamp.sh", "-ldflags"}},
		// The Makefile already stamped version/commit/date correctly (git
		// honours the `.git` file); only the dirty bit had no source, so it
		// only needs that one, routed through the same script.
		{"../../Makefile", []string{"build-stamp.sh", dirtyLdflagVar}},
	} {
		raw, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s does not stamp %q — a binary it builds in a worktree still reports the parent clone (#1686)", tc.file, want)
			}
		}
	}
}
