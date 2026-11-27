package mooring

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// testWorkbook is a minimal workbook archive: main part xlsx, xlsm (with a VBA project) or xlsb.
func testWorkbook(t *testing.T, format string, extra map[string][]byte) []byte {
	t.Helper()
	main := map[string]string{
		"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml",
		"xlsm": "application/vnd.ms-excel.sheet.macroEnabled.main+xml",
		"xlsb": "application/vnd.ms-excel.sheet.binary.macroEnabled.main",
	}[format]
	parts := map[string][]byte{
		"[Content_Types].xml":      []byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="` + main + `"/></Types>`),
		"xl/workbook.xml":          []byte(`<workbook/>`),
		"xl/worksheets/sheet1.xml": []byte(`<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`),
	}
	if format == "xlsm" {
		parts["xl/vbaProject.bin"] = []byte("\xd0\xcf\x11\xe0VBA-project-bytes")
	}
	for k, v := range extra {
		parts[k] = v
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"[Content_Types].xml", "xl/workbook.xml", "xl/worksheets/sheet1.xml", "xl/vbaProject.bin", "mimetype"} {
		if body, ok := parts[name]; ok {
			w, _ := zw.Create(name)
			_, _ = w.Write(body)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func workbookStoreFixture(t *testing.T, format string) (*NativeStore, *sql.DB, EditLeaseScope, EditLeasePrincipal) {
	t.Helper()
	_, db, clock, key := ownerFixture(t)
	scope := EditLeaseScope{key.WorkspaceID, key.ArtifactID}
	p := EditLeasePrincipal{"u", "native-tab-a"}
	leases := NewEditLeaseStore(db.DB, EditLeaseConfig{Enabled: true, Clock: clock, TTL: 5 * time.Second, Authority: func(context.Context, *sql.Tx, EditLeaseAccess) error { return nil }})
	s := NewNativeStore(db.DB, NativeStoreConfig{
		Enabled: true, Application: "gridcraft", Format: format, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Leases: leases,
		Authorize: func(context.Context, *sql.Tx, EditLeaseScope, string) (EditLeasePrincipal, error) { return p, nil },
		Validate: func(_ context.Context, b []byte) error {
			if format == "" && !json.Valid(b) {
				return ErrInvalid
			}
			return nil
		},
		MaxHistoryBytes: 512 << 20,
	})
	return s, db.DB, scope, p
}

// A workbook Sheet keeps the uploaded file byte for byte as its version, named and typed by its
// format; a macro workbook keeps its VBA project untouched.
func TestWorkbookStoreKeepsTheFileAsTheVersion(t *testing.T) {
	s, _, scope, p := workbookStoreFixture(t, NativeFormatWorkbook)
	original := testWorkbook(t, "xlsm", nil)
	l, r, err := s.Initialize(t.Context(), scope, p, original)
	if err != nil || r.Version != 1 {
		t.Fatalf("initialize: %+v %v", r, err)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.DocumentFile(state.Manifest)
	if err != nil || f.Name != "workbook.xlsm" || f.Format != "xlsm" || f.MediaType != WorkbookMediaTypeXLSM || !bytes.Equal(f.Content, original) {
		t.Fatalf("v1 file %q %q %q %v (same bytes %v)", f.Name, f.Format, f.MediaType, err, bytes.Equal(f.Content, original))
	}
	if state.Manifest.EntryPoint != "workbook.xlsm" || len(state.Manifest.Files) != 1 {
		t.Fatalf("manifest %+v", state.Manifest)
	}
	edited := testWorkbook(t, "xlsx", nil)
	r2, err := s.SaveAttempt(t.Context(), l, 1, "workbook:2", nativeReceiptHash(edited), edited, "workbook-attempt")
	if err != nil || r2.Version != "2" {
		t.Fatalf("save: %+v %v", r2, err)
	}
	reconciled, err := s.ReconcileAttempt(t.Context(), l, 1, "workbook:2", nativeReceiptHash(edited), "workbook-attempt")
	if err != nil || reconciled != r2 {
		t.Fatalf("reconcile: %+v %v, want %+v", reconciled, err, r2)
	}
	state, _ = s.Read(t.Context(), scope)
	if f, err = s.DocumentFile(state.Manifest); err != nil || f.Format != "xlsx" || f.MediaType != WorkbookMediaTypeXLSX || !bytes.Equal(f.Content, edited) {
		t.Fatalf("v2 file %+v %v", f.Name, err)
	}
	v1, _, err := s.ReadVersion(t.Context(), scope, 1)
	if f, err = s.DocumentFile(v1); err != nil || !bytes.Equal(f.Content, original) {
		t.Fatalf("version 1 is not the original: %v", err)
	}
}

// What is not a workbook archive, or a workbook this Sheet cannot keep, is refused by name.
func TestWorkbookStoreRefusesWhatIsNotAWorkbook(t *testing.T) {
	s, _, scope, p := workbookStoreFixture(t, NativeFormatWorkbook)
	ole := append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), make([]byte, 512)...)
	encrypted := append(append([]byte(nil), ole...), []byte("E\x00n\x00c\x00r\x00y\x00p\x00t\x00e\x00d\x00P\x00a\x00c\x00k\x00a\x00g\x00e\x00")...)
	for _, tc := range []struct {
		name string
		raw  []byte
		want error
	}{
		{"native json", []byte(`{"sheets":[]}`), ErrWorkbookNotWorkbook},
		{"empty", nil, ErrWorkbookNotWorkbook},
		{"zip without workbook", testWorkbook(t, "xlsx", map[string][]byte{"xl/workbook.xml": nil})[:30], ErrWorkbookNotWorkbook},
		{"encrypted", encrypted, ErrWorkbookEncrypted},
		{"legacy xls", ole, ErrWorkbookUnsupported},
		{"xlsb", testWorkbook(t, "xlsb", nil), ErrWorkbookUnsupported},
		{"ods", testWorkbook(t, "xlsx", map[string][]byte{"mimetype": []byte("application/vnd.oasis.opendocument.spreadsheet")}), ErrWorkbookUnsupported},
		{"over 64 MiB", append(testWorkbook(t, "xlsx", nil), make([]byte, 64<<20)...), ErrWorkbookTooLarge},
	} {
		if _, _, err := s.Initialize(t.Context(), scope, p, tc.raw); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := s.Read(t.Context(), scope); err == nil {
		t.Fatal("a refused workbook created a page")
	}
}

// After migration the old native versions stay listed and readable, but a workbook Sheet never
// restores one: it would put native JSON back as the document.
func TestWorkbookStoreDoesNotRestoreALegacyNativeVersion(t *testing.T) {
	legacy, db, scope, p := workbookStoreFixture(t, "")
	l, _, err := legacy.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := legacy.config
	cfg.Format = NativeFormatWorkbook
	cfg.Validate = func(context.Context, []byte) error { return nil } // the host's archive check
	s := NewNativeStore(db, cfg)
	if _, err := s.Save(t.Context(), l, 1, testWorkbook(t, "xlsx", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(t.Context(), l, 2, 1); !errors.Is(err, ErrNativeLegacy) {
		t.Fatalf("restore of a native version: %v", err)
	}
	v1, _, _ := s.ReadVersion(t.Context(), scope, 1)
	if f, err := s.DocumentFile(v1); !errors.Is(err, ErrNativeLegacy) || f.Format != "native" || string(f.Content) != `{"v":1}` {
		t.Fatalf("legacy version file %+v %v", f, err)
	}
}
