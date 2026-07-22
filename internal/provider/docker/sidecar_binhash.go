package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"sync"
)

// buildExpectedSidecarHash is injected at LINK time (#1160 ask 2) with the
// content hash (sha256, first 12 hex chars) of the crewship-sidecar binary
// built alongside this server binary:
//
//	-ldflags "-X github.com/crewship-ai/crewship/internal/provider/docker.buildExpectedSidecarHash=<hash>"
//
// The Makefile computes it from the freshly built ./crewship-sidecar (the
// build:go / build targets depend on build:sidecar, so the file always
// exists first). When set, it is the AUTHORITATIVE expected hash: hashing
// the host's on-disk binary at runtime can only detect a running sidecar
// that is older than the on-disk file — it is blind to ARTIFACT staleness,
// where a deploy updated the server but forgot `make build:sidecar` + copy,
// so the on-disk file is just as old as the running one (old-vs-old, the
// known dev2 staging gotcha). Comparing running sidecars against the
// build-time hash covers both classes.
//
// Empty (dev `go run`/air, `go test`, plain `go build`, container image
// builds that don't inject it) → detection falls back to the on-disk hash,
// i.e. exactly the pre-#1160 behavior. Malformed values are ignored the same
// way, so a broken build flag can never turn into a fleet-wide false alarm.
var buildExpectedSidecarHash string

// normalizedBuildSidecarHash returns the injected build-time hash trimmed and
// lowercased, or "" when unset or not exactly 12 lowercase hex chars
// (fail-open on malformed injection).
func normalizedBuildSidecarHash() string {
	h := buildExpectedSidecarHash
	// Trim ASCII whitespace without pulling in strings just for this.
	for len(h) > 0 && (h[0] == ' ' || h[0] == '\t' || h[0] == '\n' || h[0] == '\r') {
		h = h[1:]
	}
	for len(h) > 0 && (h[len(h)-1] == ' ' || h[len(h)-1] == '\t' || h[len(h)-1] == '\n' || h[len(h)-1] == '\r') {
		h = h[:len(h)-1]
	}
	if len(h) != 12 {
		return ""
	}
	out := make([]byte, 12)
	for i := 0; i < 12; i++ {
		c := h[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			out[i] = c
		case c >= 'A' && c <= 'F':
			out[i] = c + ('a' - 'A')
		default:
			return ""
		}
	}
	return string(out)
}

// sidecarBinHashEntry memoizes a computed binary hash keyed by the file's
// (mtime, size) so a redeploy that rewrites the sidecar invalidates it.
type sidecarBinHashEntry struct {
	mtime int64
	size  int64
	hash  string
}

// sidecarBinHashCache is process-wide (path → entry). SidecarBinaryPath is one
// value per process in practice; keying by path keeps it correct if it varies.
var sidecarBinHashCache sync.Map

// staleArtifactWarned dedupes the on-disk-vs-build-hash divergence warning per
// (path, on-disk hash, build hash). ExpectedSidecarHash runs on every agent
// exec's sidecar health check, so an un-deduped warning would spam every run;
// a redeploy that rewrites the file changes the on-disk hash and legitimately
// re-warns (or goes quiet) exactly once.
var staleArtifactWarned sync.Map

// ExpectedSidecarHash returns a short content hash of the crewship-sidecar
// binary this provider expects agent containers to be running. The
// orchestrator compares it against the hash a running sidecar advertises on
// /health to detect a container serving a STALE sidecar after a redeploy
// (#1008).
//
// When a build-time hash was injected via ldflags (see
// buildExpectedSidecarHash), that value wins: it also catches ARTIFACT
// staleness — an on-disk sidecar that was never rebuilt/recopied for this
// deploy — which on-disk hashing compares old-vs-old and misses (#1160).
// In that case the on-disk divergence is additionally logged once per
// (path, hash) pair, because `crewship crew restart-agents` alone cannot fix
// a stale artifact: new containers would just mount the same old file.
//
// Without an injected hash it hashes the on-disk binary and fails open —
// returns "" — when the path is unset or unreadable, so detection never
// raises a false alarm on an unknown binary. The result matches
// internal/sidecar.selfExeHash (sha256 of the same bytes, first 12 hex
// chars). The on-disk value is memoized by the file's (mtime, size); a
// redeploy that rewrites the binary changes those and forces a re-hash.
func (p *Provider) ExpectedSidecarHash() string {
	onDisk := p.onDiskSidecarHash()
	build := normalizedBuildSidecarHash()
	if build == "" {
		return onDisk
	}
	if onDisk != "" && onDisk != build {
		p.warnStaleSidecarArtifact(onDisk, build)
	}
	return build
}

// onDiskSidecarHash hashes the sidecar binary at cfg.SidecarBinaryPath
// (memoized by mtime+size). "" when the path is unset or unreadable.
func (p *Provider) onDiskSidecarHash() string { return SidecarFileHash(p.cfg.SidecarBinaryPath) }

// ExpectedSidecarHashFromBuild exposes the build-time injected hash (see
// buildExpectedSidecarHash) to callers that have no Provider — notably
// `crewship doctor`, which compares the sidecar binary it finds on disk
// against the one this binary was built alongside without needing a live
// docker daemon. Returns "" when no hash was injected or the injected value
// is malformed, i.e. the same fail-open contract the provider path uses.
func ExpectedSidecarHashFromBuild() string { return normalizedBuildSidecarHash() }

// SidecarFileHash returns the short content hash of the sidecar binary at
// path, in the same format the sidecar advertises on /health and the Makefile
// injects at link time (sha256, first 12 hex chars). "" when path is empty or
// unreadable — callers must treat that as "unknown", never as a mismatch.
//
// Exported so doctor hashes the binary through the same code path the
// provider does; a second implementation would be one refactor away from
// disagreeing and alarming on every healthy deploy.
func SidecarFileHash(path string) string {
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mtime, size := fi.ModTime().UnixNano(), fi.Size()
	if v, ok := sidecarBinHashCache.Load(path); ok {
		if e := v.(sidecarBinHashEntry); e.mtime == mtime && e.size == size {
			return e.hash
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	hash := hex.EncodeToString(h.Sum(nil))[:12]
	sidecarBinHashCache.Store(path, sidecarBinHashEntry{mtime: mtime, size: size, hash: hash})
	return hash
}

// warnStaleSidecarArtifact logs (once per path+hash pair) that the on-disk
// sidecar binary does not match the one baked in at build time — the deploy
// itself shipped a stale artifact, so the fix is rebuilding/recopying
// crewship-sidecar (`make build:sidecar`), NOT just restarting agents.
func (p *Provider) warnStaleSidecarArtifact(onDisk, build string) {
	key := p.cfg.SidecarBinaryPath + "\x00" + onDisk + "\x00" + build
	if _, loaded := staleArtifactWarned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log := p.logger
	if log == nil {
		log = slog.Default()
	}
	log.Error("stale sidecar ARTIFACT detected: the on-disk crewship-sidecar does not match the hash baked into this server binary at build time — this deploy updated the server but not the sidecar (missing 'make build:sidecar' + copy?); rebuild and redeploy crewship-sidecar, then run 'crewship crew restart-agents' — restarting alone would remount the same stale file",
		"sidecar_path", p.cfg.SidecarBinaryPath,
		"on_disk_sidecar_hash", onDisk,
		"build_expected_sidecar_hash", build,
	)
}
