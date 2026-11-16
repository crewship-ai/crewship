import { describe, expect, it } from "vitest"
import { inspectWorkbookXml, xmlBytes } from "../xlsx-xml"

describe("workbook XML character references", () => {
  it("decodes supplementary characters in text and attributes", () => {
    const texts: string[] = [], attributes: string[] = []
    inspectWorkbookXml(xmlBytes('<root label="&#x1F600;">&#128512;</root>'), { nodes: 0 }, {
      open: (_, attrs) => attributes.push(attrs.label),
      text: value => texts.push(value),
    })
    expect(attributes).toEqual(["😀"])
    expect(texts.join("")).toBe("😀")
  })

  it.each(["&#xD800;", "&#x110000;", "&#0;"])("refuses invalid XML character %s", reference => {
    for (const xml of [`<root>${reference}</root>`, `<root label="${reference}"/>`]) {
      expect(() => inspectWorkbookXml(xmlBytes(xml), { nodes: 0 })).toThrow(/Workbook XML/)
    }
  })
})
