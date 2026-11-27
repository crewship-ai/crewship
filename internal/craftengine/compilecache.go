package craftengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// A compiled engine module costs 5 to 7.6 s to compile on every worker start (measured on a
// Linux build host for gridcraft 0.12.1); from wazero's compilation cache it loads in 0.17 s. The cache
// holds native machine code that a worker executes, so it is handled as trusted server state:
//
//   - only the server writes it: the first worker start of a module compiles it once in the
//     server process, in a private directory under the server's data directory (0700, owned by
//     the server's user, never mounted into an agent container or given to the guest);
//   - an entry is keyed by the module's SHA-256, the wazero version and the platform (wazero
//     adds the CPU features), and a directory of this server holds only the entries it made
//     itself: whatever an earlier process left is removed, so a changed engine or wazero never
//     meets an old entry;
//   - a worker gets the entry's path and SHA-256, reads it, checks the digest and copies it to a
//     private temporary directory before wazero reads it; a worker never writes the shared
//     directory, and any problem (missing, changed, unreadable, rejected by wazero) makes it
//     compile the module itself, silently: never an error to the user;
//   - an entry is at most MaxCompileCacheEntry bytes, and the directory holds one entry per
//     engine module.

// MaxCompileCacheEntry bounds one compiled module (gridcraft 0.12.1: 44 MB).
const MaxCompileCacheEntry = 256 << 20

// CompileCache is the server's directory of compiled engine modules.
type CompileCache struct {
	root    string
	mu      sync.Mutex
	entries map[string]*compileEntry
}

type compileEntry struct {
	once            sync.Once
	path, rel, hash string
}

var (
	compileCacheMu sync.RWMutex
	compileCache   *CompileCache
)

// EnableCompileCache makes every engine worker of this process use a compilation cache under
// root. It creates root (0700) and refuses a root that another user owns or that group or others
// may use; it empties it, since its entries are only trusted within one server process.
func EnableCompileCache(root string) error {
	c, err := NewCompileCache(root)
	if err != nil {
		return err
	}
	compileCacheMu.Lock()
	compileCache = c
	compileCacheMu.Unlock()
	return nil
}

func activeCompileCache() *CompileCache {
	compileCacheMu.RLock()
	defer compileCacheMu.RUnlock()
	return compileCache
}

// NewCompileCache prepares root as an empty private cache directory.
func NewCompileCache(root string) (*CompileCache, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, ErrUnavailable
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := privateDir(root); err != nil {
		return nil, err
	}
	list, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, e := range list {
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			return nil, err
		}
	}
	return &CompileCache{root: root, entries: map[string]*compileEntry{}}, nil
}

var errNotPrivate = errors.New("craftengine: the compilation cache directory is not private to this server")

func wazeroVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d.Path == "github.com/tetratelabs/wazero" {
				if d.Replace != nil {
					return d.Replace.Version
				}
				return d.Version
			}
		}
	}
	return "unknown"
}

func cacheKey(moduleSHA string) string {
	v := strings.NewReplacer("/", "_", "+", "_").Replace(wazeroVersion())
	return moduleSHA + "-wazero-" + v + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

// entry returns the compiled module of modulePath (whose SHA-256 is moduleSHA), compiling it
// once in this process. ok is false when there is no usable entry.
func (c *CompileCache) entry(moduleSHA, modulePath string) (path, rel, hash string, ok bool) {
	if c == nil || len(moduleSHA) != 64 {
		return "", "", "", false
	}
	key := cacheKey(moduleSHA)
	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		e = &compileEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.path, e.rel, e.hash = c.fill(key, moduleSHA, modulePath) })
	return e.path, e.rel, e.hash, e.path != ""
}

// fill compiles the module with wazero's file cache in a fresh directory and keeps the one
// entry it wrote. Any failure leaves no entry (workers then compile on their own).
func (c *CompileCache) fill(key, moduleSHA, modulePath string) (string, string, string) {
	module, err := os.ReadFile(modulePath)
	if err != nil {
		return "", "", ""
	}
	if sum := sha256.Sum256(module); hex.EncodeToString(sum[:]) != moduleSHA {
		return "", "", ""
	}
	dir := filepath.Join(c.root, key)
	_ = os.RemoveAll(dir)
	if err = os.Mkdir(dir, 0o700); err != nil {
		return "", "", ""
	}
	ctx := context.Background()
	cache, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", ""
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCompilationCache(cache))
	_, _ = wasi_snapshot_preview1.Instantiate(ctx, rt)
	_, err = rt.CompileModule(ctx, module)
	_ = rt.Close(ctx)
	_ = cache.Close(ctx)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", ""
	}
	var found []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 1 {
		_ = os.RemoveAll(dir)
		return "", "", ""
	}
	raw, err := readBounded(found[0], MaxCompileCacheEntry)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", ""
	}
	_ = os.Chmod(found[0], 0o400)
	rel, err := filepath.Rel(dir, found[0])
	if err != nil || strings.HasPrefix(rel, "..") {
		_ = os.RemoveAll(dir)
		return "", "", ""
	}
	sum := sha256.Sum256(raw)
	return found[0], rel, hex.EncodeToString(sum[:])
}

func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, ErrUnavailable
	}
	return raw, nil
}

// workerCacheArgs are the worker flags naming the compiled entry of the module, or nil.
func workerCacheArgs(moduleSHA, modulePath string) []string {
	path, rel, hash, ok := activeCompileCache().entry(moduleSHA, modulePath)
	if !ok {
		return nil
	}
	return []string{"--cache-entry", path, "--cache-rel", rel, "--cache-sha", hash}
}

// workerCompile compiles module in rt's place: from the verified cache entry when there is one,
// otherwise (and on any problem with it) by itself. It returns the runtime that holds the module.
func workerCompile(ctx context.Context, cfg wazero.RuntimeConfig, module []byte, entry, rel, hash string) (wazero.Runtime, wazero.CompiledModule, error) {
	if entry != "" && rel != "" && len(hash) == 64 {
		if rt, compiled, ok := compileFromEntry(ctx, cfg, module, entry, rel, hash); ok {
			return rt, compiled, nil
		}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, nil, err
	}
	compiled, err := rt.CompileModule(ctx, module)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, nil, err
	}
	return rt, compiled, nil
}

func compileFromEntry(ctx context.Context, cfg wazero.RuntimeConfig, module []byte, entry, rel, hash string) (wazero.Runtime, wazero.CompiledModule, bool) {
	if filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
		return nil, nil, false
	}
	raw, err := readBounded(entry, MaxCompileCacheEntry)
	if err != nil {
		return nil, nil, false
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != hash {
		return nil, nil, false
	}
	tmp, err := os.MkdirTemp("", "craft-engine-cache-")
	if err != nil {
		return nil, nil, false
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, filepath.Clean(rel))
	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil || os.WriteFile(dst, raw, 0o600) != nil {
		return nil, nil, false
	}
	cache, err := wazero.NewCompilationCacheWithDir(tmp)
	if err != nil {
		return nil, nil, false
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg.WithCompilationCache(cache))
	if _, err = wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, nil, false
	}
	compiled, err := rt.CompileModule(ctx, module)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, nil, false
	}
	return rt, compiled, true
}

// CompileCacheRoot is the directory of this process's compilation cache, or "" when it is off.
func CompileCacheRoot() string {
	if c := activeCompileCache(); c != nil {
		return c.root
	}
	return ""
}

// DisableCompileCache turns the compilation cache off: workers compile the module themselves.
func DisableCompileCache() {
	compileCacheMu.Lock()
	compileCache = nil
	compileCacheMu.Unlock()
}
