package craftsession

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// An xlsx file is untrusted input. Import checks it BEFORE the engine sees it
// (shape and size of the archive, so a zip bomb or a malformed package costs
// nothing), runs the engine as a one-shot worker with a hard deadline, and
// refuses a result that lost cells, instead of storing a silently shortened
// sheet (the engine stops reading a sheet part after 1 000 000 XML elements
// and still exits 0). Which cells may be missing is in xlsxverify.go.

var (
	// ErrNotXLSX means the bytes are not an xlsx package (or it is encrypted).
	ErrNotXLSX = errors.New("craftsession: not a readable xlsx workbook")
	// ErrTooLarge means a limit of ArchiveLimits was exceeded.
	ErrTooLarge = errors.New("craftsession: workbook exceeds an import limit")
	// ErrTruncated means the engine did not keep every cell with content; a
	// *TruncatedError says which sheet and what kind.
	ErrTruncated = errors.New("craftsession: the engine did not read the whole workbook")
	// ErrImportFailed is any other engine failure (crash, deadline, bad output).
	ErrImportFailed = errors.New("craftsession: import failed")
)

// ArchiveLimits bound an xlsx package. Zero fields take the defaults.
type ArchiveLimits struct {
	MaxEntries      int   // default 10 000
	MaxPartBytes    int64 // uncompressed, default 512 MiB (the engine's own cap)
	MaxTotalBytes   int64 // uncompressed, default 1 GiB (the engine's own cap)
	MaxRatio        int64 // total uncompressed / compressed, default 200
	MaxEntryRatio   int64 // per entry larger than 1 MiB, default 1000
	MaxNativeBytes  int64 // compact native JSON after conversion, default 32 MiB
	MaxWorksheets   int   // default 256
	MaxCellsPerFile int64 // default 16 000 000 (the engine's own cap)
	// MaxPartElements is the engine's per-part XML element cap (1 000 000 in
	// gridcraft 0.12.1); a part with more is cut short without a warning.
	MaxPartElements int64
}

func (l ArchiveLimits) resolved() ArchiveLimits {
	d := func(v *int64, def int64) {
		if *v <= 0 {
			*v = def
		}
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = 10_000
	}
	if l.MaxWorksheets <= 0 {
		l.MaxWorksheets = 256
	}
	d(&l.MaxPartBytes, 512<<20)
	d(&l.MaxTotalBytes, 1<<30)
	d(&l.MaxRatio, 200)
	d(&l.MaxEntryRatio, 1000)
	d(&l.MaxNativeBytes, 32<<20)
	d(&l.MaxCellsPerFile, 16_000_000)
	d(&l.MaxPartElements, 1_000_000)
	return l
}

// ArchiveReport is what the archive check found; it also feeds the notice of
// what the import does not carry over.
type ArchiveReport struct {
	Entries           int      `json:"entries"`
	UncompressedBytes int64    `json:"uncompressed_bytes"`
	Worksheets        int      `json:"worksheets"`
	Cells             int64    `json:"cells"`    // <c> elements in all worksheets
	Formulas          int64    `json:"formulas"` // <f> elements in all worksheets
	LargestPart       int64    `json:"largest_part_elements"`
	Macros            bool     `json:"macros"`
	ExternalLinks     int      `json:"external_links"`
	Pivots            int      `json:"pivot_tables"`
	Charts            int      `json:"charts"`
	Notes             []string `json:"notes,omitempty"`
}

// CheckArchive reads the zip directory and streams the worksheet parts once
// to count cells. It never trusts the sizes declared in the directory alone:
// the archive reader stops at the declared size, and the cell count reads at
// most MaxPartBytes of each part.
func CheckArchive(data []byte, lim ArchiveLimits) (*ArchiveReport, error) {
	lim = lim.resolved()
	if len(data) < 22 || !bytes.HasPrefix(data, []byte("PK")) {
		return nil, ErrNotXLSX // includes the OLE container an encrypted workbook uses
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrNotXLSX
	}
	if len(zr.File) == 0 || len(zr.File) > lim.MaxEntries {
		return nil, ErrTooLarge
	}
	rep := &ArchiveReport{Entries: len(zr.File)}
	var total, compressed int64
	hasWorkbook := false
	seen := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		clean := path.Clean(name)
		if name == "" || len(name) > 260 || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\\") ||
			strings.HasPrefix(name, "/") || clean == ".." || strings.HasPrefix(clean, "../") || seen[strings.ToLower(name)] {
			return nil, ErrNotXLSX
		}
		seen[strings.ToLower(name)] = true
		if f.Mode()&os.ModeSymlink != 0 || f.Flags&0x1 != 0 { // symlink, or an encrypted entry
			return nil, ErrNotXLSX
		}
		if f.UncompressedSize64 > uint64(lim.MaxPartBytes) {
			return nil, ErrTooLarge
		}
		sz := int64(f.UncompressedSize64)
		total += sz
		compressed += int64(f.CompressedSize64)
		if total > lim.MaxTotalBytes {
			return nil, ErrTooLarge
		}
		if sz > 1<<20 && int64(f.CompressedSize64) > 0 && sz/int64(f.CompressedSize64) > lim.MaxEntryRatio {
			return nil, ErrTooLarge
		}
		switch {
		case name == "xl/workbook.xml":
			hasWorkbook = true
		case strings.HasPrefix(name, "xl/worksheets/") && strings.HasSuffix(name, ".xml"):
			rep.Worksheets++
		case strings.HasSuffix(name, "vbaProject.bin"):
			rep.Macros = true
		case strings.HasPrefix(name, "xl/externalLinks/") && strings.HasSuffix(name, ".xml"):
			rep.ExternalLinks++
		case strings.HasPrefix(name, "xl/pivotTables/") && strings.HasSuffix(name, ".xml"):
			rep.Pivots++
		case strings.HasPrefix(name, "xl/charts/chart") && strings.HasSuffix(name, ".xml"):
			rep.Charts++
		}
	}
	if !hasWorkbook || rep.Worksheets == 0 {
		return nil, ErrNotXLSX
	}
	if rep.Worksheets > lim.MaxWorksheets {
		return nil, ErrTooLarge
	}
	if compressed > 0 && total/compressed > lim.MaxRatio && total > 64<<20 {
		return nil, ErrTooLarge
	}
	rep.UncompressedBytes = total
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "xl/worksheets/") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		n, fm, elems, err := countParts(f, lim.MaxPartBytes)
		if err != nil {
			if errors.Is(err, ErrTooLarge) {
				return nil, err
			}
			return nil, ErrNotXLSX
		}
		if elems > lim.MaxPartElements {
			return nil, fmt.Errorf("%w: sheet part %s has %d XML elements, the engine reads at most %d", ErrTooLarge, f.Name, elems, lim.MaxPartElements)
		}
		if elems > rep.LargestPart {
			rep.LargestPart = elems
		}
		rep.Cells += n
		rep.Formulas += fm
		if rep.Cells > lim.MaxCellsPerFile {
			return nil, ErrTooLarge
		}
	}
	if rep.Macros {
		rep.Notes = append(rep.Notes, "macros are not imported")
	}
	if rep.ExternalLinks > 0 {
		rep.Notes = append(rep.Notes, "links to other workbooks are not followed")
	}
	return rep, nil
}

// countParts scans a decompressed worksheet part once and counts its <c>
// (cell), <f> (formula) and all start-tag elements.
func countParts(f *zip.File, max int64) (cells, formulas, elements int64, err error) {
	rc, err := f.Open()
	if err != nil {
		return 0, 0, 0, err
	}
	defer rc.Close()
	buf := make([]byte, 256<<10)
	var carry []byte
	var read int64
	count := func(b []byte) {
		for {
			i := bytes.IndexByte(b, '<')
			if i < 0 || i+1 >= len(b) {
				return
			}
			c := b[i+1]
			b = b[i+1:]
			if c == '/' || c == '?' || c == '!' {
				continue
			}
			elements++
			if len(b) > 1 {
				d := b[1]
				if d == ' ' || d == '>' || d == '/' {
					switch c {
					case 'c':
						cells++
					case 'f':
						formulas++
					}
				}
			}
		}
	}
	for {
		m, rerr := rc.Read(buf)
		if m > 0 {
			read += int64(m)
			if read > max {
				return 0, 0, 0, ErrTooLarge
			}
			chunk := append(append([]byte(nil), carry...), buf[:m]...)
			if cut := bytes.LastIndexByte(chunk, '<'); cut >= 0 {
				count(chunk[:cut])
				carry = chunk[cut:]
			} else {
				count(chunk)
				carry = nil
			}
		}
		if rerr == io.EOF {
			count(carry)
			return cells, formulas, elements, nil
		}
		if rerr != nil {
			return 0, 0, 0, rerr
		}
	}
}

// ImportResult is a converted workbook.
type ImportResult struct {
	Native   []byte // compact native JSON
	Archive  *ArchiveReport
	KeptCell int64 // cell records in Native
}

// ImportXLSX converts an xlsx package to native JSON with a one-shot engine
// worker. launch starts the worker (ExecLauncher in production).
func ImportXLSX(ctx context.Context, launch Launcher, tempRoot string, data []byte, lim ArchiveLimits) (*ImportResult, error) {
	lim = lim.resolved()
	rep, err := CheckArchive(data, lim)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(tempRoot, "craft-import-")
	if err != nil {
		return nil, ErrImportFailed
	}
	defer os.RemoveAll(dir)
	if err = os.WriteFile(filepath.Join(dir, "in.xlsx"), data, 0o600); err != nil {
		return nil, ErrImportFailed
	}
	w, err := launch(ctx, dir, []string{"run", "--in", "/work/in.xlsx", "--quiet", "--out", "/work/doc.json"})
	if err != nil {
		return nil, ErrImportFailed
	}
	defer w.Close()
	if err = w.Wait(ctx); err != nil {
		return nil, ErrImportFailed
	}
	info, err := os.Lstat(filepath.Join(dir, "doc.json"))
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 8*lim.MaxNativeBytes+(64<<20) {
		return nil, ErrImportFailed
	}
	pretty, err := os.ReadFile(filepath.Join(dir, "doc.json"))
	if err != nil {
		return nil, ErrImportFailed
	}
	var compact bytes.Buffer
	if json.Compact(&compact, pretty) != nil {
		return nil, ErrImportFailed
	}
	if int64(compact.Len()) > lim.MaxNativeBytes {
		return nil, fmt.Errorf("%w: the document is %d bytes compact", ErrTooLarge, compact.Len())
	}
	// Every cell of the file with a value, formula, style or metadata must be in the document at
	// its sheet and address (a sum over the workbook would let an extra record hide a lost one).
	if err = verifyKept(data, compact.Bytes(), lim.MaxPartBytes); err != nil {
		return nil, err
	}
	kept := int64(bytes.Count(compact.Bytes(), []byte(`"value":{"t":"`)))
	if note := heldNote(heldFormulas(compact.Bytes())); note != "" {
		rep.Notes = append(rep.Notes, note)
	}
	return &ImportResult{Native: compact.Bytes(), Archive: rep, KeptCell: kept}, nil
}
