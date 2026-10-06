package testutil

// Source-tree cache identity includes the full in-module migration dependency
// closure. fixturecache.go owns immutable publication and PID-owned fixtures;
// retain only the hashing helpers from the previous cache implementation.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	sharedTemplateModule = "github.com/crewship-ai/crewship"
	sharedTemplateFormat = "v1"
)

// findModuleRoot walks up from the working directory (go test runs each binary
// in its package directory) to this module's go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if goModModule(b) == sharedTemplateModule {
				return dir, nil
			}
			return "", fmt.Errorf("go.mod at %s is not %s", dir, sharedTemplateModule)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("module root not found")
		}
		dir = parent
	}
}

func goModModule(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// sharedTemplateKey hashes everything that can change the migrated schema.
//
// internal/ holds database's whole in-module dependency closure, so every
// non-test .go file there is included (with its path), plus every non-Go file
// under internal/database, which is where the embedded migration SQL and
// builtin YAML live. Test files and testdata/ are excluded: they cannot change
// what database.Migrate does, and including them would make the key churn on
// every test edit — and on files tests create while other binaries hash.
func sharedTemplateKey(root string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "format=%s\ngo=%s\n", sharedTemplateFormat, runtime.Version())
	for _, name := range []string{"go.mod", "go.sum"} {
		if err := hashFile(h, root, filepath.Join(root, name)); err != nil {
			return "", err
		}
	}
	internal := filepath.Join(root, "internal")
	dbDir := filepath.Join(internal, "database") + string(filepath.Separator)
	err := filepath.WalkDir(internal, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == "testdata" || name == "node_modules" || (strings.HasPrefix(name, ".") && path != internal) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		isGo := strings.HasSuffix(name, ".go")
		inDB := strings.HasPrefix(path, dbDir)
		if !isGo && !(inDB && sharedTemplateEmbedExt(name)) {
			return nil
		}
		return hashFile(h, root, path)
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

// sharedTemplateEmbedExt lists the non-Go file kinds internal/database embeds.
// SQLite files a test might leave in the tree are deliberately not on it.
func sharedTemplateEmbedExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".sql", ".yaml", ".yml", ".json", ".tmpl", ".txt", ".md":
		return true
	}
	return false
}

func hashFile(h io.Writer, root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	fmt.Fprintf(h, "%s\x00%x\n", filepath.ToSlash(rel), sum)
	return nil
}
