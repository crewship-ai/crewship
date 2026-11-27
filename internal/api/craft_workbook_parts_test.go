package api

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// workbookEdited copies a workbook file, leaving out drop and replacing the parts in set.
func workbookEdited(t *testing.T, src []byte, drop string, set map[string]string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if f.Name == drop {
			continue
		}
		w, _ := zw.Create(f.Name)
		if v, ok := set[f.Name]; ok {
			_, _ = w.Write([]byte(v))
			continue
		}
		r, _ := f.Open()
		_, _ = io.Copy(w, r)
		r.Close()
	}
	for name, v := range set { // parts the source does not have are added
		if _, err := zr.Open(name); err != nil {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(v))
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A person's save of a workbook Sheet keeps every part the editor does not change (the VBA project,
// its signatures, XML signatures, custom XML) byte for byte: a file without one, or with one
// changed, is refused as fidelity_loss naming it, and nothing is stored.
func TestWorkbookSaveKeepsTheVBAProjectByteForByte(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	for name, body := range map[string][]byte{
		"dropped": workbookEdited(t, rig.original, "xl/vbaProject.bin", map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData/></worksheet>`}),
		"changed": workbookEdited(t, rig.original, "", map[string]string{"xl/vbaProject.bin": "\xd0\xcf\x11\xe0other", "xl/worksheets/sheet1.xml": `<worksheet><sheetData/></worksheet>`}),
	} {
		w := rig.save(body, "1", workbookHash(body))
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"fidelity_loss"`) || !strings.Contains(w.Body.String(), `"vba_project"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if doc := workbookRead(t, rig.read(nil)); doc.Version != "1" {
		t.Fatalf("a refused save was stored: %s", doc.Version)
	}
	kept := workbookEdited(t, rig.original, "", map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData/></worksheet>`})
	if w := rig.save(kept, "1", workbookHash(kept)); w.Code != 200 {
		t.Fatalf("a save keeping the VBA project: %d %s", w.Code, w.Body)
	}
}
