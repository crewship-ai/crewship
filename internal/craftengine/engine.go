// Package craftengine runs the document engine of a bundled Craft application
// (WordCraft for Word, GridCraft for Sheet) on one private working directory
// (#5315).
//
// The release ships each engine as a WebAssembly (WASI) module that runs inside
// the Crewship process through a pure-Go runtime: the same bytes on every
// operating system and processor, no helper process, no file outside the
// working directory, no network, and bounded memory and time. A native
// command-line engine remains available only as a development override.
//
// An engine sees the working directory under the path its Root returns, and
// every argument that names a file uses that path. Callers create the
// directory, write the inputs, run the engine and read the outputs back.
package craftengine

import (
	"context"
	"errors"
	"io"
)

// Runtime names how an engine runs.
const (
	RuntimeWASM   = "wasm"
	RuntimeNative = "native"
)

// ErrUnavailable means the engine cannot run here at all.
var ErrUnavailable = errors.New("document engine unavailable")

// ErrFailed means one run did not complete (non-zero exit, trap, timeout or
// an output limit). It never carries the engine's own output.
var ErrFailed = errors.New("document engine run failed")

// Engine runs one engine command on a working directory.
type Engine interface {
	// Application is "wordcraft" or "gridcraft".
	Application() string
	// Runtime is RuntimeWASM or RuntimeNative.
	Runtime() string
	// SHA256 identifies the exact engine bytes (module or executable).
	SHA256() string
	// Root is the path under which the engine sees dir.
	Root(dir string) string
	// Run executes the engine with args (args[0] is the first argument, not
	// the program name). stdout and stderr receive its output; a writer that
	// refuses a write fails the run.
	Run(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error
}
