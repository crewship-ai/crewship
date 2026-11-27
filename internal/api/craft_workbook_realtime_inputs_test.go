package api

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestSheetITRealtimeTypedWorksheetInputEquality(t *testing.T) {
	// Exercise the actual relationship and shared string table. A valid
	// workbook can keep the table in a non-default part.
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for name, body := range map[string]string{
		craftWorkbookRelsPart:  `<Relationships xmlns="` + craftPackageRelsNS + `"><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="texts/strings.xml"/></Relationships>`,
		"xl/texts/strings.xml": `<sst xmlns="` + craftSpreadsheetNS + `"><si><t xml:space="preserve"> open B3 </t></si><si><r><t>=</t></r><r><t>abc</t></r></si></sst>`,
	} {
		part, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]*zip.File{}
	for _, p := range z.File {
		parts[p.Name] = p
	}
	shared := craftSharedStringReader(parts)
	read := func(attr, content string) (input, structure [32]byte) {
		t.Helper()
		d := xml.NewDecoder(strings.NewReader(`<row xmlns="` + craftSpreadsheetNS + `"><c r="B3"` + attr + `>` + content + `</c></row>`))
		if _, err := d.Token(); err != nil {
			t.Fatal(err)
		}
		token, err := d.Token()
		if err != nil {
			t.Fatal(err)
		}
		structure, _, err = craftWorksheetCell(d, token.(xml.StartElement), false, false, craftWorksheetHash{}, func(value [32]byte) error {
			input = value
			return nil
		}, shared)
		if err != nil {
			t.Fatal(err)
		}
		return input, structure
	}
	plain, plainXML := read(` t="inlineStr"`, `<is><t> open B3 </t></is>`)
	preserved, preservedXML := read(` t="inlineStr"`, `<is><t xml:space="preserve"> open B3 </t></is>`)
	fromShared, _ := read(` t="s"`, `<v>0</v>`)
	escaped, _ := read(` t="inlineStr"`, `<is><t>_x0020_open B3 </t></is>`)
	trimmed, _ := read(` t="inlineStr"`, `<is><t>open B3</t></is>`)
	if plain != preserved || plain != fromShared || plain != escaped || plain == trimmed || plainXML == preservedXML {
		t.Fatal("typed text should retain spaces and ignore representation while the fidelity projection keeps its own digest")
	}
	for _, tc := range []struct {
		name, attr, content, otherAttr, otherContent string
		shouldMatch                                  bool
	}{
		{"same numeric input", "", "<v>1.00</v>", ` t="n"`, "<v>1</v>", true},
		{"same boolean input", ` t="b"`, "<v>true</v>", ` t="b"`, "<v>1</v>", true},
		{"number and text", "", "<v>1</v>", ` t="inlineStr"`, "<is><t>1</t></is>", false},
		{"number and bool", "", "<v>1</v>", ` t="b"`, "<v>1</v>", false},
		{"formula and literal shared text", "", "<f>abc</f><v>9</v>", ` t="s"`, "<v>1</v>", false},
		{"formula cached value only", "", "<f>A1</f><v>123</v>", "", "<f>A1</f><v>456</v>", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := read(tc.attr, tc.content)
			b, _ := read(tc.otherAttr, tc.otherContent)
			if (a == b) != tc.shouldMatch {
				t.Fatalf("input equality = %v, want %v", a == b, tc.shouldMatch)
			}
		})
	}
}
