package craftsession

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

// fakeOneShot stands in for `gridcraft-cli run --in X --quiet --out Y`: it reads in.xlsx and
// writes an indented native document the way the engine does (a blank cell is dropped), or
// crashes or hangs. Options after "faithful", comma separated, make it lose something on
// purpose: cut=N keeps the first N cells, drop=Sheet!A1 drops a cell, retype=Sheet!A1 empties
// its value, noformula=Sheet!A1 drops its formula, extra=Sheet!Z9 adds a cell, rename=Sheet
// renames a sheet, pad=N adds N filler cells.
func fakeOneShot(mode string) {
	switch mode {
	case "crash":
		os.Exit(1)
	case "hang":
		select {}
	}
	opts := map[string][]string{}
	for _, o := range strings.Split(mode, ",")[1:] {
		k, v, _ := strings.Cut(o, "=")
		opts[k] = append(opts[k], v)
	}
	has := func(k, sheet string, row, col uint32) bool {
		for _, v := range opts[k] {
			sh, ref, _ := strings.Cut(v, "!")
			if r, c, ok := parseCellRef(ref); ok && sh == sheet && r == row && c == col {
				return true
			}
		}
		return false
	}
	data, _ := os.ReadFile("in.xlsx")
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	books, _ := workbookSheets(zr)
	cut := -1
	if len(opts["cut"]) > 0 {
		cut, _ = strconv.Atoi(opts["cut"][0])
	}
	var sheets []any
	n := 0
	for _, b := range books {
		rows := map[string]map[string]any{}
		put := func(row, col uint32, cell map[string]any) {
			r := strconv.Itoa(int(row))
			if rows[r] == nil {
				rows[r] = map[string]any{}
			}
			rows[r][strconv.Itoa(int(col))] = cell
		}
		_ = readCells(files[b.part], 1<<30, func(c xlsxCell) error {
			if c.blank() || !c.address || has("drop", b.name, c.row, c.col) || cut >= 0 && n >= cut {
				return nil
			}
			n++
			value := map[string]any{"t": "Empty"}
			if c.value && !has("retype", b.name, c.row, c.col) {
				value = map[string]any{"t": c.nativeKind(), "v": 1}
			}
			var formula any
			if c.formula && !has("noformula", b.name, c.row, c.col) {
				formula = map[string]any{"text": "1+1", "array": nil}
			}
			style := 0
			if c.styled {
				style = 1
			}
			put(c.row, c.col, map[string]any{"value": value, "formula": formula, "style": style})
			return nil
		})
		for _, v := range opts["extra"] {
			sh, ref, _ := strings.Cut(v, "!")
			if r, c, ok := parseCellRef(ref); ok && sh == b.name {
				put(r, c, map[string]any{"value": map[string]any{"t": "Number", "v": 1}, "formula": nil, "style": 0})
			}
		}
		if len(opts["pad"]) > 0 {
			p, _ := strconv.Atoi(opts["pad"][0])
			for i := 0; i < p; i++ {
				put(uint32(1000+i), 0, map[string]any{"value": map[string]any{"t": "Number", "v": i}, "formula": nil, "style": 0})
			}
		}
		name := b.name
		for _, v := range opts["rename"] {
			if v == name {
				name += " (2)"
			}
		}
		sheets = append(sheets, map[string]any{"name": name, "cells": map[string]any{"bands": map[string]any{"0": map[string]any{"rows": rows}}}})
	}
	out, _ := json.MarshalIndent(map[string]any{"sheets": sheets}, "", "  ")
	_ = os.WriteFile("doc.json", out, 0o600)
}

func oneShotLauncher(mode string) Launcher {
	return func(ctx context.Context, dir string, engineArgs []string) (*craftengine.Worker, error) {
		exe, _ := os.Executable()
		return craftengine.StartWorker(craftengine.WorkerSpec{Exe: exe, Env: []string{"CRAFTSESSION_FAKE_ONESHOT=" + mode}, Application: "gridcraft",
			ModulePath: "/nonexistent", SHA256: strings.Repeat("0", 64), Dir: dir, EngineArgs: engineArgs})
	}
}

func xlsxZip(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return b.Bytes()
}

func sheetXML(cells int) string {
	var s strings.Builder
	s.WriteString(`<worksheet><cols><col min="1" max="2"/></cols><sheetData><row r="1">`)
	for i := 0; i < cells; i++ {
		fmt.Fprintf(&s, `<c r="A%d"><v>1</v></c>`, i+1)
	}
	s.WriteString(`</row></sheetData><conditionalFormatting sqref="A1"><cfRule type="x"/></conditionalFormatting></worksheet>`)
	return s.String()
}

func goodXLSX(t *testing.T, cells int) []byte {
	return bookXLSX(t, map[string]string{"Sheet1": sheetXML(cells)})
}

// bookXLSX is a workbook with these sheets, in name order, with their relationships.
func bookXLSX(t *testing.T, sheets map[string]string) []byte {
	names := make([]string, 0, len(sheets))
	for n := range sheets {
		names = append(names, n)
	}
	sort.Strings(names)
	var wb, rels strings.Builder
	wb.WriteString(`<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(`<Relationships>`)
	parts := map[string]string{"[Content_Types].xml": "<x/>"}
	for i, n := range names {
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, n, i+1, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		parts[fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)] = sheets[n]
	}
	wb.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	parts["xl/workbook.xml"], parts["xl/_rels/workbook.xml.rels"] = wb.String(), rels.String()
	return xlsxZip(t, parts)
}

func TestCheckArchiveCountsCellsAndNotesWhatIsNotCarried(t *testing.T) {
	data := xlsxZip(t, map[string]string{"xl/workbook.xml": "<workbook/>", "xl/worksheets/sheet1.xml": sheetXML(7), "xl/vbaProject.bin": "x",
		"xl/externalLinks/externalLink1.xml": "<x/>", "xl/pivotTables/pivotTable1.xml": "<x/>"})
	rep, err := CheckArchive(data, ArchiveLimits{})
	if err != nil || rep.Cells != 7 || rep.Formulas != 0 || rep.LargestPart < 7 || !rep.Macros || rep.ExternalLinks != 1 || rep.Pivots != 1 || rep.Worksheets != 1 || len(rep.Notes) != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestCheckArchiveRefusesWhatIsNotAWorkbookOrIsTooBig(t *testing.T) {
	zeros := strings.Repeat("\x00", 64<<20)
	cases := []struct {
		name string
		data []byte
		lim  ArchiveLimits
		want error
	}{
		{"not a zip", []byte("hello world, this is not a zip file at all"), ArchiveLimits{}, ErrNotXLSX},
		{"ole container (encrypted workbook)", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 600)...), ArchiveLimits{}, ErrNotXLSX},
		{"no workbook part", xlsxZip(t, map[string]string{"a.txt": "x"}), ArchiveLimits{}, ErrNotXLSX},
		{"path traversal", xlsxZip(t, map[string]string{"xl/workbook.xml": "<w/>", "xl/worksheets/s.xml": sheetXML(1), "../evil": "x"}), ArchiveLimits{}, ErrNotXLSX},
		{"absolute path", xlsxZip(t, map[string]string{"xl/workbook.xml": "<w/>", "xl/worksheets/s.xml": sheetXML(1), "/etc/passwd": "x"}), ArchiveLimits{}, ErrNotXLSX},
		{"too many entries", goodXLSX(t, 1), ArchiveLimits{MaxEntries: 2}, ErrTooLarge},
		{"too many cells", goodXLSX(t, 50), ArchiveLimits{MaxCellsPerFile: 10}, ErrTooLarge},
		{"sheet part over the engine's element cap", goodXLSX(t, 50), ArchiveLimits{MaxPartElements: 40}, ErrTooLarge},
		{"zip bomb entry", xlsxZip(t, map[string]string{"xl/workbook.xml": "<w/>", "xl/worksheets/s.xml": sheetXML(1), "xl/bomb.bin": zeros}), ArchiveLimits{}, ErrTooLarge},
		{"part over its cap", xlsxZip(t, map[string]string{"xl/workbook.xml": "<w/>", "xl/worksheets/s.xml": sheetXML(1), "xl/big.bin": strings.Repeat("a", 4096)}), ArchiveLimits{MaxPartBytes: 2048}, ErrTooLarge},
	}
	for _, c := range cases {
		if _, err := CheckArchive(c.data, c.lim); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestImportKeepsEveryCellAndReturnsCompactJSON(t *testing.T) {
	res, err := ImportXLSX(context.Background(), oneShotLauncher("faithful"), t.TempDir(), goodXLSX(t, 5), ArchiveLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.KeptCell != 5 || res.Archive.Cells != 5 || strings.ContainsAny(string(res.Native), "\n ") || !json.Valid(res.Native) {
		t.Fatalf("%d %d %q", res.KeptCell, res.Archive.Cells, res.Native[:40])
	}
}

func TestImportRefusesAShortenedSheetInsteadOfStoringIt(t *testing.T) {
	// the engine exits 0 after reading only part of a sheet; the file has 5 cells
	_, err := ImportXLSX(context.Background(), oneShotLauncher("faithful,cut=3"), t.TempDir(), goodXLSX(t, 5), ArchiveLimits{})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("shortened sheet: %v", err)
	}
}

func TestImportEngineCrashAndDeadline(t *testing.T) {
	if _, err := ImportXLSX(context.Background(), oneShotLauncher("crash"), t.TempDir(), goodXLSX(t, 1), ArchiveLimits{}); !errors.Is(err, ErrImportFailed) {
		t.Fatalf("crash: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := ImportXLSX(ctx, oneShotLauncher("hang"), t.TempDir(), goodXLSX(t, 1), ArchiveLimits{}); !errors.Is(err, ErrImportFailed) || time.Since(started) > 5*time.Second {
		t.Fatalf("hang: %v after %s", err, time.Since(started))
	}
}

func TestImportRefusesADocumentOverTheStoredSizeLimit(t *testing.T) {
	_, err := ImportXLSX(context.Background(), oneShotLauncher("faithful,pad=200"), t.TempDir(), goodXLSX(t, 5), ArchiveLimits{MaxNativeBytes: 1024})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized native document: %v", err)
	}
}

func TestImportLeavesNoFilesBehind(t *testing.T) {
	root := t.TempDir()
	_, _ = ImportXLSX(context.Background(), oneShotLauncher("faithful"), root, goodXLSX(t, 5), ArchiveLimits{})
	_, _ = ImportXLSX(context.Background(), oneShotLauncher("crash"), root, goodXLSX(t, 5), ArchiveLimits{})
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestCheckArchiveCountsFormulas(t *testing.T) {
	sheet := `<worksheet><sheetData><row><c r="A1"><f>1+1</f><v>2</v></c><c r="B1"><f t="shared" si="0"/><v>2</v></c><c r="C1"><v>3</v></c></row></sheetData></worksheet>`
	rep, err := CheckArchive(xlsxZip(t, map[string]string{"xl/workbook.xml": "<w/>", "xl/worksheets/s.xml": sheet}), ArchiveLimits{})
	if err != nil || rep.Cells != 3 || rep.Formulas != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// The client's workbook: unstyled cells without a value or formula (<c r="AI8"/>) are dropped
// by the engine as blank. That is no loss, and the import keeps every cell that has something.
func TestImportAcceptsBlankCellsTheEngineDropsAsEquivalent(t *testing.T) {
	sheet := `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c><c r="B1" t="s"><v>0</v></c><c r="C1"><f>A1+1</f><v>2</v></c>` +
		`<c r="D1"/><c r="E1"><v></v></c><c r="F1" s="0"/><c r="G1" s="3"/></row><row r="2"><c r="A2" t="b"><v>1</v></c><c r="B2" t="e"><v>#N/A</v></c>` +
		`<c r="C2"><f t="shared" ref="C2:C3" si="0">A2</f><v>1</v></c></row><row r="3"><c r="C3"><f t="shared" si="0"/><v>1</v></c><c r="D3"/></row></sheetData></worksheet>`
	res, err := ImportXLSX(context.Background(), oneShotLauncher("faithful"), t.TempDir(), bookXLSX(t, map[string]string{"Sheet1": sheet, "Sheet2": sheetXML(2)}), ArchiveLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive.Cells != 14 || res.KeptCell != 10 {
		t.Fatalf("file %d cells, kept %d", res.Archive.Cells, res.KeptCell)
	}
}

// A lost cell with content refuses the import and says which sheet and what kind, even when the
// engine has as many records as the file has cells (an extra one elsewhere hides it in a sum).
func TestImportRefusesALostCellWithContentAndSaysWhere(t *testing.T) {
	sheet := `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c><c r="B1"><v>2</v></c><c r="C1"><f>A1</f><v>1</v></c><c r="D1" s="2"/></row></sheetData></worksheet>`
	book := bookXLSX(t, map[string]string{"Data": sheet, "Notes": sheetXML(1)})
	cases := []struct {
		mode string
		want LostCells
	}{
		{"faithful,drop=Data!B1,extra=Data!Z9", LostCells{Sheet: 1, Name: "Data", Kind: LostValue, Count: 1}},
		{"faithful,retype=Data!A1", LostCells{Sheet: 1, Name: "Data", Kind: LostValue, Count: 1}},
		{"faithful,noformula=Data!C1,extra=Data!Z8", LostCells{Sheet: 1, Name: "Data", Kind: LostFormula, Count: 1}},
		{"faithful,drop=Data!D1,extra=Data!Z7", LostCells{Sheet: 1, Name: "Data", Kind: LostStyledEmpty, Count: 1}},
		{"faithful,rename=Notes", LostCells{Sheet: 2, Name: "Notes", Kind: LostSheet, Count: 1}},
	}
	for _, c := range cases {
		_, err := ImportXLSX(context.Background(), oneShotLauncher(c.mode), t.TempDir(), book, ArchiveLimits{})
		var lost *TruncatedError
		if !errors.Is(err, ErrTruncated) || !errors.As(err, &lost) || len(lost.Lost) != 1 || lost.Lost[0] != c.want {
			t.Errorf("%s: %v", c.mode, err)
		}
	}
}

// Two cells at one address: the engine keeps one of them, so the other one is lost.
func TestImportRefusesTwoCellsAtOneAddress(t *testing.T) {
	sheet := `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c><c r="A1"><v>2</v></c></row></sheetData></worksheet>`
	_, err := ImportXLSX(context.Background(), oneShotLauncher("faithful"), t.TempDir(), bookXLSX(t, map[string]string{"S": sheet}), ArchiveLimits{})
	var lost *TruncatedError
	if !errors.As(err, &lost) || lost.Lost[0].Kind != LostDuplicate {
		t.Fatalf("%v", err)
	}
}

// A sheet whose relationship is missing is an unreadable workbook, not an empty sheet.
func TestImportRefusesASheetWithoutItsPart(t *testing.T) {
	data := xlsxZip(t, map[string]string{"xl/workbook.xml": `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="A" r:id="rId1"/><sheet name="B" r:id="rId9"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`, "xl/worksheets/sheet1.xml": sheetXML(1)})
	if _, err := ImportXLSX(context.Background(), oneShotLauncher("faithful"), t.TempDir(), data, ArchiveLimits{}); !errors.Is(err, ErrNotXLSX) {
		t.Fatalf("%v", err)
	}
}
