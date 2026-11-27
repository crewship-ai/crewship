//go:build linux && real_gridcraft_engine

package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/craftsession"
)

func sheetFidelityFixture(t *testing.T, customProperty bool) []byte {
	t.Helper()
	const ns = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	const rel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	types := `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`
	sheet := `<worksheet xmlns="` + ns + `" xmlns:r="` + rel + `"><sheetData><row r="1"><c r="A1"><v>2</v></c><c r="B1"><f>A1*3</f><v>6</v></c></row></sheetData>`
	workbook := `<workbook xmlns="` + ns + `" xmlns:r="` + rel + `"><sheets><sheet name="Synthetic" sheetId="1" r:id="rId1"/></sheets>`
	parts := map[string]string{
		"_rels/.rels":                `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="` + rel + `/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="` + rel + `/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
	}
	if customProperty {
		types += `<Override PartName="/xl/customProperty1.bin" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.customProperty"/>`
		sheet += `<autoFilter ref="A1:B1"/><customProperties><customPr name="SyntheticProperty" r:id="rIdProperty"/></customProperties>`
		workbook += `<definedNames><definedName name="_xlnm._FilterDatabase" hidden="1" localSheetId="0">'Synthetic'!$A$1:$B$1</definedName></definedNames>`
		parts["xl/customProperty1.bin"] = "\x00synthetic property\xff"
		parts["xl/worksheets/_rels/sheet1.xml.rels"] = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdProperty" Type="` + rel + `/customProperty" Target="../customProperty1.bin"/></Relationships>`
	}
	parts["[Content_Types].xml"] = types + "</Types>"
	parts["xl/workbook.xml"] = workbook + "</workbook>"
	parts["xl/worksheets/sheet1.xml"] = sheet + "</worksheet>"

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for part, data := range parts {
		w, err := zw.Create(part)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sheetFidelityParts(t *testing.T, snapshot []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(snapshot), int64(len(snapshot)))
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]byte, len(zr.File))
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[file.Name] = data
	}
	return out
}

func checkSheetFidelityFormula(t *testing.T, snapshot []byte, input, cache string) {
	t.Helper()
	var sheet struct {
		Cells []struct {
			Address string `xml:"r,attr"`
			Formula string `xml:"f"`
			Value   string `xml:"v"`
		} `xml:"sheetData>row>c"`
	}
	if err := xml.Unmarshal(sheetFidelityParts(t, snapshot)["xl/worksheets/sheet1.xml"], &sheet); err != nil {
		t.Fatal(err)
	}
	if len(sheet.Cells) != 2 || sheet.Cells[0].Address != "A1" || sheet.Cells[0].Value != input ||
		sheet.Cells[1].Address != "B1" || sheet.Cells[1].Formula != "A1*3" || sheet.Cells[1].Value != cache {
		t.Fatalf("cells: %+v, want A1=%s and B1=A1*3 with cache %s", sheet.Cells, input, cache)
	}
}

func TestSheetITWorkbookFidelityWithTheRealEngine(t *testing.T) {
	file := os.Getenv("GRIDCRAFT_ENGINE_WASM")
	if file == "" {
		t.Fatal("set GRIDCRAFT_ENGINE_WASM to the GridCraft module from the CI package")
	}
	module, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(module)
	profile := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: file, CLISHA256: hex.EncodeToString(digest[:]), Workbook: true}
	t.Logf("GridCraft engine SHA-256 %s", profile.CLISHA256)
	sessions := &sheetSessions{}
	t.Cleanup(sessions.Close)
	apply, _ := nativeCraftAgentEngine(sessions)
	command := func(input string) json.RawMessage {
		return json.RawMessage(`[{"command":"cell.set","params":{"sheet":"Synthetic","cell":"A1","input":"` + input + `"}}]`)
	}

	t.Run("recalculation and inverse on an engine-authored workbook", func(t *testing.T) {
		// Use the writer's own package as the starting point so this checks a cell edit
		// and calculation, independently of any normalization on a third-party import.
		engine := sheetEngine{sessions.manager(profile)}
		session, release, err := engine.Open(t.Context(), sheetFidelityFixture(t, false))
		if err != nil {
			t.Fatal(err)
		}
		base, err := session.Snapshot(t.Context())
		release()
		if err != nil {
			t.Fatal(err)
		}
		checkSheetFidelityFormula(t, base, "2", "6")
		changed, err := apply(t.Context(), profile, base, command("4"))
		if err != nil {
			t.Fatalf("agent write: %v", err)
		}
		checkSheetFidelityFormula(t, changed, "4", "12")
		undone, err := apply(t.Context(), profile, changed, command("2"))
		if err != nil {
			t.Fatalf("inverse write: %v", err)
		}
		checkSheetFidelityFormula(t, undone, "2", "6")
	})

	t.Run("custom property and filter name", func(t *testing.T) {
		base := sheetFidelityFixture(t, true)
		changed, err := apply(t.Context(), profile, base, command("4"))
		var lost *craftsession.FidelityLossError
		if errors.As(err, &lost) && len(changed) == 0 {
			t.Logf("guard refused the snapshot: %+v", lost.Parts)
			return
		}
		if err != nil {
			t.Fatalf("expected a lossless snapshot or fidelity_loss: %v", err)
		}
		before, after := sheetFidelityParts(t, base), sheetFidelityParts(t, changed)
		for _, part := range []string{"[Content_Types].xml", "xl/workbook.xml", "xl/customProperty1.bin", "xl/worksheets/_rels/sheet1.xml.rels"} {
			if !bytes.Equal(before[part], after[part]) {
				t.Errorf("changed protected part %s", part)
			}
		}
		if body := string(after["xl/worksheets/sheet1.xml"]); !strings.Contains(body, "rIdProperty") || !strings.Contains(body, "autoFilter") {
			t.Fatal("worksheet property or filter lost")
		}
		checkSheetFidelityFormula(t, changed, "4", "12")
	})
}
