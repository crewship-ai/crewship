package buildinfo

import (
	"runtime"
	"runtime/debug"
	"testing"
)

// The whole point of this package is that the ONE build path where "what
// commit is this server running?" actually gets asked — a dev slot built by
// dev.sh with a bare `go build` — is the path where the ldflags are all
// placeholders. So every test here asserts the resolved VALUE, not that a
// field is non-empty: a resolver that answered "none" for the commit would
// satisfy "non-empty" and be exactly the bug.

func vcs(rev, when, modified string) []debug.BuildSetting {
	return []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: rev},
		{Key: "vcs.time", Value: when},
		{Key: "vcs.modified", Value: modified},
	}
}

// showDirty renders the three-state flag readably — %v on a *bool prints an
// address, which is exactly the kind of unusable diagnostic this package
// exists to prevent.
func showDirty(d *bool) string {
	if d == nil {
		return "unknown"
	}
	if *d {
		return "true"
	}
	return "false"
}

// TestResolve_DevShBuildReportsTheVCSCommit is the case the issue was filed
// for: a bare `go build` with no -ldflags at all, so main.version/commit/date
// keep their in-source defaults "dev"/"none"/"unknown" while the Go toolchain
// still stamps vcs.* into the binary. Empirically verified on this repo: a
// plain `go build` (and a `-trimpath` one) both carry
// vcs.revision/vcs.time/vcs.modified.
//
// dev.sh stamps explicitly since #1686 (the toolchain's stamps describe the
// wrong tree in a nested worktree), but this fallback still has to hold: it is
// what `go build ./cmd/crewship` by hand, and any deploy that skips dev.sh,
// gets.
func TestResolve_DevShBuildReportsTheVCSCommit(t *testing.T) {
	got := resolve("dev", "none", "unknown", "",
		vcs("496c8c1a84be761abdb5cbe323a1fd501b8b9ab7", "2026-08-02T15:35:24Z", "true"))

	if got.Commit != "496c8c1a84be761abdb5cbe323a1fd501b8b9ab7" {
		t.Errorf("commit = %q, want the vcs.revision — the ldflags placeholder %q must not win", got.Commit, "none")
	}
	if got.BuildTime != "2026-08-02T15:35:24Z" {
		t.Errorf("build time = %q, want the vcs.time", got.BuildTime)
	}
	if got.Dirty == nil {
		t.Fatal("dirty = nil, want true — vcs.modified was stamped")
	}
	if !*got.Dirty {
		t.Error("dirty = false, want true (vcs.modified=true)")
	}
	// The version stays "dev". debug.BuildInfo.Main.Version offers a
	// pseudo-version here ("v0.1.0-beta.4.0.20260802153524-496c8c1a84be+dirty")
	// and adopting it would be actively harmful: internal/cli's version-skew
	// warning parses this string as semver, so a dev build would start
	// claiming to be a release and produce bogus "upgrade the server" hints.
	if got.Version != "dev" {
		t.Errorf("version = %q, want %q — a dev build must not present as a release", got.Version, "dev")
	}
}

// TestResolve_ReleaseBuildPrefersLdflags: goreleaser stamps
// -X main.commit={{.Commit}} AND the toolchain stamps vcs.revision. The
// ldflags value is the release's own record of what it built, so it wins.
func TestResolve_ReleaseBuildPrefersLdflags(t *testing.T) {
	got := resolve("v0.1.0-beta.4", "cafebabecafebabecafebabecafebabecafebabe", "2026-07-01T09:00:00Z", "",
		vcs("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-01T08:59:00Z", "false"))

	if got.Version != "v0.1.0-beta.4" {
		t.Errorf("version = %q, want the ldflags value", got.Version)
	}
	if got.Commit != "cafebabecafebabecafebabecafebabecafebabe" {
		t.Errorf("commit = %q, want the ldflags commit, not the vcs.revision", got.Commit)
	}
	if got.BuildTime != "2026-07-01T09:00:00Z" {
		t.Errorf("build time = %q, want the ldflags date", got.BuildTime)
	}
	if got.Dirty == nil || *got.Dirty {
		t.Errorf("dirty = %v, want false — vcs.modified=false", got.Dirty)
	}
}

// TestResolve_DockerBuildHasLdflagsButNoVCS: the Dockerfile builds from a
// context that does not carry .git, so vcs.* is absent, but ARG VERSION /
// COMMIT / DATE feed the ldflags. Dirty is genuinely unknown there and must
// be null rather than a confident "clean".
func TestResolve_DockerBuildHasLdflagsButNoVCS(t *testing.T) {
	got := resolve("v0.1.0-beta.4", "cafebabe", "2026-07-01T09:00:00Z", "", nil)

	if got.Commit != "cafebabe" {
		t.Errorf("commit = %q, want the ldflags commit", got.Commit)
	}
	if got.Dirty != nil {
		t.Errorf("dirty = %v, want nil — nothing stamped vcs.modified, so cleanliness is unknown", *got.Dirty)
	}
}

// TestResolve_NoLdflagsNoVCSReportsEmptyNotPlaceholder: `go run` and
// `go install`-from-proxy give neither. The placeholders must be erased
// rather than shipped over the wire, so a client can tell "the server does
// not know" from "the server is at commit none".
func TestResolve_NoLdflagsNoVCSReportsEmptyNotPlaceholder(t *testing.T) {
	got := resolve("dev", "none", "unknown", "", nil)

	if got.Commit != "" {
		t.Errorf("commit = %q, want empty — %q is a placeholder, not a commit", got.Commit, "none")
	}
	if got.BuildTime != "" {
		t.Errorf("build time = %q, want empty — %q is a placeholder", got.BuildTime, "unknown")
	}
	if got.Dirty != nil {
		t.Errorf("dirty = %v, want nil", *got.Dirty)
	}
	if got.Version != "dev" {
		t.Errorf("version = %q, want %q", got.Version, "dev")
	}
}

// TestResolve_EmptyLdflagsFallBackToVCS covers the api.NewRouter default,
// which resolves with no ldflags strings at all.
func TestResolve_EmptyLdflagsFallBackToVCS(t *testing.T) {
	got := resolve("", "", "", "", vcs("abc123", "2026-08-01T00:00:00Z", "false"))

	if got.Commit != "abc123" {
		t.Errorf("commit = %q, want %q", got.Commit, "abc123")
	}
	if got.BuildTime != "2026-08-01T00:00:00Z" {
		t.Errorf("build time = %q, want the vcs.time", got.BuildTime)
	}
	if got.Version != "" {
		t.Errorf("version = %q, want empty (nothing supplied one)", got.Version)
	}
}

// TestResolve_UnknownVCSModifiedIsNotClean: a vcs.modified the toolchain
// never wrote (or wrote as something unparseable) must stay unknown. Reading
// it as "clean" would be a confident wrong answer about whether the running
// build matches its commit.
func TestResolve_UnknownVCSModifiedIsNotClean(t *testing.T) {
	got := resolve("dev", "none", "unknown", "", []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc123"},
		{Key: "vcs.modified", Value: "maybe"},
	})
	if got.Dirty != nil {
		t.Errorf("dirty = %v, want nil for an unparseable vcs.modified", *got.Dirty)
	}
}

// TestCurrent_FillsToolchainFacts: Resolve's public wrapper adds the facts
// that need no build stamping at all, and they must be this process's real
// ones.
func TestCurrent_FillsToolchainFacts(t *testing.T) {
	got := Resolve("dev", "none", "unknown")

	if got.GoVersion != runtime.Version() {
		t.Errorf("go version = %q, want %q", got.GoVersion, runtime.Version())
	}
	if got.OS != runtime.GOOS {
		t.Errorf("os = %q, want %q", got.OS, runtime.GOOS)
	}
	if got.Arch != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", got.Arch, runtime.GOARCH)
	}
}

// TestResolve_AgreesWithThisBinarysOwnBuildInfo pins the public wrapper to
// debug.ReadBuildInfo rather than to a hand-built settings slice.
//
// Honest limitation: `go test` does NOT stamp vcs.* into the test binary (nor
// does `go run`) — only `go build`/`go install` do. So on the usual CI run
// `want` is "" and this asserts the property that still matters here: an
// unstamped binary reports an EMPTY commit, never the "none" placeholder. The
// toolchain's setting-key names were verified empirically against a real
// `go build` of this module (plain and `-trimpath`), and a resolver reading
// the wrong key fails the table cases above, which supply the toolchain's own
// key strings.
//
// Note what is deliberately NOT claimed here any more: that the stamped VALUES
// are right inside a git worktree. They are not, when the worktree is nested
// in its parent clone — see build_stamp_test.go (#1686).
func TestResolve_AgreesWithThisBinarysOwnBuildInfo(t *testing.T) {
	var want string
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				want = s.Value
			}
		}
	}
	got := Resolve("dev", "none", "unknown")
	if got.Commit != want {
		t.Errorf("Resolve().Commit = %q, want this binary's vcs.revision %q", got.Commit, want)
	}
	for _, bad := range []string{"none", "dev", "unknown", "(devel)"} {
		if got.Commit == bad {
			t.Errorf("Resolve().Commit = %q — a placeholder reached the wire", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// #1686: the link-time dirty stamp.
//
// Dirty had exactly one source — the toolchain's vcs.modified — and in a
// nested git worktree the toolchain answers for the WRONG TREE (see
// build_stamp_test.go for why). A build driver that asks git itself gets the
// right answer but had nowhere to put it. These cases pin the fourth source
// and its precedence.
// ---------------------------------------------------------------------------

// TestResolve_LinkerDirtyOverridesTheToolchainStamp: when a build driver
// stamped the answer, it wins. It asked git in the directory being built;
// vcs.modified is the toolchain's answer from a directory it may have
// mis-resolved to the enclosing clone.
func TestResolve_LinkerDirtyOverridesTheToolchainStamp(t *testing.T) {
	// The issue's exact shape: clean worktree, dirty parent clone.
	got := resolve("dev", "none", "unknown", "false",
		vcs("3fbe705a223a71bcd1471f097d78adad7aba735b", "2026-08-02T15:35:24Z", "true"))

	if got.Dirty == nil {
		t.Fatal("dirty = nil; the build driver stamped an answer")
	}
	if *got.Dirty {
		t.Error("dirty = true, want false — the tree that was built was clean; vcs.modified described the enclosing clone")
	}

	// And the other direction, so this is precedence and not a hard-coded false.
	if got := resolve("dev", "none", "unknown", "true", vcs("abc", "", "false")); got.Dirty == nil || !*got.Dirty {
		t.Errorf("dirty = %s, want true — the stamp said the built tree was dirty", showDirty(got.Dirty))
	}
}

// TestResolve_UnstampedDirtyFallsBackToVCS: every build path that does NOT
// stamp (goreleaser today, a bare `go build`, `go test`) must keep the
// pre-existing behaviour. An empty stamp is "nobody said", not "clean".
func TestResolve_UnstampedDirtyFallsBackToVCS(t *testing.T) {
	for _, ld := range []string{"", "   ", "maybe", "1"} {
		got := resolve("dev", "none", "unknown", ld, vcs("abc", "", "true"))
		if got.Dirty == nil || !*got.Dirty {
			t.Errorf("ldDirty=%q: dirty = %s, want the vcs.modified value (true)", ld, showDirty(got.Dirty))
		}
	}
}

// TestResolve_LinkerDirtyWithoutAnyVCSStamp: the Docker path has ldflags and
// no vcs.* at all. If a build there ever stamps dirty, it must be reported —
// this is the one source that can answer where the toolchain cannot.
func TestResolve_LinkerDirtyWithoutAnyVCSStamp(t *testing.T) {
	got := resolve("v0.1.0", "cafebabe", "2026-07-01T09:00:00Z", "true", nil)
	if got.Dirty == nil {
		t.Fatal("dirty = nil, want true — the stamp is the only source here")
	}
	if !*got.Dirty {
		t.Error("dirty = false, want true")
	}
}

// TestResolve_UsesThePackagesOwnLinkerVar closes the loop between the pure
// core and the exported entry point: a test that only exercised resolve()
// would pass even if Resolve never read the injected var, leaving every real
// binary unstamped.
func TestResolve_UsesThePackagesOwnLinkerVar(t *testing.T) {
	prev := buildDirty
	t.Cleanup(func() { buildDirty = prev })

	buildDirty = "true"
	if got := Resolve("dev", "none", "unknown"); got.Dirty == nil || !*got.Dirty {
		t.Errorf("Resolve ignored the injected buildDirty: dirty = %s", showDirty(got.Dirty))
	}
	buildDirty = "false"
	if got := Resolve("dev", "none", "unknown"); got.Dirty == nil || *got.Dirty {
		t.Errorf("Resolve ignored the injected buildDirty: dirty = %s", showDirty(got.Dirty))
	}
}
