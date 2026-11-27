//go:build linux && real_gridcraft_engine

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/craftsession"
)

// The test binary is the engine worker too, as the server binary is in production
// (cmd/crewship runCraftEngineWorker).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == craftsession.WorkerFlag {
		os.Exit(craftengine.WorkerMain(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// sitPivotWorkbook is a synthetic xlsx: sheet L (Name, Status; 8 rows), sheet Other (Code; 2 rows)
// and two pivot tables on sheet P, cache 1 over L!A1:B9, cache 2 over Other!A1:A3.
func sitPivotWorkbook(t *testing.T) []byte {
	t.Helper()
	const ns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	const rel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	row := func(r int, cells ...string) string {
		var b strings.Builder
		fmt.Fprintf(&b, `<row r="%d">`, r)
		for i, v := range cells {
			fmt.Fprintf(&b, `<c r="%c%d" t="inlineStr"><is><t>%s</t></is></c>`, 'A'+i, r, v)
		}
		return b.String() + `</row>`
	}
	l := row(1, "Name", "Status")
	var records strings.Builder
	for i := 1; i <= 8; i++ {
		l += row(i+1, fmt.Sprintf("Item %d", i), "open")
		fmt.Fprintf(&records, `<r><x v="%d"/><x v="0"/></r>`, i-1)
	}
	var names strings.Builder
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&names, `<s v="Item %d"/>`, i)
	}
	sheet := func(rows string) string {
		return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet ` + ns + `><sheetData>` + rows + `</sheetData></worksheet>`
	}
	rels := func(items ...string) string {
		return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + strings.Join(items, "") + `</Relationships>`
	}
	r := func(id, typ, target string) string {
		return fmt.Sprintf(`<Relationship Id="%s" Type="%s" Target="%s"/>`, id, typ, target)
	}
	pivot := func(name string, cache int, ref string, fields string, rowItems string) string {
		return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><pivotTableDefinition ` + ns + ` name="` + name + `" cacheId="` + fmt.Sprint(cache) + `" dataCaption="Values" applyNumberFormats="0" applyBorderFormats="0" applyFontFormats="0" applyPatternFormats="0" applyAlignmentFormats="0" applyWidthHeightFormats="1" updatedVersion="6" minRefreshableVersion="3" createdVersion="6" outline="1" outlineData="1"><location ref="` + ref + `" firstHeaderRow="1" firstDataRow="1" firstDataCol="0"/>` + fields + rowItems + `</pivotTableDefinition>`
	}
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet3.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/pivotCache/pivotCacheDefinition1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"/><Override PartName="/xl/pivotCache/pivotCacheRecords1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheRecords+xml"/><Override PartName="/xl/pivotCache/pivotCacheDefinition2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"/><Override PartName="/xl/pivotCache/pivotCacheRecords2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheRecords+xml"/><Override PartName="/xl/pivotTables/pivotTable1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotTable+xml"/><Override PartName="/xl/pivotTables/pivotTable2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotTable+xml"/></Types>`},
		{"_rels/.rels", rels(r("rId1", rel+"officeDocument", "xl/workbook.xml"))},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook ` + ns + `><sheets><sheet name="L" sheetId="1" r:id="rId1"/><sheet name="Other" sheetId="2" r:id="rId2"/><sheet name="P" sheetId="3" r:id="rId3"/></sheets><pivotCaches><pivotCache cacheId="1" r:id="rId4"/><pivotCache cacheId="2" r:id="rId5"/></pivotCaches></workbook>`},
		{"xl/_rels/workbook.xml.rels", rels(r("rId1", rel+"worksheet", "worksheets/sheet1.xml"), r("rId2", rel+"worksheet", "worksheets/sheet2.xml"), r("rId3", rel+"worksheet", "worksheets/sheet3.xml"), r("rId4", rel+"pivotCacheDefinition", "pivotCache/pivotCacheDefinition1.xml"), r("rId5", rel+"pivotCacheDefinition", "pivotCache/pivotCacheDefinition2.xml"))},
		{"xl/worksheets/sheet1.xml", sheet(l)},
		{"xl/worksheets/sheet2.xml", sheet(row(1, "Code") + row(2, "X1") + row(3, "X2"))},
		{"xl/worksheets/sheet3.xml", sheet(row(3, "Row Labels") + row(4, "open") + row(10, "Row Labels") + row(11, "X1") + row(12, "X2"))},
		{"xl/worksheets/_rels/sheet3.xml.rels", rels(r("rId1", rel+"pivotTable", "../pivotTables/pivotTable1.xml"), r("rId2", rel+"pivotTable", "../pivotTables/pivotTable2.xml"))},
		{"xl/pivotCache/pivotCacheDefinition1.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><pivotCacheDefinition ` + ns + ` r:id="rId1" createdVersion="6" refreshedVersion="6" minRefreshableVersion="3" recordCount="8"><cacheSource type="worksheet"><worksheetSource ref="A1:B9" sheet="L"/></cacheSource><cacheFields count="2"><cacheField name="Name" numFmtId="0"><sharedItems count="8">` + names.String() + `</sharedItems></cacheField><cacheField name="Status" numFmtId="0"><sharedItems count="1"><s v="open"/></sharedItems></cacheField></cacheFields></pivotCacheDefinition>`},
		{"xl/pivotCache/_rels/pivotCacheDefinition1.xml.rels", rels(r("rId1", rel+"pivotCacheRecords", "pivotCacheRecords1.xml"))},
		{"xl/pivotCache/pivotCacheRecords1.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><pivotCacheRecords ` + ns + ` count="8">` + records.String() + `</pivotCacheRecords>`},
		{"xl/pivotCache/pivotCacheDefinition2.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><pivotCacheDefinition ` + ns + ` r:id="rId1" createdVersion="6" refreshedVersion="6" minRefreshableVersion="3" recordCount="2"><cacheSource type="worksheet"><worksheetSource ref="A1:A3" sheet="Other"/></cacheSource><cacheFields count="1"><cacheField name="Code" numFmtId="0"><sharedItems count="2"><s v="X1"/><s v="X2"/></sharedItems></cacheField></cacheFields></pivotCacheDefinition>`},
		{"xl/pivotCache/_rels/pivotCacheDefinition2.xml.rels", rels(r("rId1", rel+"pivotCacheRecords", "pivotCacheRecords2.xml"))},
		{"xl/pivotCache/pivotCacheRecords2.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><pivotCacheRecords ` + ns + ` count="2"><r><x v="0"/></r><r><x v="1"/></r></pivotCacheRecords>`},
		{"xl/pivotTables/pivotTable1.xml", pivot("P1", 1, "A3:A4", `<pivotFields count="2"><pivotField showAll="0"/><pivotField axis="axisRow" showAll="0"><items count="2"><item x="0"/><item t="default"/></items></pivotField></pivotFields>`, `<rowFields count="1"><field x="1"/></rowFields>`)},
		{"xl/pivotTables/_rels/pivotTable1.xml.rels", rels(r("rId1", rel+"pivotCacheDefinition", "../pivotCache/pivotCacheDefinition1.xml"))},
		{"xl/pivotTables/pivotTable2.xml", pivot("P2", 2, "A10:A12", `<pivotFields count="1"><pivotField axis="axisRow" showAll="0"><items count="3"><item x="0"/><item x="1"/><item t="default"/></items></pivotField></pivotFields>`, `<rowFields count="1"><field x="0"/></rowFields>`)},
		{"xl/pivotTables/_rels/pivotTable2.xml.rels", rels(r("rId1", rel+"pivotCacheDefinition", "../pivotCache/pivotCacheDefinition2.xml"))},
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, p.body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// sitCacheMeaning is what a pivot cache definition means: its source, its fields and their items.
type sitCacheMeaning struct {
	RefreshOnLoad string `xml:"refreshOnLoad,attr"`
	Source        struct {
		Type      string `xml:"type,attr"`
		Worksheet struct {
			Ref   string `xml:"ref,attr"`
			Sheet string `xml:"sheet,attr"`
		} `xml:"worksheetSource"`
	} `xml:"cacheSource"`
	Fields []struct {
		Name   string `xml:"name,attr"`
		Shared struct {
			Items []struct {
				XMLName xml.Name
				V       string `xml:"v,attr"`
			} `xml:",any"`
		} `xml:"sharedItems"`
	} `xml:"cacheFields>cacheField"`
}

func sitCaches(t *testing.T, raw []byte) map[string]sitCacheMeaning {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]sitCacheMeaning{}
	for _, f := range z.File {
		if !strings.HasPrefix(f.Name, "xl/pivotCache/pivotCacheDefinition") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		var m sitCacheMeaning
		if err := xml.Unmarshal(body, &m); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		out[m.Source.Worksheet.Sheet+"!"+m.Source.Worksheet.Ref] = m
	}
	return out
}

// Pivot refreshOnLoad with the real Sheet engine (the gridcraft engine module of the package, run
// through the production agent adapter, nativeCraftAgentEngine): an agent change in the source of
// one pivot cache marks only that cache refreshOnLoad="1"; the cache over the unchanged sheet gets
// no refreshOnLoad and means the same (source, fields, items). Byte equality is not required.
// Run: GRIDCRAFT_ENGINE_WASM=/path/engine.wasm go test -tags real_gridcraft_engine ./internal/server -run TestSheetITPivot
func TestSheetITPivotRefreshOnLoadWithTheRealEngine(t *testing.T) {
	engine := os.Getenv("GRIDCRAFT_ENGINE_WASM")
	if engine == "" {
		t.Fatal("real_gridcraft_engine needs GRIDCRAFT_ENGINE_WASM, the gridcraft engine module; a stand-in engine does not count")
	}
	module, err := os.ReadFile(engine)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(module)
	profile := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: engine, CLISHA256: hex.EncodeToString(sum[:])}
	sessions := &sheetSessions{}
	t.Cleanup(sessions.Close)
	apply, _ := nativeCraftAgentEngine(sessions)
	base := sitPivotWorkbook(t)
	before := sitCaches(t, base)
	if len(before) != 2 || len(before["L!A1:B9"].Fields) != 2 || len(before["L!A1:B9"].Fields[0].Shared.Items) != 8 || len(before["Other!A1:A3"].Fields[0].Shared.Items) != 2 {
		t.Fatalf("synthetic workbook caches are not read as meant: %+v", before)
	}
	for _, tc := range []struct {
		name, sheet, cell, changed, kept string
	}{
		{"change in L", "L", "B8", "L!A1:B9", "Other!A1:A3"},
		{"change in Other", "Other", "A2", "Other!A1:A3", "L!A1:B9"},
		{"change outside both sources", "Other", "D9", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			commands, _ := json.Marshal([]map[string]any{{"command": "cell.set", "params": map[string]any{"sheet": tc.sheet, "cell": tc.cell, "input": "done"}}})
			out, err := apply(ctx, profile, base, commands)
			if err != nil {
				t.Fatalf("apply with the real engine: %v", err)
			}
			after := sitCaches(t, out)
			if tc.changed == "" {
				for source, c := range after {
					if c.RefreshOnLoad != "" || !reflect.DeepEqual(c, before[source]) {
						t.Errorf("a change outside every source marked or changed the cache over %s: %+v", source, c)
					}
				}
				return
			}
			changed, kept := after[tc.changed], after[tc.kept]
			if changed.RefreshOnLoad != "1" && changed.RefreshOnLoad != "true" {
				t.Errorf("cache over %s (changed at %s) refreshOnLoad=%q, want 1", tc.changed, tc.cell, changed.RefreshOnLoad)
			}
			if kept.RefreshOnLoad != "" {
				t.Errorf("cache over the unchanged %s refreshOnLoad=%q, want none", tc.kept, kept.RefreshOnLoad)
			}
			if want := before[tc.kept]; !reflect.DeepEqual(kept, want) {
				t.Errorf("cache over the unchanged %s changed meaning:\n got %+v\nwant %+v", tc.kept, kept, want)
			}
			if len(after) != 2 {
				t.Errorf("caches after the change: %d", len(after))
			}
		})
	}
}
