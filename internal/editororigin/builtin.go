package editororigin

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"path"
)

// Packages are immutable, reviewed application code shipped with this binary.
// They pass exactly the same pin/file verification and CSP as external bundles.
//
//go:embed packages/*/*
var compiledPackages embed.FS

func (h *Handler) loadCompiledPackages() error {
	return h.loadPackages(compiledPackages, "packages")
}

// loadPackages copies verified bytes into the handler; it retains no filesystem
// or lazy reader. The caller supplies only immutable release-owned packages.
func (h *Handler) loadPackages(source fs.FS, root string) error {
	entries, err := fs.ReadDir(source, root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		digest := entry.Name()
		if !IsDigest(digest) || !entry.IsDir() {
			return fmt.Errorf("invalid compiled package pin")
		}
		files, err := verifyBundle(digest, func(name string, limit int) ([]byte, error) {
			f, err := source.Open(path.Join(root, digest, name))
			if err != nil {
				return nil, err
			}
			defer f.Close()
			raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
			if len(raw) > limit {
				return nil, fmt.Errorf("compiled package exceeds limit")
			}
			return raw, err
		})
		if err != nil {
			return err
		}
		extra := ""
		for _, hash := range inlineScriptHashes(files["index.html"].body) {
			extra += " " + hash
		}
		h.bundles[digest] = bundle{files: files, inline: extra}
	}
	return nil
}
