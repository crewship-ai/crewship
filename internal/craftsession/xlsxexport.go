package craftsession

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// ErrExportFailed is an engine that did not write a workbook from the document.
var ErrExportFailed = errors.New("craftsession: export failed")

// DefaultMaxExportBytes caps the workbook an export hands out (the import accepts 64 MiB; an
// exported workbook of the same document is of that order).
const DefaultMaxExportBytes = 256 << 20

// ExportXLSX converts a native document to an xlsx package with a one-shot engine worker
// (`run --in doc.json --out out.xlsx`; ExecLauncher in production). The result must be a zip
// with [Content_Types].xml and xl/workbook.xml and at most maxBytes (0 = DefaultMaxExportBytes);
// anything else is ErrExportFailed or ErrTooLarge and nothing is returned. The work directory is
// removed either way.
func ExportXLSX(ctx context.Context, launch Launcher, tempRoot string, native []byte, maxBytes int64) ([]byte, error) {
	return exportXLSX(ctx, launch, tempRoot, native, maxBytes, false)
}

// FidelityPart is one kind of workbook part an export would lose (pivot_fields, charts, …) and how
// many; never a name or content.
type FidelityPart struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// ErrFidelityLoss matches a *FidelityLossError.
var ErrFidelityLoss = errors.New("craftsession: the workbook would lose parts")

// FidelityLossError: the engine compared the written workbook with the document (--fidelity) and
// the workbook would lose these parts; nothing was written.
type FidelityLossError struct{ Parts []FidelityPart }

func (e *FidelityLossError) Error() string {
	out := ErrFidelityLoss.Error() + ":"
	for _, p := range e.Parts {
		out += " " + p.Code + " " + strconv.Itoa(p.Count)
	}
	return out
}
func (e *FidelityLossError) Is(target error) bool { return target == ErrFidelityLoss }

var fidelityCode = regexp.MustCompile(`^[a-z_]{1,32}$`)

// fidelityParts reads the engine's {"parts":[…]} line; ok is false for anything else.
func fidelityParts(line []byte) (parts []FidelityPart, ok bool) {
	var v struct {
		Parts *[]FidelityPart `json:"parts"`
	}
	if json.Unmarshal(line, &v) != nil || v.Parts == nil || len(*v.Parts) > 32 {
		return nil, false
	}
	for _, p := range *v.Parts {
		if !fidelityCode.MatchString(p.Code) || p.Count < 1 {
			return nil, false
		}
	}
	return *v.Parts, true
}

// ExportXLSXFaithful is ExportXLSX with the engine's fidelity check (--fidelity): the workbook is
// returned only when the engine confirms it loses nothing; a loss is a *FidelityLossError. An
// engine that does not confirm (no fidelity line) is a failed export.
func ExportXLSXFaithful(ctx context.Context, launch Launcher, tempRoot string, native []byte, maxBytes int64) ([]byte, error) {
	return exportXLSX(ctx, launch, tempRoot, native, maxBytes, true)
}

func exportXLSX(ctx context.Context, launch Launcher, tempRoot string, native []byte, maxBytes int64, faithful bool) ([]byte, error) {
	if len(native) == 0 {
		return nil, ErrInvalid
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxExportBytes
	}
	dir, err := os.MkdirTemp(tempRoot, "craft-export-")
	if err != nil {
		return nil, ErrExportFailed
	}
	defer os.RemoveAll(dir)
	if err = os.WriteFile(filepath.Join(dir, "doc.json"), native, 0o600); err != nil {
		return nil, ErrExportFailed
	}
	args := []string{"run", "--in", "/work/doc.json", "--quiet", "--out", "/work/out.xlsx"}
	if faithful {
		args = append(args, "--fidelity")
	}
	w, err := launch(ctx, dir, args)
	if err != nil {
		return nil, ErrExportFailed
	}
	defer w.Close()
	if err = w.Wait(ctx); err != nil {
		// Exit 3 is the engine's "would lose parts", with its parts line (the worker's own exit 3,
		// an unusable module, prints none).
		if faithful && w.ExitCode() == 3 {
			if parts, ok := fidelityParts(w.LastLine(ctx)); ok && len(parts) > 0 {
				return nil, &FidelityLossError{Parts: parts}
			}
		}
		return nil, ErrExportFailed
	}
	if faithful {
		if parts, ok := fidelityParts(w.LastLine(ctx)); !ok || len(parts) != 0 {
			return nil, ErrExportFailed
		}
	}
	path := filepath.Join(dir, "out.xlsx")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, ErrExportFailed
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: the exported workbook is %d bytes", ErrTooLarge, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrExportFailed
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrExportFailed
	}
	parts := map[string]bool{}
	for _, f := range zr.File {
		parts[f.Name] = true
	}
	if !parts["[Content_Types].xml"] || !parts["xl/workbook.xml"] {
		return nil, ErrExportFailed
	}
	return data, nil
}
