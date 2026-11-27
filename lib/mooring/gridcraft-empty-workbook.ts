import { strToU8, zipSync } from "fflate"

/** A blank workbook with a single editable worksheet. */
export function emptyGridCraftWorkbook(): Uint8Array {
  const spreadsheet = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
  const documentRel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
  const packageRel = "http://schemas.openxmlformats.org/package/2006/relationships"
  const parts = {
    "[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
      <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
      <Default Extension="xml" ContentType="application/xml"/>
      <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
      <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
    </Types>`,
    "_rels/.rels": `<Relationships xmlns="${packageRel}">
      <Relationship Id="rId1" Type="${documentRel}/officeDocument" Target="xl/workbook.xml"/>
    </Relationships>`,
    "xl/workbook.xml": `<workbook xmlns="${spreadsheet}" xmlns:r="${documentRel}">
      <sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets>
    </workbook>`,
    "xl/_rels/workbook.xml.rels": `<Relationships xmlns="${packageRel}">
      <Relationship Id="rId1" Type="${documentRel}/worksheet" Target="worksheets/sheet1.xml"/>
    </Relationships>`,
    "xl/worksheets/sheet1.xml": `<worksheet xmlns="${spreadsheet}"><sheetData/></worksheet>`,
  }
  return zipSync(Object.fromEntries(Object.entries(parts).map(([name, xml]) => [name, strToU8(xml)])))
}
