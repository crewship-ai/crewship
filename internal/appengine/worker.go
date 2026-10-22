package appengine

import (
	"bytes"
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
	"time"
)

// RunGridCraftWorker belongs inside the offline worker image. It is not a host
// sandbox and must never be wired directly into the public server. Tests may
// call it against a verified local binary with synthetic inputs only.
func RunGridCraftWorker(ctx context.Context, binary, scratch string, input []byte, c Command) (result EngineResult, err error) {
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
	f, err := os.Open(binary)
	if err != nil {
		return EngineResult{}, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	closeErr := f.Close()
	if err != nil {
		return EngineResult{}, err
	}
	if closeErr != nil {
		return EngineResult{}, closeErr
	}
	if hex.EncodeToString(h.Sum(nil)) != GridCraftBinarySHA256 {
		return EngineResult{}, fmt.Errorf("%w: engine digest mismatch", ErrCandidate)
	}
	dir, err := os.MkdirTemp(scratch, "workbook-")
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
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
		cmd.WaitDelay = 2 * time.Second
		// The CLI prints file paths. Keep them inside the worker; protocol errors do
		// not copy process output into API responses or private Artifact logs.
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("workbook engine execution failed: %w", err)
		}
		return nil
	}
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
	recalc, err := verifyRecalc(ctx, binary, dir, filepath.Base(resultPath))
	if err != nil {
		return EngineResult{}, err
	}
	return EngineResult{Base: normalized, Result: data, Recalc: recalc}, nil
}

// readBounded reads an engine file within MaxEngineOutputBytes and returns its
// compact form. MaxSnapshotBytes bounds that compact form, which is what the
// host stores, rather than the engine's indented output.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxEngineOutputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxEngineOutputBytes {
		return nil, ErrInvalid
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, b); err != nil || compact.Len() > MaxSnapshotBytes {
		return nil, ErrInvalid
	}
	return compact.Bytes(), nil
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

const recalcReportLimit = 1 << 20

// verifyRecalc runs the engine's cached-value oracle over a candidate file in a
// fresh process: every formula is recomputed from scratch and compared exactly
// with the value stored by the incremental edit. The caller has already pinned
// the binary digest. Exit status 1 only reports differences; the parsed report
// decides, and any other failure or malformed report rejects the candidate.
func verifyRecalc(ctx context.Context, binary, dir, name string) (*RecalcVerification, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, "check", name, "--json", "--tolerance", "0", "--max-report", "1")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout = &limitedBuffer{buf: &out, limit: recalcReportLimit}
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("workbook recalculation check failed: %w", err)
		}
	}
	var report struct {
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
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Schema != "gridcraft-check/1" || len(report.Files) != 1 || !report.Files[0].Opened || report.Files[0].Error != nil {
		return nil, fmt.Errorf("%w: unreadable recalculation report", ErrCandidate)
	}
	c := report.Files[0].Calc
	return &RecalcVerification{FormulaCells: c.FormulaCells, Checked: c.Checked, Mismatched: c.Mismatched, NoOracle: c.NoOracle, Volatile: c.Volatile, StaleCache: c.StaleCache}, nil
}

type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		return 0, ErrInvalid
	}
	return w.buf.Write(p)
}
