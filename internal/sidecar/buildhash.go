package sidecar

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

// selfExeHash returns a short content hash of the sidecar binary this process
// is running. It is advertised on /health so the server can detect a container
// still serving a STALE bind-mounted sidecar after a redeploy (#1008): a bind
// mount pins the inode the container started with, so a running sidecar keeps
// executing the OLD binary while the host file at the same path is already the
// NEW one. Hashing os.Executable() reflects the actual running inode, which is
// the ground truth for that comparison.
//
// Memoized: the running binary never changes for the life of the process, so
// hashing it once is enough. Returns "" if the executable can't be read — the
// server treats an empty hash as "unknown" and never raises a false stale
// alarm (fail-open).
var selfExeHash = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		return ""
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
	return hex.EncodeToString(h.Sum(nil))[:12]
})
