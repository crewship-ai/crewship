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
	run := func(args ...string) error { return runEngineProcess(ctx, binary, dir, nil, args...) }
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
	recalc, err := verifyRecalc(ctx, binary, scratch, data)
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
	// Keep the candidate bytes exactly as checked; HTML escaping would change
	// them and break the report's digest binding.
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return enc.Encode(result)
}

const recalcReportLimit = 1 << 20

// verifyRecalc runs the engine's cached-value oracle over a candidate file in a
// fresh process: every formula is recomputed from scratch and compared exactly
// with the value stored by the incremental edit. The caller has already pinned
// the binary digest. Exit status 1 only reports differences; the parsed report
// decides, and any other failure or malformed report rejects the candidate.
func verifyRecalc(ctx context.Context, binary, scratch string, data []byte) (*RecalcVerification, error) {
	// Check exactly the bytes being returned, from a fresh private directory the
	// earlier engine executions never saw.
	dir, err := os.MkdirTemp(scratch, "check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err = writeExclusive(filepath.Join(dir, "candidate.json"), data); err != nil {
		return nil, err
	}
	report, err := os.OpenFile(filepath.Join(dir, "report.json"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	defer report.Close()
	if err = runEngineProcess(ctx, binary, dir, report, "check", "candidate.json", "--json", "--tolerance", "0", "--max-report", "1"); err != nil {
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

// runEngineProcess runs one engine execution in its own process group and kills
// the whole group afterwards, so nothing the engine started can outlive it and
// touch scratch files later. Output goes to a file, not a pipe a surviving
// descendant could hold open. (The confined worker engine keeps the same
// contract: one group per execution, killed after every run.)
func runEngineProcess(ctx context.Context, binary, dir string, stdout *os.File, args ...string) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	cmd.WaitDelay = 2 * time.Second
	// The CLI prints file paths. Keep them inside the worker; protocol errors do
	// not copy process output into API responses or private Artifact logs.
	if stdout != nil {
		cmd.Stdout = stdout
	}
	ownProcessGroup(cmd)
	cmd.Cancel = func() error { killProcessGroup(cmd); return nil }
	err := cmd.Run()
	killProcessGroup(cmd)
	if err != nil {
		return fmt.Errorf("workbook engine execution failed: %w", err)
	}
	return nil
}
