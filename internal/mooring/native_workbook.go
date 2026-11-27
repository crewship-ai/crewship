package mooring

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// A workbook Sheet (NativeFormatWorkbook) keeps the xlsx or xlsm file itself as each version: the
// file a person uploaded is version 1, byte for byte, and every save stores the editor's file. No
// native JSON is derived, and a VBA project travels unread and unexecuted. The store only decides
// what the file is; the host's Validate bounds the archive (entries, unpacked size, ratios).
const (
	NativeFormatWorkbook = "workbook"

	WorkbookMediaTypeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	WorkbookMediaTypeXLSM = "application/vnd.ms-excel.sheet.macroEnabled.12"

	// MaxWorkbookBytes bounds the compressed file of one version.
	MaxWorkbookBytes = 64 << 20
	// maxContentTypesBytes bounds the one part the store reads to tell the format.
	maxContentTypesBytes = 1 << 20
)

var (
	ErrWorkbookNotWorkbook = errors.New("not an xlsx or xlsm workbook")
	ErrWorkbookEncrypted   = errors.New("the workbook is password protected")
	ErrWorkbookUnsupported = errors.New("workbook format not supported (xls, xlsb, ods)")
	ErrWorkbookTooLarge    = errors.New("the workbook file is larger than 64 MiB")
	// ErrNativeLegacy: a native JSON version of a Sheet that now keeps workbook files. It stays
	// listed and readable; it is never restored or served as the document.
	ErrNativeLegacy = errors.New("native JSON version of a workbook Sheet")
)

// NativeFile is one version's document: its stored name and media type, the format ("xlsx",
// "xlsm", or "native" for a JSON document), and its bytes.
type NativeFile struct {
	Name, MediaType, Format string
	Content                 []byte
}

var oleMagic = []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")

// utf16le is s in the UTF-16LE an OLE directory names its streams with.
func utf16le(s string) []byte {
	b := make([]byte, 0, 2*len(s))
	for _, c := range []byte(s) {
		b = append(b, c, 0)
	}
	return b
}

// DetectWorkbook tells an xlsx from an xlsm by the workbook part's content type, and names what
// else it is. It reads the zip directory and [Content_Types].xml only.
func DetectWorkbook(raw []byte) (format string, err error) {
	if len(raw) > MaxWorkbookBytes {
		return "", ErrWorkbookTooLarge
	}
	if bytes.HasPrefix(raw, oleMagic) {
		if bytes.Contains(raw, utf16le("EncryptedPackage")) {
			return "", ErrWorkbookEncrypted
		}
		return "", ErrWorkbookUnsupported // a legacy .xls
	}
	if len(raw) < 22 || !bytes.HasPrefix(raw, []byte("PK")) {
		return "", ErrWorkbookNotWorkbook
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", ErrWorkbookNotWorkbook
	}
	var types *zip.File
	hasWorkbook := false
	for _, f := range zr.File {
		switch f.Name {
		case "mimetype":
			return "", ErrWorkbookUnsupported // OpenDocument
		case "[Content_Types].xml":
			types = f
		case "xl/workbook.xml":
			hasWorkbook = true
		case "xl/workbook.bin":
			return "", ErrWorkbookUnsupported // xlsb
		}
	}
	if types == nil || types.UncompressedSize64 > maxContentTypesBytes {
		return "", ErrWorkbookNotWorkbook
	}
	rc, err := types.Open()
	if err != nil {
		return "", ErrWorkbookNotWorkbook
	}
	defer rc.Close()
	text, err := io.ReadAll(io.LimitReader(rc, maxContentTypesBytes+1))
	if err != nil || len(text) > maxContentTypesBytes {
		return "", ErrWorkbookNotWorkbook
	}
	content := strings.ToLower(string(text))
	switch {
	case strings.Contains(content, "sheet.binary.macroenabled.main"):
		return "", ErrWorkbookUnsupported
	case !hasWorkbook:
		return "", ErrWorkbookNotWorkbook
	case strings.Contains(content, "application/vnd.ms-excel.sheet.macroenabled.main+xml"):
		return "xlsm", nil
	case strings.Contains(content, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"),
		strings.Contains(content, "application/vnd.openxmlformats-officedocument.spreadsheetml.template.main+xml"),
		strings.Contains(content, "application/vnd.ms-excel.template.macroenabled.main+xml"):
		if strings.Contains(content, "macroenabled") {
			return "xlsm", nil
		}
		return "xlsx", nil
	}
	return "", ErrWorkbookNotWorkbook
}

func workbookFile(format string, content []byte) NativeFile {
	if format == "xlsm" {
		return NativeFile{Name: "workbook.xlsm", MediaType: WorkbookMediaTypeXLSM, Format: "xlsm", Content: content}
	}
	return NativeFile{Name: "workbook.xlsx", MediaType: WorkbookMediaTypeXLSX, Format: "xlsx", Content: content}
}

// workbookStore reports whether s keeps workbook files.
func (s *NativeStore) workbookStore() bool { return s.config.Format == NativeFormatWorkbook }

// manifest is the version request holding b: the native document.json, or the workbook file.
func (s *NativeStore) manifest(base int64, b []byte) artifacts.VersionRequest {
	if !s.workbookStore() {
		return nativeManifest(base, b)
	}
	format, _ := DetectWorkbook(b) // b passed candidate
	f := workbookFile(format, b)
	return artifacts.VersionRequest{BaseVersion: base, EntryPoint: f.Name, Files: map[string]artifacts.VersionFile{f.Name: {Content: b, MediaType: f.MediaType}}}
}

// DocumentFile is the document of version manifest m. A workbook Sheet's native JSON version is
// returned with ErrNativeLegacy, so a caller can list or show it but never serve or restore it.
func (s *NativeStore) DocumentFile(m artifacts.VersionManifest) (NativeFile, error) {
	if len(m.Files) != 1 {
		return NativeFile{}, ErrNativeProfile
	}
	if file, ok := m.Files["document.json"]; ok && m.EntryPoint == "document.json" && file.MediaType == "application/json" {
		f := NativeFile{Name: "document.json", MediaType: "application/json", Format: "native", Content: file.Content}
		if s.workbookStore() {
			return f, ErrNativeLegacy
		}
		return f, nil
	}
	if !s.workbookStore() {
		return NativeFile{}, ErrNativeProfile
	}
	for _, format := range []string{"xlsx", "xlsm"} {
		want := workbookFile(format, nil)
		if file, ok := m.Files[want.Name]; ok && m.EntryPoint == want.Name && file.MediaType == want.MediaType {
			return workbookFile(format, file.Content), nil
		}
	}
	return NativeFile{}, ErrNativeProfile
}

// Workbook reports whether this store keeps workbook files (NativeFormatWorkbook).
func (s *NativeStore) Workbook() bool { return s != nil && s.workbookStore() }

// maxVersionBytes is the per-version byte cap: a workbook file may use the whole 64 MiB, a native
// document keeps the version store's default.
func maxVersionBytes(c NativeStoreConfig) int64 {
	if c.Format == NativeFormatWorkbook {
		return MaxWorkbookBytes
	}
	return 0
}

// ValidateWorkbook bounds the archive before any editor receives the stored file.
func ValidateWorkbook(ctx context.Context, raw []byte) error {
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return ErrWorkbookNotWorkbook
	}
	if len(archive.File) > 10_000 {
		return ErrWorkbookTooLarge
	}
	seen := make(map[string]bool, len(archive.File))
	var total uint64
	var worksheets int
	for _, part := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSuffix(part.Name, "/")
		key := strings.ToLower(name)
		if !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00") ||
			seen[key] || part.Mode()&fs.ModeSymlink != 0 || part.Flags&1 != 0 {
			return ErrWorkbookNotWorkbook
		}
		seen[key] = true
		if strings.HasPrefix(name, "xl/worksheets/") && strings.HasSuffix(name, ".xml") {
			worksheets++
		}
		size, compressed := part.UncompressedSize64, part.CompressedSize64
		if size > 512<<20 || total+size > 1<<30 || size > 1<<20 && (compressed == 0 || size/compressed > 200) {
			return ErrWorkbookTooLarge
		}
		total += size
	}
	if worksheets == 0 || worksheets > 256 {
		return ErrWorkbookNotWorkbook
	}
	return nil
}

// WorkbookFormatOf is the format a version's entry point names: xlsx, xlsm, or native.
func WorkbookFormatOf(entryPoint string) string {
	switch entryPoint {
	case "workbook.xlsx":
		return "xlsx"
	case "workbook.xlsm":
		return "xlsm"
	}
	return "native"
}
