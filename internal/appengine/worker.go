package appengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
func RunGridCraftWorker(ctx context.Context, binary, scratch string, input []byte, c Command) (EngineResult, error) {
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
	defer os.RemoveAll(dir)
	source, base, result := filepath.Join(dir, "source.json"), filepath.Join(dir, "base.json"), filepath.Join(dir, "candidate.json")
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
		return EngineResult{normalized, normalized}, nil
	}
	params, err := json.Marshal(map[string]string{"cell": c.Cell, "input": c.Input})
	if err != nil {
		return EngineResult{}, err
	}
	if err = run("run", "--in", base, "--cmd", "cell.set="+string(params), "--out", result, "--quiet"); err != nil {
		return EngineResult{}, err
	}
	data, err := readBounded(result)
	if err != nil {
		return EngineResult{}, err
	}
	return EngineResult{normalized, data}, nil
}
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSnapshotBytes {
		return nil, ErrInvalid
	}
	return b, nil
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
