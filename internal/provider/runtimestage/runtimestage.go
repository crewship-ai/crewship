// Package runtimestage puts the mandatory crew bind sources somewhere the
// container runtime can actually see them (#1706, #1724).
//
// Every crew container bind-mounts crewship-sidecar and entrypoint.sh, and a
// bind source is a string the RUNTIME resolves, not this process. On a native
// Linux daemon those are the same filesystem and the distinction never
// surfaces. On a VM-backed runtime — Colima, Rancher Desktop, Docker Desktop,
// podman machine, Apple Containers — the runtime lives inside a VM that shares
// only a configured set of host directories, and a bind source outside that set
// does not exist as far as it is concerned.
//
// That breaks the most common install outright. Both artifacts are MANDATORY
// binds, and internal/config resolves them next to the crewship executable:
// Homebrew puts that at /opt/homebrew/..., install.sh at /usr/local/bin. Colima
// shares only $HOME by default, so neither exists in its VM and every crew
// create fails with `bind source path does not exist`. The operator is told a
// file is missing when it is plainly there.
//
// Artifacts copies both under the crew data dir so the provider can bind the
// copies. The data dir already has to be visible to the runtime — /workspace,
// /output and /crew are bind-mounted out of it, so nothing works if it is not —
// which collapses two separate "this path must be shared" requirements into the
// one that was already load-bearing. On the default install
// ($HOME/.crewship/output) that is inside Colima's default share and a
// Homebrew-installed crewship starts crews on a default Colima with no
// configuration at all.
//
// It lives in its own package rather than in either provider because both the
// docker and the apple provider need exactly this, down to the mtime-preserving
// copy, and the two Config types have nothing else in common. Providers keep
// their own thin wrapper that maps their Config onto Artifacts; what must not be
// re-derived per provider is the copy semantics below.
//
// Deliberately no tests of its own: the contract is pinned twice over, through
// each wrapper, in internal/provider/docker/runtime_binds_test.go and
// internal/provider/apple/runtime_binds_test.go. A third in-package copy of the
// same six assertions would be the one nobody updates.
package runtimestage

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// DirName is the subdirectory of the crew data dir that holds the
// bind-mountable copies. Dot-prefixed so it can never collide with a crew id
// (cuid2, never leading-dot) and never with the reserved "crews"/"workspaces"/
// "secrets" subtrees the providers own. It is NOT mounted into any container —
// only the files inside it are, read-only, at their existing /usr/local/bin
// targets — so it stays out of reach of every agent.
const DirName = ".runtime"

// SidecarFileName and EntrypointFileName are the staged copies' basenames.
// Fixed rather than derived from the source path so a restart re-stages onto
// the same inode's name whatever the install layout was.
const (
	SidecarFileName    = "crewship-sidecar"
	EntrypointFileName = "entrypoint.sh"
)

// Artifacts returns the paths a provider should bind for the sidecar and the
// entrypoint: copies under outputBase/.runtime when staging worked, and the
// supplied install paths unchanged when it did not.
//
// Unconditional rather than "only when the runtime looks VM-backed". There is no
// API that enumerates a VM's share set, so any conditional version is a guess
// about which runtimes need it — which is exactly how #1706 stayed invisible to
// everyone developing on OrbStack. One path, exercised on every runtime, beats a
// fast path that only three of them take.
//
// Best-effort: any failure returns the inputs untouched and logs why, because a
// copy error must degrade to the previous behaviour (which works whenever the
// runtime shares this process's filesystem) rather than take the provider down.
func Artifacts(outputBase, sidecarPath, entrypointPath string, logger *slog.Logger) (stagedSidecar, stagedEntrypoint string) {
	if logger == nil {
		logger = slog.Default()
	}
	if outputBase == "" {
		// No data dir to stage into — a hand-built provider (tests) or an
		// embedding that never persists crew state. Leave the paths alone.
		return sidecarPath, entrypointPath
	}
	dir := filepath.Join(outputBase, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warn("could not create the runtime staging dir; crew containers will bind the sidecar and entrypoint from their install location, which a VM-backed runtime may not be able to see (#1706)",
			"dir", dir, "error", err)
		return sidecarPath, entrypointPath
	}

	if staged, err := stageArtifact(sidecarPath, filepath.Join(dir, SidecarFileName)); err != nil {
		logger.Warn("could not stage crewship-sidecar next to the crew data dirs; binding it from its install path instead (#1706)",
			"source", sidecarPath, "error", err)
	} else if staged != "" {
		logger.Info("staged crewship-sidecar for bind-mounting",
			"install_path", sidecarPath, "bind_source", staged)
		sidecarPath = staged
	}

	if staged, err := stageArtifact(entrypointPath, filepath.Join(dir, EntrypointFileName)); err != nil {
		logger.Warn("could not stage entrypoint.sh next to the crew data dirs; binding it from its install path instead (#1706)",
			"source", entrypointPath, "error", err)
	} else if staged != "" {
		entrypointPath = staged
	}
	return sidecarPath, entrypointPath
}

// stageArtifact copies src to dst and returns dst. An empty src is not an
// error — the caller's config simply has no such artifact — and returns "".
//
// The copy is atomic (temp file + rename) so two servers sharing a data dir
// cannot bind a half-written sidecar, and it PRESERVES the source's mtime:
// docker's assertSidecarFreshAtStartup compares the sidecar's mtime against the
// server binary's to catch a deploy that updated one and not the other, and a
// copy that stamped "now" would silence that check on every single boot.
func stageArtifact(src, dst string) (string, error) {
	if src == "" {
		return "", nil
	}
	if same, err := sameFile(src, dst); err == nil && same {
		// Already the staged copy (a re-entrant call, or an operator who
		// pinned CREWSHIP_SIDECAR_PATH at it). Copying a file onto itself
		// would truncate it.
		return dst, nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if alreadyStaged(info, dst) {
		// Same size and same mtime: the previous boot already staged this exact
		// artifact. Skipping is not only about the ~20 MB copy — the rename
		// swaps the inode under any crew container currently bind-mounting it,
		// which is harmless (the mount holds the old inode) but pointless
		// churn on every single restart.
		return dst, nil
	}
	in, err := os.Open(src) // #nosec G304 — src is an operator-configured artifact path
	if err != nil {
		return "", err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".stage-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeded

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", err
	}
	if err := os.Chtimes(tmpName, info.ModTime(), info.ModTime()); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// alreadyStaged reports whether dst is a copy of the file src describes.
//
// Size and mtime, because mtime is what the staging contract already promises
// to carry across (assertSidecarFreshAtStartup reads it) — a dst that matches on
// both is a copy this function made from a src that has not changed since. It
// deliberately does NOT hash: a content hash of the sidecar on every boot buys
// nothing over the pair, since a rebuild that produced identical bytes needs no
// re-copy either.
func alreadyStaged(src os.FileInfo, dst string) bool {
	di, err := os.Stat(dst)
	if err != nil {
		return false
	}
	return di.Size() == src.Size() && di.ModTime().Equal(src.ModTime())
}

// sameFile reports whether two paths are the same file on disk.
func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}
