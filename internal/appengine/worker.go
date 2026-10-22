package appengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// EngineSandboxFlag is the worker binary's internal launcher mode. The worker
// re-executes itself with it so the engine is confined before the pinned CLI
// starts; it is not a public command.
const EngineSandboxFlag = "--engine-sandbox"

// WorkerEngine runs the pinned CLI for one candidate inside the offline worker
// image. Every CLI execution runs confined in its own process group, the whole
// group is killed after the execution, and any process that outlives it makes
// the worker unavailable.
type WorkerEngine struct {
	Binary, Scratch string
	// Launcher is the executable that implements EngineSandboxFlag. Empty
	// means the current executable, which is the worker in its image.
	Launcher string
	// ReadOnly adds read/execute roots for the confined engine. The image
	// needs none beyond the defaults; tests add their fixture directory.
	ReadOnly []string
	// Escaped is called when an engine process survived its candidate. The
	// worker must then exit so the container and its PID namespace restart.
	Escaped func()
	// digest overrides the pinned CLI digest in package tests only.
	digest string
	// pinned is the identity of the binary Verify hashed. Without it every
	// candidate hashes the binary again.
	pinned *engineIdentity
}

// Verify hashes the CLI against its pin once and records the file's identity.
// The worker calls it at startup and refuses to serve if it fails. Afterwards
// each candidate checks only that the identity is unchanged: any write,
// replacement or metadata change of the file changes its inode, ctime or
// size, and then the engine is unavailable until a restart verifies again.
func (e *WorkerEngine) Verify() error {
	id, err := e.hashBinary()
	if err != nil {
		return err
	}
	e.pinned = id
	return nil
}

func (e WorkerEngine) verifiedIdentity() bool { return e.pinned != nil }

// hashBinary checks the digest and the identity of the same open file, so the
// identity describes exactly the content that was hashed.
func (e WorkerEngine) hashBinary() (*engineIdentity, error) {
	f, err := os.Open(e.Binary)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := fileIdentity(f)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return nil, err
	}
	after, err := fileIdentity(f)
	if err != nil {
		return nil, err
	}
	want := GridCraftBinarySHA256
	if e.digest != "" {
		want = e.digest
	}
	if hex.EncodeToString(h.Sum(nil)) != want || before != after {
		return nil, fmt.Errorf("%w: engine digest mismatch", ErrCandidate)
	}
	return &after, nil
}

// RunGridCraftWorker belongs inside the offline worker image. It is not a host
// sandbox and must never be wired directly into the public server. Tests may
// call it against a verified local binary with synthetic inputs only.
func RunGridCraftWorker(ctx context.Context, binary, scratch string, input []byte, c Command) (EngineResult, error) {
	return WorkerEngine{Binary: binary, Scratch: scratch}.Run(ctx, input, c)
}

// Run validates and executes one candidate. There is no unconfined fallback.
func (e WorkerEngine) Run(ctx context.Context, input []byte, c Command) (result EngineResult, err error) {
	if err := c.validate(); err != nil {
		return EngineResult{}, err
	}
	workbook, err := decodeWorkbook(input)
	if err != nil {
		return EngineResult{}, err
	}
	if c.Kind == "cell.set" {
		index, ok := workbook["active_sheet"].(json.Number)
		if !ok {
			return EngineResult{}, ErrInvalid
		}
		n, err := index.Int64()
		sheets := workbook["sheets"].([]any)
		if err != nil || n < 0 || n >= int64(len(sheets)) || sheets[n].(map[string]any)["name"] != c.Sheet {
			return EngineResult{}, ErrInvalid
		}
	}
	if e.pinned != nil {
		// Hashed once at startup; only confirm it is still that file.
		if err := e.pinned.check(e.Binary); err != nil {
			return EngineResult{}, err
		}
	} else if _, err := e.hashBinary(); err != nil {
		return EngineResult{}, err
	}
	dir, err := os.MkdirTemp(e.Scratch, "workbook-")
	if err != nil {
		return EngineResult{}, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			result = EngineResult{}
			err = fmt.Errorf("%w: private scratch cleanup unconfirmed", ErrEngineUnavailable)
		}
	}()
	source, base, resultPath := filepath.Join(dir, "source.json"), filepath.Join(dir, "base.json"), filepath.Join(dir, "candidate.json")
	if err = os.WriteFile(source, input, 0600); err != nil {
		return EngineResult{}, err
	}
	run := func(args ...string) error { return e.execEngine(ctx, dir, args) }
	if err = run("run", "--in", source, "--out", base, "--quiet"); err != nil {
		return EngineResult{}, err
	}
	normalized, err := readBounded(base)
	if err != nil {
		return EngineResult{}, err
	}
	if c.Kind == "import" {
		return EngineResult{normalized, normalized}, nil
	}
	params, err := json.Marshal(map[string]string{"cell": c.Cell, "input": c.Input})
	if err != nil {
		return EngineResult{}, err
	}
	if err = run("run", "--in", base, "--cmd", "cell.set="+string(params), "--out", resultPath, "--quiet"); err != nil {
		return EngineResult{}, err
	}
	data, err := readBounded(resultPath)
	if err != nil {
		return EngineResult{}, err
	}
	return EngineResult{normalized, data}, nil
}

type WorkerRequest struct {
	Snapshot json.RawMessage `json:"snapshot"`
	Command  Command         `json:"command"`
}

// ServeGridCraftWorker handles exactly one bounded, offline task. A fresh process
// per candidate makes discard and recovery independent of upstream undo state.
func ServeGridCraftWorker(ctx context.Context, binary, scratch string, in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, 2*MaxSnapshotBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > 2*MaxSnapshotBytes {
		return ErrInvalid
	}
	var request WorkerRequest
	if err = DecodeStrict(raw, &request); err != nil {
		return err
	}
	result, err := RunGridCraftWorker(ctx, binary, scratch, request.Snapshot, request.Command)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
