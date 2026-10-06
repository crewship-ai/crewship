package testutil

// migrateddb_shared.go makes the migrated template one-per-source-tree instead
// of one-per-test-binary.
//
// Why
// ---
// buildMigratedTemplate used to run database.Migrate once in every test binary.
// That is cheap alone and ruinous in CI: `go test -race ./...` starts dozens of
// binaries at once, every one replays the whole migration chain through
// modernc's race-instrumented SQLite at the same moment, and they fight each
// other for the same cores. In main run 37455853260 the first DB-backed test of
// almost every package took 100–156 s (internal/server's
// TestStart_AttachmentBlobGC_ReclaimsAnOrphanedBlob 106 s,
// internal/restrictedworkflow 141 s, internal/restricteddispatch 156 s, ...)
// while the same test's own work is well under a second. The 100 s was the
// template, paid N times concurrently.
//
// How
// ---
// The template is stored under os.TempDir() keyed by a hash of everything that
// can change what database.Migrate produces: the Go toolchain, go.mod/go.sum,
// and the module's non-test sources under internal/ (database's closure lives
// entirely there — the migration SQL, the Go-func migrations and every package
// they import). One process builds it under an exclusive flock; the rest wait
// on the lock, verify the file against its recorded SHA-256 and copy it into a
// private directory. The private copy is what the process hands out, so a test
// that writes to MigratedTemplatePath (documented as forbidden) still only
// damages its own process, exactly as before.
//
// Safety
// ------
//   - A stale template needs the key to miss a change that alters the schema.
//     The key covers every non-test source file the migration code could
//     depend on; a CI runner additionally starts with an empty temp dir.
//     TestMigratedDB_SchemaMatchesMigrateRunner still compares the template
//     with a fresh database.Migrate run, so a key that ever missed something
//     fails that test rather than every fixture silently drifting.
//   - Any error on the shared path returns an error and the caller falls back
//     to the old private build. Set CREWSHIP_TEST_SHARED_TEMPLATE=0 to force
//     that path.
//   - Entries unused for a week are pruned by the next builder, under each
//     entry's own lock, so a template is never deleted while being read.

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
	"time"
)

const (
	sharedTemplateModule  = "github.com/crewship-ai/crewship"
	sharedTemplateDirName = "crewship-migrated-templates"
	// Bump when the template's build recipe changes in a way the source hash
	// cannot see (it lives in this package, which is hashed too, but saying it
	// is cheaper than reasoning about it).
	sharedTemplateFormat = "v1"
	sharedTemplateMaxAge = 7 * 24 * time.Hour
)

var errSharedTemplateDisabled = errors.New("shared migrated template disabled")

// sharedMigratedTemplate returns the path of a process-private copy of the
// shared template for the current source tree, building the shared entry first
// if no process has yet.
func sharedMigratedTemplate() (string, error) {
	if os.Getenv("CREWSHIP_TEST_SHARED_TEMPLATE") == "0" || !sharedTemplateLockSupported {
		return "", errSharedTemplateDisabled
	}
	root, err := findModuleRoot()
	if err != nil {
		return "", err
	}
	key, err := sharedTemplateKey(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), sharedTemplateDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	entry := filepath.Join(dir, key+".db")

	unlock, err := lockFile(entry+".lock", true)
	if err != nil {
		return "", err
	}
	defer unlock()

	if !sharedTemplateValid(entry) {
		if err := buildSharedTemplate(dir, entry); err != nil {
			return "", err
		}
		pruneSharedTemplates(dir, entry)
	} else {
		// Mark as recently used so pruning keeps it.
		now := time.Now()
		_ = os.Chtimes(entry, now, now)
	}

	private, err := os.MkdirTemp("", "crewship-migrated-template-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(private, "template.db")
	if err := copyFileForTest(entry, path); err != nil {
		_ = os.RemoveAll(private)
		return "", err
	}
	// The copy is verified too: a template that does not hash to what its
	// builder recorded must never reach a fixture.
	if sum, err := fileSHA256(path); err != nil || sum != readSum(entry) {
		_ = os.RemoveAll(private)
		return "", fmt.Errorf("private template copy does not match the shared entry")
	}
	return path, nil
}

// buildSharedTemplate migrates into a scratch file inside dir and publishes it
// with rename, then records its checksum. Called with the entry lock held.
func buildSharedTemplate(dir, entry string) error {
	_ = os.Remove(entry)
	_ = os.Remove(entry + ".sha256")
	scratch, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	tmp := filepath.Join(scratch, "template.db")
	if err := migrateTemplateAt(tmp); err != nil {
		return err
	}
	sum, err := fileSHA256(tmp)
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o400); err != nil {
		return err
	}
	if err := os.Rename(tmp, entry); err != nil {
		return err
	}
	sumTmp := filepath.Join(scratch, "template.sha256")
	if err := os.WriteFile(sumTmp, []byte(sum+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(sumTmp, entry+".sha256")
}

// sharedTemplateValid reports whether entry exists and still hashes to the
// checksum its builder recorded. Anything else — missing file, missing or
// unreadable checksum, a mismatch from a writer that ignored the read-only
// mode — means rebuild.
func sharedTemplateValid(entry string) bool {
	want := readSum(entry)
	if want == "" {
		return false
	}
	got, err := fileSHA256(entry)
	return err == nil && got == want
}

func readSum(entry string) string {
	b, err := os.ReadFile(entry + ".sha256")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pruneSharedTemplates removes entries other than keep that have not been used
// for sharedTemplateMaxAge. Each is removed only while holding its own lock
// (non-blocking — an entry somebody holds is in use, so it is skipped).
func pruneSharedTemplates(dir, keep string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.db"))
	for _, entry := range matches {
		if entry == keep {
			continue
		}
		info, err := os.Stat(entry)
		if err != nil || time.Since(info.ModTime()) < sharedTemplateMaxAge {
			continue
		}
		unlock, err := lockFile(entry+".lock", false)
		if err != nil {
			continue
		}
		_ = os.Remove(entry)
		_ = os.Remove(entry + ".sha256")
		_ = os.Remove(entry + ".lock")
		unlock()
	}
}

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
