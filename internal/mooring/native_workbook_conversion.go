package mooring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

var ErrWorkbookFidelity = errors.New("legacy Sheet conversion fidelity could not be confirmed")

// ConvertLegacyWorkbook requires both the engine's part check and an exact
// round trip of the old model's fields. The latter checks values, formulas,
// styles, names and pivots, including fields the new engine could have ignored
// when opening the legacy JSON. It allows defaults added by the new engine.
func ConvertLegacyWorkbook(ctx context.Context, engine craftengine.Engine, source []byte) ([]byte, error) {
	if engine == nil || engine.Application() != "gridcraft" || !json.Valid(source) || len(source) > 32<<20 {
		return nil, ErrWorkbookFidelity
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "craft-workbook-convert-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err = os.WriteFile(filepath.Join(dir, "source.json"), source, 0o600); err != nil {
		return nil, err
	}
	run := func(input, output string, fidelity bool) ([]byte, error) {
		root := engine.Root(dir)
		target := root + "/" + output
		args := []string{"convert", root + "/" + input, target, "--saved-values"}
		if fidelity {
			args = append(args, "--fidelity")
		}
		stdout := &nativeEngineOutput{limit: 64 << 10}
		stderr := &nativeEngineOutput{limit: 64 << 10}
		if err := engine.Run(ctx, dir, args, stdout, stderr); err != nil {
			return nil, ErrWorkbookFidelity
		}
		expected := ""
		if fidelity {
			expected = `{"parts":[]}`
		}
		if strings.TrimSpace(stdout.String()) != expected {
			return nil, ErrWorkbookFidelity
		}
		path := filepath.Join(dir, output)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 32<<20 ||
			stderr.String() != fmt.Sprintf("wrote %s (%d bytes)\n", target, info.Size()) {
			return nil, ErrWorkbookFidelity
		}
		return os.ReadFile(path)
	}
	workbook, err := run("source.json", "workbook.xlsx", true)
	if err != nil {
		return nil, err
	}
	if format, err := DetectWorkbook(workbook); err != nil || format != "xlsx" {
		return nil, ErrWorkbookFidelity
	}
	if err := ValidateWorkbook(ctx, workbook); err != nil {
		return nil, ErrWorkbookFidelity
	}
	roundTrip, err := run("workbook.xlsx", "round-trip.json", false)
	if err != nil {
		return nil, err
	}
	if !json.Valid(roundTrip) || !preservesNativeModel(source, roundTrip, "gridcraft") {
		return nil, ErrWorkbookFidelity
	}
	return workbook, nil
}
