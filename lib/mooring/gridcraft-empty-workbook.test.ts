import { strFromU8, unzipSync } from "fflate"
import { expect, it } from "vitest"
import { emptyGridCraftWorkbook } from "./gridcraft-empty-workbook"

it("packages the default Sheet styles and declares the styles part", () => {
  const files = unzipSync(emptyGridCraftWorkbook())
  const styles = strFromU8(files["xl/styles.xml"])
  expect(styles).toContain("<fonts count=\"1\">")
  expect(styles).toContain("<fills count=\"2\">")
  expect(styles).toContain('patternType="none"')
  expect(styles).toContain('patternType="gray125"')
  expect(styles).toContain("<borders count=\"1\">")
  expect(styles).toContain("<cellStyleXfs count=\"1\">")
  expect(styles).toContain("<cellXfs count=\"1\">")
  expect(styles).toContain('<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/>')
  expect(strFromU8(files["[Content_Types].xml"])).toContain(
    'PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"',
  )
  expect(strFromU8(files["xl/_rels/workbook.xml.rels"])).toContain(
    'Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"',
  )
})
