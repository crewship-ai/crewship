package editororigin

import (
	"embed"
	"fmt"
)

// Packages are immutable, reviewed application code shipped with this binary.
// They pass exactly the same pin/file verification and CSP as external bundles.
//
//go:embed packages/*/*
var compiledPackages embed.FS

func (h *Handler) loadCompiledPackages() error {
	entries, e := compiledPackages.ReadDir("packages")
	if e != nil {
		return e
	}
	for _, entry := range entries {
		digest := entry.Name()
		if !IsDigest(digest) || !entry.IsDir() {
			return fmt.Errorf("invalid compiled package pin")
		}
		files, e := verifyBundle(digest, func(name string, limit int) ([]byte, error) {
			raw, e := compiledPackages.ReadFile("packages/" + digest + "/" + name)
			if len(raw) > limit {
				return nil, fmt.Errorf("compiled package exceeds limit")
			}
			return raw, e
		})
		if e != nil {
			return e
		}
		extra := ""
		for _, hash := range inlineScriptHashes(files["index.html"].body) {
			extra += " " + hash
		}
		h.bundles[digest] = bundle{files: files, inline: extra}
	}
	return nil
}
