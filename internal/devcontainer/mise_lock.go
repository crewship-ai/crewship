package devcontainer

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxMiseLockFiles     = 128
	maxMiseLockFileBytes = 128 << 10
	maxMiseLockBytes     = 512 << 10
)

// MiseLockBundle preserves the entire native mise lock input, including
// auxiliary npm/aube files. It is build input, not observed CLI metadata.
// Values are UTF-8 file contents; paths are relative to the config directory.
type MiseLockBundle struct {
	SchemaVersion int               `json:"schema_version" yaml:"schema_version"`
	Files         map[string]string `json:"files" yaml:"files"`
}

var miseLockPathPart = regexp.MustCompile(`^[A-Za-z0-9_.~+-]+$`)

func validMiseLockPath(name string) bool {
	if name == "mise.lock" {
		return true
	}
	if len(name) > 256 || !strings.HasPrefix(name, ".mise/locks/") || path.Clean(name) != name {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) < 4 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !miseLockPathPart.MatchString(part) {
			return false
		}
	}
	return true
}

func (b *MiseLockBundle) Validate() error {
	if b == nil {
		return nil
	}
	if b.SchemaVersion != 1 {
		return fmt.Errorf("mise lock: unsupported bundle schema")
	}
	if len(b.Files) == 0 || len(b.Files) > maxMiseLockFiles {
		return fmt.Errorf("mise lock: expected 1-%d files", maxMiseLockFiles)
	}
	if strings.TrimSpace(b.Files["mise.lock"]) == "" {
		return fmt.Errorf("mise lock: mise.lock is required")
	}
	total := 0
	for name, content := range b.Files {
		if !validMiseLockPath(name) {
			return fmt.Errorf("mise lock: invalid relative file path")
		}
		if len(content) > maxMiseLockFileBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return fmt.Errorf("mise lock: invalid or oversized text file")
		}
		total += len(content)
		if total > maxMiseLockBytes {
			return fmt.Errorf("mise lock: bundle exceeds %d bytes", maxMiseLockBytes)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := b.Files[parent]; exists {
				return fmt.Errorf("mise lock: file conflicts with a parent directory")
			}
		}
	}
	return nil
}

// writeMiseLock uses fixed destinations inside the build container. It never
// extracts a user archive or writes the host filesystem. Small base64 chunks
// avoid shell interpolation and OS argument-length limits for large locks.
func writeMiseLock(ctx context.Context, containerID string, b *MiseLockBundle, exec ExecFunc) error {
	if err := b.Validate(); err != nil {
		return err
	}
	names := make([]string, 0, len(b.Files))
	for name := range b.Files {
		names = append(names, name)
	}
	slices.Sort(names)
	run := func(script string) error {
		_, code, err := exec(ctx, containerID, []string{"sh", "-c", script}, "0:0", nil)
		if err != nil {
			return fmt.Errorf("mise lock: write bundle: %w", err)
		}
		if code != 0 {
			return fmt.Errorf("mise lock: write bundle exited %d", code)
		}
		return nil
	}
	// Remove stale auxiliary files from a previous build only after checking
	// their controlled parent chain. A partial bundle must not borrow hidden
	// files from the base image.
	if err := run("set -eu; test ! -L /opt; test ! -L /opt/mise; test ! -L /opt/mise/config; test ! -L /opt/mise/config/.mise; rm -rf /opt/mise/config/.mise/locks; rm -f /opt/mise/config/mise.lock"); err != nil {
		return err
	}
	for _, name := range names {
		dest := path.Join(miseRoot, "config", name)
		// Reject inherited symlinks in each parent before creating or truncating.
		parts := strings.Split(strings.TrimPrefix(dest, "/"), "/")
		var script strings.Builder
		script.WriteString("set -eu; ")
		current := ""
		for i, part := range parts {
			current += "/" + part
			fmt.Fprintf(&script, "test ! -L '%s'; ", current)
			if i < len(parts)-1 {
				fmt.Fprintf(&script, "mkdir -p '%s'; ", current)
			}
		}
		fmt.Fprintf(&script, ": > '%s'; chmod 0600 '%s'", dest, dest)
		if err := run(script.String()); err != nil {
			return err
		}
		content := b.Files[name]
		for len(content) > 0 {
			n := min(len(content), 24<<10)
			encoded := base64.StdEncoding.EncodeToString([]byte(content[:n]))
			if err := run("printf '%s' '" + encoded + "' | base64 -d >> '" + dest + "'"); err != nil {
				return err
			}
			content = content[n:]
		}
	}
	return nil
}
