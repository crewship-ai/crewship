// Package buildinfo answers one question: which build is this?
//
// Nothing could answer it for a *running server* before #1645, which is how a
// dev slot sat a full day behind main unnoticed and how a missing API field
// got misdiagnosed as "the server is running old code" when the stale binary
// was actually the local CLI.
//
// The subtlety is that the answer has two sources and neither covers every
// build path this repo has:
//
//	build path                     ldflags            vcs.* stamped
//	-----------------------------  -----------------  --------------
//	goreleaser (releases)          version/commit/date  yes
//	Makefile (`make build`)        version/commit/date  yes
//	Dockerfile                     ARG defaults or CI   no (.git not in context)
//	dev.sh (dev slots, `go build`) NONE                 yes
//	`go run` / `go install` proxy  NONE                 no
//
// The row that matters is the last-but-one: dev.sh (line 367) builds the dev
// slots with a bare `go build`, so main.version/commit/date keep their
// in-source defaults "dev"/"none"/"unknown". A build-identity field sourced
// from ldflags alone would read "none" in the exact situation the feature
// exists for. The Go toolchain does stamp vcs.revision / vcs.time /
// vcs.modified for a plain `go build` (verified on this repo, including under
// -trimpath and inside a git worktree), so that is the fallback.
//
// Rules: ldflags win when they carry a real value; placeholders are treated as
// absent and erased rather than shipped; and "was the tree dirty" is a
// three-state answer, because a build with ldflags but no VCS stamping
// genuinely does not know.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Info is a build's identity. The JSON tags are the wire shape of the
// build fields on GET /api/v1/system/version.
type Info struct {
	// Version is the release version ("v0.1.0-beta.4") or "dev" for an
	// unstamped build. It is deliberately NOT backfilled from
	// debug.BuildInfo.Main.Version: for a `go build` that is a pseudo-version
	// ("v0.1.0-beta.4.0.20260802153524-496c8c1a84be+dirty") which
	// internal/cli's version-skew check would parse as a real release and
	// warn about.
	Version string `json:"version"`
	// Commit is the full git SHA, or "" when neither source knew one.
	Commit string `json:"commit"`
	// BuildTime is RFC 3339 as produced by goreleaser/Make, or the commit
	// time for a VCS-stamped build. "" when unknown.
	BuildTime string `json:"build_time"`
	// Dirty reports whether the working tree carried uncommitted changes at
	// build time. nil means nothing stamped it — which is a different fact
	// from "clean" and must not be flattened into one.
	Dirty *bool `json:"dirty"`

	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// placeholders are the in-source defaults of main.version/commit/date (see
// cmd/crewship/main.go) plus the module system's stand-in. They mean "nobody
// stamped this", so they must never be reported as an answer.
var placeholders = map[string]bool{
	"":        true,
	"dev":     true,
	"none":    true,
	"unknown": true,
	"(devel)": true,
}

func stamped(v string) string {
	v = strings.TrimSpace(v)
	if placeholders[strings.ToLower(v)] {
		return ""
	}
	return v
}

// Resolve merges the ldflags-injected strings with this binary's embedded
// VCS stamps and the running toolchain's facts.
func Resolve(ldVersion, ldCommit, ldDate string) Info {
	var settings []debug.BuildSetting
	if bi, ok := debug.ReadBuildInfo(); ok {
		settings = bi.Settings
	}
	info := resolve(ldVersion, ldCommit, ldDate, settings)
	info.GoVersion = runtime.Version()
	info.OS = runtime.GOOS
	info.Arch = runtime.GOARCH
	return info
}

// resolve is the pure core, separated so the build paths in the table above
// can be tested without actually producing five binaries.
func resolve(ldVersion, ldCommit, ldDate string, settings []debug.BuildSetting) Info {
	var vcsRevision, vcsTime, vcsModified string
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			vcsRevision = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			vcsModified = s.Value
		}
	}

	info := Info{
		// Version keeps the raw ldflags string, placeholder and all: "dev" is
		// the honest, already-understood answer for an unstamped build, and
		// every consumer (version-skew, update check) is written against it.
		Version:   strings.TrimSpace(ldVersion),
		Commit:    stamped(ldCommit),
		BuildTime: stamped(ldDate),
	}
	if info.Commit == "" {
		info.Commit = strings.TrimSpace(vcsRevision)
	}
	if info.BuildTime == "" {
		info.BuildTime = strings.TrimSpace(vcsTime)
	}

	switch strings.TrimSpace(vcsModified) {
	case "true":
		dirty := true
		info.Dirty = &dirty
	case "false":
		dirty := false
		info.Dirty = &dirty
	}

	return info
}
