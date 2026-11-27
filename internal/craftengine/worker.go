package craftengine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"
)

// ProbeFlag makes a worker answer ProbeAnswer and exit, so a caller can tell a
// binary that really runs as a worker from one that only looks like it.
const (
	ProbeFlag   = "--probe"
	ProbeAnswer = "craft-engine-worker 1"
)

// Worker exit codes (WorkerMain).
const (
	workerExitUsage  = 2
	workerExitModule = 3
	workerExitRun    = 4
)

// WorkerMain is the body of the engine worker process: one WASI module runs
// with this process's stdin and stdout as its own, so a parent talks to a
// long-lived engine (for example `gridcraft-cli mcp --in FILE`) over pipes.
//
//	--app NAME --module PATH --sha HEX [--mem MiB] --dir DIR -- ENGINE ARGS...
//
// The module bytes must hash to --sha or the worker exits without running
// anything. The guest sees DIR (as GuestRoot) and nothing else: no network,
// minimal environment. Unlike Run, a worker does not poll its context: a
// parent that wants it stopped kills the process, which is the only way that
// also stops a guest stuck in a loop, and costs nothing while it runs (the
// close-on-context-done checks made a real 235 000 cell workbook open 4.6
// times slower).
func WorkerMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == ProbeFlag {
		fmt.Fprintln(stdout, ProbeAnswer)
		return 0
	}
	fs := flag.NewFlagSet("craft-engine-worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	app := fs.String("app", "", "application name")
	modulePath := fs.String("module", "", "path of the verified engine module")
	sha := fs.String("sha", "", "expected SHA-256 of the module")
	mem := fs.Int("mem", 1024, "linear memory limit in MiB")
	dir := fs.String("dir", "", "working directory the engine sees")
	cacheEntry := fs.String("cache-entry", "", "compiled module from the server's compilation cache")
	cacheRel := fs.String("cache-rel", "", "its path inside the cache directory")
	cacheSHA := fs.String("cache-sha", "", "its SHA-256")
	if err := fs.Parse(args); err != nil {
		return workerExitUsage
	}
	if *app == "" || *modulePath == "" || len(*sha) != 64 || *dir == "" {
		fmt.Fprintln(stderr, "craft-engine-worker: --app, --module, --sha and --dir are required")
		return workerExitUsage
	}
	module, err := os.ReadFile(*modulePath)
	if err != nil {
		fmt.Fprintln(stderr, "craft-engine-worker: module unreadable")
		return workerExitModule
	}
	sum := sha256.Sum256(module)
	if hex.EncodeToString(sum[:]) != *sha {
		fmt.Fprintln(stderr, "craft-engine-worker: module digest mismatch")
		return workerExitModule
	}
	limits := Limits{MemoryMiB: *mem}.resolved()
	ctx := context.Background()
	// From the server's compilation cache when it names a verified entry; otherwise, and on any
	// problem with that entry, compiled here (compilecache.go).
	rt, compiled, err := workerCompile(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(uint32(limits.MemoryMiB)*16), module, *cacheEntry, *cacheRel, *cacheSHA)
	if err != nil {
		fmt.Fprintln(stderr, "craft-engine-worker: module does not compile")
		return workerExitModule
	}
	defer rt.Close(ctx)
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs(append([]string{*app + "-cli"}, fs.Args()...)...).
		WithFSConfig(wazero.NewFSConfig().WithDirMount(*dir, GuestRoot)).
		WithEnv("LANG", "C.UTF-8").WithEnv("HOME", GuestRoot).WithEnv("TMPDIR", GuestRoot).
		WithStdin(stdin).WithStdout(stdout).WithStderr(stderr).
		WithSysWalltime().WithSysNanotime().WithRandSource(rand.Reader)
	mod, err := rt.InstantiateModule(ctx, compiled, cfg)
	if mod != nil {
		_ = mod.Close(ctx)
	}
	if err == nil {
		return 0
	}
	if exit, ok := err.(*sys.ExitError); ok {
		return int(exit.ExitCode())
	}
	return workerExitRun
}
