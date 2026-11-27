package api

import (
	"fmt"
	"strings"
	"testing"
)

const (
	sitChainOverride     = `<Override PartName="/xl/calcChain.xml" ContentType="` + craftCalcChainType + `"/>`
	sitChainRelationship = `<Relationship Id="rIdChain" Type="` + craftCalcChainRelationship + `" Target="calcChain.xml"/>`
	sitChainComments     = "xl/comments1.xml"
	sitChainPivot        = "xl/pivotTables/pivotTable1.xml"
)

// Synthetic workbook with 570 clearable formulas, a separate pivot and a comment.
func sitCalcChainWorkbook(t *testing.T) (base, cleared []byte) {
	t.Helper()
	original := sitFidelityWorkbook(t, "xlsx")
	parts := sitParts(t, original)
	cells := sitInitialL()
	cells["F8"] = "comment anchor"
	cells["A600"] = "outside the clear"
	for row := 8; row <= 9; row++ {
		delete(cells, sitAddress(2, row))
	}
	after := sitSheetXML(cells)
	var chain strings.Builder
	chain.WriteString(`<calcChain xmlns="` + craftSpreadsheetNS + `">`)
	for row := 8; row <= 577; row++ {
		cells[sitAddress(2, row)] = fmt.Sprintf(`="L_"&E%d|L_`, row)
		if row == 8 {
			fmt.Fprintf(&chain, `<c r="B%d" i="1"/>`, row)
		} else {
			fmt.Fprintf(&chain, `<c r="B%d"/>`, row)
		}
	}
	chain.WriteString("</calcChain>")
	types := strings.Replace(string(parts[craftContentTypesPart]), "</Types>", `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/xl/comments1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.comments+xml"/><Override PartName="/xl/pivotTables/pivotTable1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotTable+xml"/><Override PartName="/xl/pivotCache/pivotCacheDefinition1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"/><Override PartName="/xl/pivotCache/pivotCacheDefinition2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"/></Types>`, 1)
	rels := string(parts[craftWorkbookRelsPart])
	const rel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	pivotRels := `<Relationships xmlns="` + craftPackageRelsNS + `"><Relationship Id="rId1" Type="` + rel + `pivotCacheDefinition" Target="../pivotCache/pivotCacheDefinition2.xml"/></Relationships>`
	base = workbookEdited(t, original, "", map[string]string{
		craftCalcChainPart:                          chain.String(),
		craftContentTypesPart:                       strings.Replace(types, "</Types>", sitChainOverride+"</Types>", 1),
		craftWorkbookRelsPart:                       strings.Replace(rels, "</Relationships>", sitChainRelationship+"</Relationships>", 1),
		sitFidelitySheet:                            sitSheetXML(cells),
		sitChainComments:                            `<comments xmlns="` + craftSpreadsheetNS + `"><authors><author>Synthetic</author></authors><commentList><comment ref="F8" authorId="0"><text><t>Synthetic note</t></text></comment></commentList></comments>`,
		"xl/worksheets/_rels/sheet1.xml.rels":       `<Relationships xmlns="` + craftPackageRelsNS + `"><Relationship Id="rIdComment" Type="` + rel + `comments" Target="../comments1.xml"/></Relationships>`,
		"xl/worksheets/_rels/sheet2.xml.rels":       `<Relationships xmlns="` + craftPackageRelsNS + `"><Relationship Id="rIdPivot" Type="` + rel + `pivotTable" Target="../pivotTables/pivotTable1.xml"/></Relationships>`,
		"xl/pivotCache/pivotCacheDefinition1.xml":   `<pivotCacheDefinition xmlns="` + craftSpreadsheetNS + `" recordCount="2"><cacheSource type="worksheet"><worksheetSource ref="A1:A3" sheet="Other"/></cacheSource><cacheFields count="1"><cacheField name="Code"><sharedItems count="2"><s v="X1"/><s v="X2"/></sharedItems></cacheField></cacheFields></pivotCacheDefinition>`,
		"xl/pivotTables/_rels/pivotTable1.xml.rels": pivotRels,
		sitChainPivot:                               `<pivotTableDefinition xmlns="` + craftSpreadsheetNS + `" name="CodePivot" cacheId="2" dataCaption="Values"><location ref="E3:E5" firstHeaderRow="1" firstDataRow="1" firstDataCol="0"/><pivotFields count="1"><pivotField axis="axisRow"><items count="2"><item x="0"/><item x="1"/></items></pivotField></pivotFields><rowFields count="1"><field x="0"/></rowFields></pivotTableDefinition>`,
	})
	cleared = workbookEdited(t, base, craftCalcChainPart, map[string]string{
		sitFidelitySheet:      after,
		craftContentTypesPart: types,
		craftWorkbookRelsPart: rels,
	})
	return base, cleared
}

func TestWorkbookCalcChainDropRequiresAnAddressedFormulaChange(t *testing.T) {
	base, cleared := sitCalcChainWorkbook(t)
	parts := sitParts(t, base)
	candidateParts := sitParts(t, cleared)
	var scope, wrongScope craftWorkbookEdits
	scope.add("'L'!B8:B577")
	wrongScope.add("'Other'!B8:B577")
	for _, tc := range []struct {
		name      string
		candidate []byte
		edits     craftWorkbookEdits
		code      string
	}{
		{"consistent drop", cleared, scope, ""},
		{"drop without formula change", workbookEdited(t, cleared, "", map[string]string{sitFidelitySheet: string(parts[sitFidelitySheet])}), scope, "calc_chain"},
		{"formulas outside touched range", cleared, wrongScope, "worksheet"},
		{"missing explicit touched range", cleared, craftWorkbookEdits{allowScalarValues: true}, "worksheet"},
		{"without relationship update", workbookEdited(t, cleared, "", map[string]string{craftWorkbookRelsPart: string(parts[craftWorkbookRelsPart])}), scope, "calc_chain"},
		{"other declaration changed", workbookEdited(t, cleared, "", map[string]string{craftContentTypesPart: strings.Replace(string(candidateParts[craftContentTypesPart]), "/xl/comments1.xml", "/xl/comments2.xml", 1)}), scope, "content_types"},
		{"other relationships changed", workbookEdited(t, cleared, "", map[string]string{craftWorkbookRelsPart: strings.Replace(string(candidateParts[craftWorkbookRelsPart]), "worksheets/sheet2.xml", "worksheets/sheet3.xml", 1)}), scope, "relationships"},
		{"comment removed", workbookEdited(t, cleared, sitChainComments, nil), scope, "package_parts"},
		{"pivot removed", workbookEdited(t, cleared, sitChainPivot, nil), scope, "package_parts"},
		{"calc chain rewritten", workbookEdited(t, base, "", map[string]string{craftCalcChainPart: strings.Replace(string(parts[craftCalcChainPart]), `r="B8"`, `r="C8"`, 1)}), scope, "calc_chain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lost := craftWorkbookPartsLost(t.Context(), base, tc.candidate, tc.edits)
			if tc.code == "" && len(lost) == 0 {
				return
			}
			for _, part := range lost {
				if part.Code == tc.code {
					return
				}
			}
			t.Fatalf("losses = %+v, want %q", lost, tc.code)
		})
	}
}
