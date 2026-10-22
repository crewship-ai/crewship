package appengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	// Escaped is called when the worker can no longer vouch for engine
	// isolation: an engine process survived its candidate, the verified CLI
	// changed, or candidate scratch could not be removed. The worker must then
	// exit so the container, its PID namespace and its scratch are replaced and
	// the CLI is verified again.
	Escaped func()
	// Limits bound each engine process; zero fields use DefaultEngineLimits.
	Limits EngineLimits
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

// EngineLimits bound one engine process. They are resource limits of that
// process (and its descendants inherit them), not of the container.
type EngineLimits struct {
	// AddressSpace is RLIMIT_AS in bytes.
	AddressSpace uint64
	// FileSize is RLIMIT_FSIZE in bytes: the largest file the engine can write.
	FileSize uint64
	// CPUSeconds is RLIMIT_CPU.
	CPUSeconds uint64
}

// DefaultEngineLimits fit the documented container profile. The engine needs
// about 10 MiB for a 1 MiB workbook; the worker rejects raw outputs above
// MaxEngineOutputBytes (4 MiB) anyway.
var DefaultEngineLimits = EngineLimits{AddressSpace: 256 << 20, FileSize: 4 << 20, CPUSeconds: 15}

func (l EngineLimits) resolved() EngineLimits {
	if l.AddressSpace == 0 {
		l.AddressSpace = DefaultEngineLimits.AddressSpace
	}
	if l.FileSize == 0 {
		l.FileSize = DefaultEngineLimits.FileSize
	}
	if l.CPUSeconds == 0 {
		l.CPUSeconds = DefaultEngineLimits.CPUSeconds
	}
	return l
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
	sheet := c.Sheet
	if c.Kind == "cells.set" {
		sheet = c.Cells[0].Sheet
	}
	if c.Kind != "import" {
		index, ok := workbook["active_sheet"].(json.Number)
		if !ok {
			return EngineResult{}, ErrInvalid
		}
		n, err := index.Int64()
		sheets := workbook["sheets"].([]any)
		if err != nil || n < 0 || n >= int64(len(sheets)) || sheets[n].(map[string]any)["name"] != sheet {
			return EngineResult{}, ErrInvalid
		}
	}
	if e.pinned != nil {
		// Hashed once at startup; only confirm it is still that file.
		if err := e.pinned.check(e.Binary); err != nil {
			// Only a restart, which hashes the binary again, may trust it.
			if e.Escaped != nil {
				e.Escaped()
			}
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
		cleanupErr := makeRemovable(dir)
		if cleanupErr == nil {
			cleanupErr = os.RemoveAll(dir)
		}
		if cleanupErr != nil {
			// Scratch is shared by every candidate; only a new container
			// guarantees it is clean again.
			if e.Escaped != nil {
				e.Escaped()
			}
			result = EngineResult{}
			err = fmt.Errorf("%w: private scratch cleanup unconfirmed", ErrEngineUnavailable)
		}
	}()
	source, base, resultPath := filepath.Join(dir, "source.json"), filepath.Join(dir, "base.json"), filepath.Join(dir, "candidate.json")
	if err = os.WriteFile(source, input, 0600); err != nil {
		return EngineResult{}, err
	}
	run := func(args ...string) error { return e.execEngine(ctx, dir, nil, args) }
	if err = run("run", "--in", source, "--out", base, "--quiet"); err != nil {
		return EngineResult{}, err
	}
	normalized, err := readBounded(base)
	if err != nil {
		return EngineResult{}, err
	}
	if c.Kind == "import" {
		return EngineResult{Base: normalized, Result: normalized}, nil
	}
	cells := c.Cells
	if c.Kind == "cell.set" {
		cells = []CellInput{{c.Sheet, c.Cell, c.Input}}
	}
	// All cells of one mutation are applied in a single engine run, atomically.
	args := []string{"run", "--in", base}
	for _, cell := range cells {
		params, err := json.Marshal(map[string]string{"cell": cell.Cell, "input": cell.Input})
		if err != nil {
			return EngineResult{}, err
		}
		args = append(args, "--cmd", "cell.set="+string(params))
	}
	if err = run(append(args, "--out", resultPath, "--quiet")...); err != nil {
		return EngineResult{}, err
	}
	data, err := readBounded(resultPath)
	if err != nil {
		return EngineResult{}, err
	}
	recalc, err := e.verifyRecalc(ctx, data)
	if err != nil {
		return EngineResult{}, err
	}
	return EngineResult{Base: normalized, Result: data, Recalc: recalc}, nil
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
	// Keep the candidate bytes exactly as checked; HTML escaping would change
	// them and break the report's digest binding.
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return enc.Encode(result)
}

const recalcReportLimit = 1 << 20

// verifyRecalc checks a candidate's recalculation in a confined execution.
func verifyRecalc(ctx context.Context, binary, scratch string, data []byte) (*RecalcVerification, error) {
	return WorkerEngine{Binary: binary, Scratch: scratch}.verifyRecalc(ctx, data)
}

// verifyRecalc runs the engine's cached-value oracle over a candidate in a
// fresh, fully confined engine execution: every formula is recomputed from
// scratch and compared exactly with the value stored by the incremental edit.
// It uses the same sandbox as every other execution (Landlock write scope on
// its own new directory beside the candidate's, seccomp, resource limits,
// session sweep), so no earlier execution's process can survive into it or
// reach its files. The report goes to an unlinked file only the worker and
// this execution's stdout hold. Exit status 1 only reports differences; the
// parsed report decides, and any other failure or malformed report rejects
// the candidate.
func (e WorkerEngine) verifyRecalc(ctx context.Context, data []byte) (result *RecalcVerification, err error) {
	dir, err := os.MkdirTemp(e.Scratch, "check-")
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanupErr := makeRemovable(dir)
		if cleanupErr == nil {
			cleanupErr = os.RemoveAll(dir)
		}
		if cleanupErr != nil {
			if e.Escaped != nil {
				e.Escaped()
			}
			result = nil
			err = fmt.Errorf("%w: private scratch cleanup unconfirmed", ErrEngineUnavailable)
		}
	}()
	if err = writeExclusive(filepath.Join(dir, "candidate.json"), data); err != nil {
		return nil, err
	}
	report, err := os.CreateTemp(e.Scratch, "report-")
	if err != nil {
		return nil, err
	}
	defer report.Close()
	if err = os.Remove(report.Name()); err != nil {
		return nil, err
	}
	if err = e.execEngine(ctx, dir, report, []string{"check", "candidate.json", "--json", "--tolerance", "0", "--max-report", "1"}); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("workbook recalculation check failed: %w", err)
		}
	}
	if _, err = report.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(report, recalcReportLimit+1))
	if err != nil {
		return nil, err
	}
	if len(out) > recalcReportLimit {
		return nil, ErrInvalid
	}
	var parsed struct {
		Schema string `json:"schema"`
		Files  []struct {
			Opened bool    `json:"opened"`
			Error  *string `json:"error"`
			Calc   struct {
				FormulaCells int  `json:"formulaCells"`
				Checked      int  `json:"checked"`
				Mismatched   int  `json:"mismatched"`
				NoOracle     int  `json:"noOracle"`
				Volatile     int  `json:"volatile"`
				StaleCache   bool `json:"staleCache"`
			} `json:"calc"`
		} `json:"files"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || parsed.Schema != "gridcraft-check/1" || len(parsed.Files) != 1 || !parsed.Files[0].Opened || parsed.Files[0].Error != nil {
		return nil, fmt.Errorf("%w: unreadable recalculation report", ErrCandidate)
	}
	c := parsed.Files[0].Calc
	sum := sha256.Sum256(data)
	return &RecalcVerification{FormulaCells: c.FormulaCells, Checked: c.Checked, Mismatched: c.Mismatched, NoOracle: c.NoOracle, Volatile: c.Volatile, StaleCache: c.StaleCache, SHA256: hex.EncodeToString(sum[:])}, nil
}

func writeExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
