package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

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

// ExpectedSidecarHash returns a short content hash of the crewship-sidecar
// binary this provider bind-mounts into agent containers. The orchestrator
// compares it against the hash a running sidecar advertises on /health to
// detect a container serving a STALE sidecar after a redeploy (#1008).
//
// The result matches internal/sidecar.selfExeHash (sha256 of the same bytes,
// first 12 hex chars). It fails open — returns "" — when the path is unset or
// unreadable, so detection never raises a false alarm on an unknown binary.
// The value is memoized by the file's (mtime, size); a redeploy that rewrites
// the binary changes those and forces a re-hash.
func (p *Provider) ExpectedSidecarHash() string {
	path := p.cfg.SidecarBinaryPath
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
