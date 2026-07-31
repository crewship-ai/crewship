package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/fsnotify/fsnotify"
)

var _ provider.StorageProvider = (*Provider)(nil)

// Provider implements StorageProvider using the local filesystem. All paths
// are resolved relative to basePath with symlink-aware path traversal protection.
type Provider struct {
	basePath string

	// mu guards closed and every watchers.Add. A sync.WaitGroup panics with
	// "reused before previous Wait has returned" if an Add lands while a Wait
	// is in flight, so a Watch starting while WaitWatchers drains must be
	// refused rather than counted.
	mu       sync.Mutex
	closed   bool
	watchers sync.WaitGroup
}

// ErrWatchersClosed is returned by Watch once WaitWatchers has been called.
// Waiting is terminal: the provider stays usable for plain file operations,
// but it accepts no new watches.
var ErrWatchersClosed = errors.New("localfs: provider watchers closed")

// New creates a local filesystem Provider rooted at basePath, creating the
// directory if it does not exist.
//
// Mode 0775 matches Write/EnsureDir so a base dir created here is also
// group-writable for sibling processes (agent uid vs crewshipd uid on
// the same shared bind-mount).
func New(basePath string) (*Provider, error) {
	if err := os.MkdirAll(basePath, 0775); err != nil {
		return nil, fmt.Errorf("create base dir %s: %w", basePath, err)
	}
	return &Provider{basePath: basePath}, nil
}

// resolve validates a caller-supplied path and returns it as a clean
// base-relative path — the form every filesystem operation in this package
// hands to an *os.Root (see openRoot).
//
// It rejects lexical traversal ("../"), and for paths that already exist it
// resolves symlinks and re-checks containment (V-09) so an in-base symlink is
// followed exactly as before while an escaping one is refused with a message
// naming the cause.
//
// resolve is the *diagnosis*, not the enforcement: a path whose leaf does not
// exist yet cannot be symlink-resolved at all, so resolve alone could never
// see a symlinked intermediate component. os.Root is what enforces
// containment for those — see openRoot.
func (p *Provider) resolve(path string) (string, error) {
	full := filepath.Join(p.basePath, filepath.Clean(path))
	rel, err := filepath.Rel(p.basePath, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path traversal detected: %s", path)
	}
	// V-09: Resolve symlinks and re-check containment to prevent symlink escape
	realBase, baseErr := filepath.EvalSymlinks(p.basePath)
	if baseErr != nil {
		return "", fmt.Errorf("resolve base path: %w", baseErr)
	}
	// Only check symlinks if the path exists (new files won't resolve)
	if realFull, evalErr := filepath.EvalSymlinks(full); evalErr == nil {
		if !strings.HasPrefix(realFull, realBase+string(os.PathSeparator)) && realFull != realBase {
			return "", fmt.Errorf("path traversal detected (symlink): %s", path)
		}
		// Re-derive the relative path from the *resolved* pair so an in-base
		// symlink keeps resolving to its real target under the root.
		if resolvedRel, relErr := filepath.Rel(realBase, realFull); relErr == nil {
			rel = resolvedRel
		}
	}
	return rel, nil
}

// resolveAbs is resolve for the one consumer that cannot take a relative path
// and a root: fsnotify watches a directory by name, not by descriptor.
func (p *Provider) resolveAbs(path string) (string, error) {
	rel, err := p.resolve(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.basePath, rel), nil
}

// openRoot resolves path and opens a traversal-resistant handle on the base
// directory. The caller operates on the returned base-relative path via the
// Root's methods and must Close the Root.
//
// Why a Root rather than os.Open(filepath.Join(base, path)): os.Root
// validates every component as it walks (one openat per component) and
// refuses any symlink that leaves the root. The previous resolve-then-join
// form left a hole for targets that do not exist yet — EvalSymlinks fails on
// a missing leaf, so the *unresolved* path was returned and os.Create would
// happily follow "a" in "a/b/new.txt" when "a" was a symlink an agent had
// planted in the shared bind-mount. Agent containers write into this tree at
// a different uid, so planting that symlink is inside the threat model.
//
// One narrowing that comes with Root: an *absolute* symlink is refused even
// when it points back inside the base. Relative in-base symlinks, and
// absolute ones on an already-existing target (resolve rewrites those to
// their real path above), keep working.
func (p *Provider) openRoot(path string) (*os.Root, string, error) {
	rel, err := p.resolve(path)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(p.basePath)
	if err != nil {
		return nil, "", fmt.Errorf("open storage root: %w", err)
	}
	return root, rel, nil
}

// Read opens the file at the given path for reading.
func (p *Provider) Read(_ context.Context, path string) (io.ReadCloser, error) {
	root, rel, err := p.openRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// Write creates or overwrites the file at path with content from r.
//
// On a shared bind-mount where files may have been created by another
// uid (e.g. the agent container at uid 1001 while crewshipd runs as
// uid 1000), os.Create can fail with EACCES on an existing file even
// though the calling process has group-write via the bind-mount setgid
// + group-shared layout. Retry path:
//  1. Best-effort chmod 0664 — opens up the file if we own it OR if
//     it's group-writable already (no-op in those cases).
//  2. If create still fails with EACCES, try unlink + create — works
//     when the parent dir is writable for our uid/gid.
func (p *Provider) Write(_ context.Context, path string, r io.Reader) error {
	root, rel, err := p.openRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(rel), 0775); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	// Reject writes that would land on a symlink. The Root already blocks
	// symlink-escape on every component, but a symlink that stays *inside*
	// the base is still a redirect this call should not follow silently
	// (agent A pointing at agent B's output). lstat (no follow) before each
	// touch and bail if the path is a symlink or non-regular.
	if st, statErr := root.Lstat(rel); statErr == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlink target: %s", path)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type at %s", path)
		}
		// Best-effort: relax mode on the existing file before re-opening
		// it for write. Ignore failures (we may not own it — Create
		// will report the real problem).
		_ = root.Chmod(rel, 0664)
	}
	f, err := root.Create(rel)
	if err != nil && os.IsPermission(err) {
		// Re-check before destructive Remove — another process could
		// have raced a symlink into place between Create and Remove.
		if st, statErr := root.Lstat(rel); statErr == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlink target: %s", path)
		}
		// Last-resort: unlink and recreate. Works when the parent dir
		// is group-writable to us. Files we recreate this way drop
		// previous ownership; the entrypoint sets umask 0002 so
		// future writes from agent-side land at 0664 instead of 0644.
		if rmErr := root.Remove(rel); rmErr == nil {
			f, err = root.Create(rel)
		}
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// List returns file entries in the given directory (non-recursive).
func (p *Provider) List(_ context.Context, dir string) ([]provider.FileInfo, error) {
	root, rel, err := p.openRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	d, err := root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var files []provider.FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, provider.FileInfo{
			Path:    filepath.Join(rel, e.Name()),
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
		})
	}
	return files, nil
}

// ListRecursive walks the directory tree and returns all file entries.
func (p *Provider) ListRecursive(_ context.Context, dir string) ([]provider.FileInfo, error) {
	root, rel, err := p.openRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	// Walking root.FS() keeps the traversal inside the Root: every entry is
	// reached through the root's descriptor, and the reported paths are
	// already base-relative (no filepath.Rel against a base whose textual
	// form may differ from its resolved one — the macOS /var → /private/var
	// drift the old code papered over).
	start := filepath.ToSlash(rel)
	var files []provider.FileInfo
	err = fs.WalkDir(root.FS(), start, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == start {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		files = append(files, provider.FileInfo{
			Path:    filepath.FromSlash(path),
			Name:    d.Name(),
			Size:    info.Size(),
			IsDir:   d.IsDir(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("walk dir %s: %w", dir, err)
	}
	return files, nil
}

// Delete removes the file or directory at path (recursively if a directory).
//
// Deleting the base directory itself is refused: a caller that reaches here
// with an empty or "." path has lost track of what it is removing, and the
// old behaviour (os.RemoveAll(basePath)) wiped every crew's output.
func (p *Provider) Delete(_ context.Context, path string) error {
	root, rel, err := p.openRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if rel == "." {
		return fmt.Errorf("refuse to delete the storage root: %s", path)
	}
	return root.RemoveAll(rel)
}

// Exists reports whether a file or directory exists at the given path.
func (p *Provider) Exists(_ context.Context, path string) (bool, error) {
	root, rel, err := p.openRoot(path)
	if err != nil {
		return false, err
	}
	defer root.Close()
	_, err = root.Stat(rel)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// EnsureDir creates the directory at path if it does not already exist.
func (p *Provider) EnsureDir(_ context.Context, path string) error {
	root, rel, err := p.openRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.MkdirAll(rel, 0775)
}

// WaitWatchers blocks until every goroutine started by Watch has observed its
// context cancellation and released the underlying fsnotify watcher.
//
// Callers that delete a watched tree must cancel, then WaitWatchers, then
// delete — never delete while the watcher is still closing. On the kqueue
// backends (macOS, *BSD) fsnotify holds one descriptor per watched file and
// closes them all inside Watcher.Close; if a flood of NOTE_DELETE arrives at
// the same time, fsnotify v1.10.1 can close the same descriptor twice (its
// readEvents→remove path drops the watches lock between the map lookup and
// the unix.Close that Close is doing concurrently). The second close lands on
// whatever descriptor the process has since opened for that number — in CI
// that was the directory fd of an unrelated parallel test's os.RemoveAll,
// which then failed with EBADF. See #1286.
// Waiting is terminal for watches: afterwards Watch returns
// ErrWatchersClosed, so a watch starting concurrently cannot be counted into
// a WaitGroup that is already being waited on.
func (p *Provider) WaitWatchers() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()

	p.watchers.Wait()
}

// Watch starts watching the directory tree for filesystem changes, sending
// events to the provided channel until ctx is cancelled. Shutdown is
// asynchronous; use WaitWatchers to block until it has completed.
func (p *Provider) Watch(ctx context.Context, dir string, events chan<- provider.FileEvent) error {
	full, err := p.resolveAbs(dir)
	if err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}

	if err := addRecursive(watcher, full); err != nil {
		watcher.Close()
		return fmt.Errorf("watch %s: %w", dir, err)
	}

	// Register under the lock so the Add cannot race WaitWatchers' Wait.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		watcher.Close()
		return ErrWatchersClosed
	}
	p.watchers.Add(1)
	p.mu.Unlock()

	go func() {
		// Done last: WaitWatchers must not release until the descriptors are
		// actually gone, so watcher.Close has to run first.
		defer p.watchers.Done()
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				fe := p.toFileEvent(event, full)
				if fe != nil {
					select {
					case events <- *fe:
					case <-ctx.Done():
						return
					}
				}
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = watcher.Add(event.Name)
					}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

func (p *Provider) toFileEvent(event fsnotify.Event, baseDir string) *provider.FileEvent {
	rel, err := filepath.Rel(baseDir, event.Name)
	if err != nil {
		return nil
	}

	var op string
	switch {
	case event.Has(fsnotify.Create):
		op = "create"
	case event.Has(fsnotify.Write):
		op = "write"
	case event.Has(fsnotify.Remove):
		op = "remove"
	case event.Has(fsnotify.Rename):
		op = "rename"
	default:
		return nil
	}

	var size int64
	if info, err := os.Stat(event.Name); err == nil {
		size = info.Size()
	}

	agent := extractAgent(rel)

	return &provider.FileEvent{
		Op:    op,
		Path:  rel,
		Agent: agent,
		Size:  size,
	}
}

func extractAgent(relPath string) string {
	parts := strings.SplitN(relPath, string(filepath.Separator), 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func addRecursive(w *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}
