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

// TestResolve_DevShBuildReportsTheVCSCommit is the case the issue was filed
// for. dev.sh line 367 runs `go build -o "$binary" ./cmd/crewship` with no
// -ldflags at all, so main.version/commit/date keep their in-source defaults
// "dev"/"none"/"unknown" — while the Go toolchain still stamps vcs.* into the
// binary. Empirically verified on this repo: a plain `go build` (and a
// `-trimpath` one) both carry vcs.revision/vcs.time/vcs.modified.
func TestResolve_DevShBuildReportsTheVCSCommit(t *testing.T) {
	got := resolve("dev", "none", "unknown",
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
	got := resolve("v0.1.0-beta.4", "cafebabecafebabecafebabecafebabecafebabe", "2026-07-01T09:00:00Z",
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
	got := resolve("v0.1.0-beta.4", "cafebabe", "2026-07-01T09:00:00Z", nil)

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
	got := resolve("dev", "none", "unknown", nil)

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
	got := resolve("", "", "", vcs("abc123", "2026-08-01T00:00:00Z", "false"))

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
	got := resolve("dev", "none", "unknown", []debug.BuildSetting{
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
// `go build` of this module (plain and `-trimpath`, inside a git worktree),
// and a resolver reading the wrong key fails the table cases above, which
// supply the toolchain's own key strings.
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
