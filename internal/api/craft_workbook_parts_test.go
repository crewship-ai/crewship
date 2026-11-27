package api

import (
	"archive/zip"
	"bytes"
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
		if v, ok := set[f.Name]; ok {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(v)); err != nil {
				t.Fatal(err)
			}
		} else if err := zw.Copy(f); err != nil {
			t.Fatal(err)
		}
	}
	for name, v := range set {
		if f, err := zr.Open(name); err == nil {
			_ = f.Close()
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(v)); err != nil {
			t.Fatal(err)
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
		"dropped": workbookEdited(t, rig.original, "xl/vbaProject.bin", map[string]string{"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`}),
		"changed": workbookEdited(t, rig.original, "", map[string]string{"xl/vbaProject.bin": "\xd0\xcf\x11\xe0other", "xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`}),
	} {
		w := rig.save(body, "1", workbookHash(body))
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"fidelity_loss"`) || !strings.Contains(w.Body.String(), `"vba_project"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if doc := workbookRead(t, rig.read(nil)); doc.Version != "1" {
		t.Fatalf("a refused save was stored: %s", doc.Version)
	}
	kept := workbookEdited(t, rig.original, "", map[string]string{"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`})
	if w := rig.save(kept, "1", workbookHash(kept)); w.Code != 200 {
		t.Fatalf("a save keeping the VBA project: %d %s", w.Code, w.Body)
	}
}

func TestWorkbookSaveFidelityRefusesAnUnreadableBase(t *testing.T) {
	valid := craftTestWorkbook(t, "xlsm", "original")
	base := workbookEdited(t, valid, "", map[string]string{
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`,
	})
	rig := workbookDocumentsFixture(t, base)
	w := rig.save(valid, "1", workbookHash(valid))
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"fidelity_loss"`) || rig.sitHead(t) != 1 {
		t.Fatalf("unreadable base: %d %s, head %d", w.Code, w.Body, rig.sitHead(t))
	}
}

func TestWorkbookSaveFidelityRefusesAChangedSignedWorkbook(t *testing.T) {
	base := craftTestWorkbook(t, "xlsm", "original")
	signed := workbookEdited(t, base, "", map[string]string{"_xmlsignatures/sig1.xml": "<Signature/>"})
	rig := workbookDocumentsFixture(t, signed)
	sheet := string(sitParts(t, signed)["xl/worksheets/sheet1.xml"])
	candidate := workbookEdited(t, signed, "", map[string]string{
		"xl/worksheets/sheet1.xml": strings.Replace(sheet, "original", "edited", 1),
	})
	w := rig.save(candidate, "1", workbookHash(candidate))
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"fidelity_loss"`) || !strings.Contains(w.Body.String(), `"xml_signatures"`) || rig.sitHead(t) != 1 {
		t.Fatalf("signed save: %d %s, head %d", w.Code, w.Body, rig.sitHead(t))
	}
}

func TestWorkbookSaveFidelityChecksWorksheetDimensionAgainstCells(t *testing.T) {
	const part = "xl/worksheets/sheet1.xml"
	original := craftTestWorkbook(t, "xlsm", "original")
	body := string(sitParts(t, original)[part])
	body = strings.Replace(body, "<sheetData>", `<dimension ref="A1"/><sheetData>`, 1)
	base := workbookEdited(t, original, "", map[string]string{part: body})
	edit := strings.Replace(body, "</sheetData>", `<row r="2"><c r="C2"><v>9</v></c></row></sheetData>`, 1)
	for _, tc := range []struct {
		name, body, scope string
		status            int
	}{
		{"precise expansion", strings.Replace(edit, `ref="A1"`, `ref="A1:C2"`, 1), `["'L'!C2"]`, 200},
		{"false extent", strings.Replace(edit, `ref="A1"`, `ref="A1:D2"`, 1), `["'L'!C2"]`, 422},
		{"new cell outside reported extent", edit, `["'L'!C2"]`, 422},
		{"expansion without actual cells", strings.Replace(body, `ref="A1"`, `ref="A1:C2"`, 1), `["'L'!C2"]`, 422},
		{"outside declared edits", strings.Replace(edit, `ref="A1"`, `ref="A1:C2"`, 1), `["'L'!A1"]`, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := workbookDocumentsFixture(t, base)
			rig.touched = &tc.scope
			body := workbookEdited(t, base, "", map[string]string{part: tc.body})
			w := rig.save(body, "1", workbookHash(body))
			if w.Code != tc.status {
				t.Fatalf("status %d want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.status == 422 && (!strings.Contains(w.Body.String(), `"fidelity_loss"`) || rig.sitHead(t) != 1) {
				t.Fatalf("non-fidelity refusal or version created: %s, head %d", w.Body, rig.sitHead(t))
			}
		})
	}
}
