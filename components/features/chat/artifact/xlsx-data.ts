import readExcelFile from "read-excel-file/universal"
import { zipSync } from "fflate"
import { unpackBoundedWorkbook, workbookRefusal, XLSX_LIMITS } from "./xlsx-archive"
import { inspectWorkbookXml, xmlBytes, xmlEscape, type XmlBudget } from "./xlsx-xml"

export interface WorkbookSheet { name: string; table: { rows: string[][]; columns: number } }
export interface WorkbookData { sheets: WorkbookSheet[]; formulas: number; missingFormulaResults: number }
const MISSING_FORMULA_RESULT = "[Formula not evaluated: no cached result]"
const NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
const PACKAGE_REL = "http://schemas.openxmlformats.org/package/2006/relationships"

function required(files: Map<string, Uint8Array>, path: string): Uint8Array {
  const bytes = files.get(path)
  if (!bytes) throw workbookRefusal("Workbook is missing a required part.")
  return bytes
}
function boundedInteger(value: string, maximum: number): number {
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) > maximum) throw workbookRefusal("Workbook coordinate or style exceeds the preview limits.")
  return Number(value)
}
function relationPath(target: string): string {
  const path = target.startsWith("/") ? target.slice(1) : `xl/${target}`
  if (/[\\\0]/.test(path) || path.split("/").some(part => !part || part === "." || part === "..") || /^[\w+.-]+:/.test(path)) throw workbookRefusal("Workbook contains an unsafe relationship path.")
  return path
}
function coordinate(address: string): { row: number; column: number } {
  const match = /^([A-Z]{1,3})([1-9]\d{0,6})$/.exec(address)
  if (!match) throw workbookRefusal("Workbook cell coordinates are unsupported.")
  const row = boundedInteger(match[2], XLSX_LIMITS.rows)
  let column = 0
  for (const letter of match[1]) column = column * 26 + letter.charCodeAt(0) - 64
  if (column > XLSX_LIMITS.columns) throw workbookRefusal("Workbook exceeds the 100-column preview limit.")
  return { row, column }
}
function columnName(column: number): string {
  let result = ""
  for (let n = column; n > 0; n = Math.floor((n - 1) / 26)) result = String.fromCharCode(65 + (n - 1) % 26) + result
  return result
}
function boundedText(value: string): string {
  if (value.length > XLSX_LIMITS.cellCharacters) throw workbookRefusal("Workbook cell exceeds the 32,768-character preview limit.")
  return value
}

function sharedStrings(bytes: Uint8Array | undefined, budget: XmlBudget): string[] {
  if (!bytes) return []
  const strings: string[] = []
  let value: string | null = null
  inspectWorkbookXml(bytes, budget, {
    open(name) { if (name === "si") { if (value !== null || strings.length >= XLSX_LIMITS.cells) throw workbookRefusal("Workbook exceeds the shared-string limit."); value = "" } },
    text(text, stack) { if (value !== null && stack.at(-1) === "t" && !stack.includes("rPh")) value = boundedText(value + text) },
    close(name) { if (name === "si" && value !== null) { strings.push(value); value = null } },
  })
  return strings
}

function normalizeStyles(bytes: Uint8Array | undefined, budget: XmlBudget): Uint8Array | undefined {
  if (!bytes) return undefined
  const formats: string[] = [], base: string[] = [], styles: string[] = []
  inspectWorkbookXml(bytes, budget, {
    open(name, attrs, stack) {
      if (name === "numFmt") {
        if (formats.length >= 1024) throw workbookRefusal("Workbook exceeds the number-format limit.")
        const id = boundedInteger(attrs.numFmtId ?? "", 65_535)
        formats.push(`<numFmt numFmtId="${id}" formatCode="${xmlEscape(attrs.formatCode ?? "")}"/>`)
      }
      if (name === "xf" && (stack.at(-2) === "cellXfs" || stack.at(-2) === "cellStyleXfs")) {
        const group = stack.at(-2) === "cellXfs" ? styles : base
        if (group.length >= 10_000) throw workbookRefusal("Workbook exceeds the style limit.")
        const format = attrs.numFmtId === undefined ? "" : ` numFmtId="${boundedInteger(attrs.numFmtId, 65_535)}"`
        const parent = attrs.xfId === undefined ? "" : ` xfId="${boundedInteger(attrs.xfId, 9999)}"`
        group.push(`<xf${format}${parent}/>`)
      }
    },
  })
  return xmlBytes(`<styleSheet xmlns="${NS}"><numFmts>${formats.join("")}</numFmts><cellStyleXfs>${base.join("")}</cellStyleXfs><cellXfs>${styles.join("")}</cellXfs></styleSheet>`)
}

interface Cell { address: string; row: number; column: number; type: string; style?: string; value: string; inline: string; hasValue: boolean; hasInline: boolean; formula: boolean; formulaCharacters: number }
interface WorkbookBudget { cells: number; projectedBytes: number; formulas: number; missing: number; rectangleCells: number }
function normalizeSheet(bytes: Uint8Array, strings: string[], xmlBudget: XmlBudget, budget: WorkbookBudget): { bytes: Uint8Array; rows: number; columns: number } {
  const rows: string[] = [], cells: string[] = []
  let cell: Cell | null = null, row = 0, previousRow = 0, previousColumn = 0, maxRow = 0, maxColumn = 0
  let explicitRow = false, hasSheetData = false
  const flushRow = () => { rows.push(`<row r="${row}">${cells.join("")}</row>`); cells.length = 0; previousRow = row }
  inspectWorkbookXml(bytes, xmlBudget, {
    open(name, attrs, stack) {
      if (name === "sheetData") {
        if (hasSheetData || stack.length !== 2 || stack[0] !== "worksheet") throw workbookRefusal("Workbook worksheet data is unsupported.")
        hasSheetData = true
      }
      if (name === "row") {
        if (stack.length !== 3 || stack.at(-2) !== "sheetData") throw workbookRefusal("Workbook row structure is unsupported.")
        row = attrs.r === undefined ? previousRow + 1 : boundedInteger(attrs.r, XLSX_LIMITS.rows)
        if (!row || row <= previousRow) throw workbookRefusal("Workbook rows are out of order.")
        explicitRow = attrs.r !== undefined; previousColumn = 0
      }
      if (name === "c") {
        if (stack.length !== 4 || stack.at(-2) !== "row" || stack.at(-3) !== "sheetData") throw workbookRefusal("Workbook cell structure is unsupported.")
        if (cell || ++budget.cells > XLSX_LIMITS.cells) throw workbookRefusal("Workbook exceeds the 200,000-cell preview limit.")
        const address = attrs.r ?? `${columnName(previousColumn + 1)}${row}`
        const position = coordinate(address)
        if (position.row !== row) {
          if (explicitRow || previousColumn || position.row <= previousRow) throw workbookRefusal("Workbook cell and row coordinates disagree.")
          row = position.row
        }
        if (position.column <= previousColumn) throw workbookRefusal("Workbook cells are duplicated or out of order.")
        previousColumn = position.column
        maxRow = Math.max(maxRow, row); maxColumn = Math.max(maxColumn, position.column)
        // Check sparse allocation bounds before the library creates any gaps.
        if (maxRow * maxColumn + budget.rectangleCells > XLSX_LIMITS.cells) throw workbookRefusal("Workbook sparse coordinates exceed the 200,000-cell preview limit.")
        if (attrs.s !== undefined) boundedInteger(attrs.s, 9999)
        cell = { address, ...position, type: attrs.t ?? "n", style: attrs.s, value: "", inline: "", hasValue: false, hasInline: false, formula: false, formulaCharacters: 0 }
        if (!["n", "s", "str", "inlineStr", "b", "d", "e"].includes(cell.type)) throw workbookRefusal("Workbook cell type is unsupported.")
      }
      if (cell) {
        if (name === "f") cell.formula = true
        if (name === "v") cell.hasValue = true
        if (name === "is") cell.hasInline = true
      }
    },
    text(text, stack) {
      if (!cell) return
      if (stack.at(-1) === "v") cell.value = boundedText(cell.value + text)
      else if (stack.at(-1) === "t" && stack.includes("is") && !stack.includes("rPh")) cell.inline = boundedText(cell.inline + text)
      else if (stack.at(-1) === "f" && (cell.formulaCharacters += text.length) > XLSX_LIMITS.cellCharacters) throw workbookRefusal("Workbook formula exceeds the text limit.")
    },
    close(name, stack) {
      if (name === "c" && cell) {
        let type = cell.type, value = cell.value
        if (cell.formula) {
          budget.formulas++
          if (!cell.hasValue || type !== "str" && !value.trim()) { type = "inlineStr"; value = MISSING_FORMULA_RESULT; budget.missing++ }
        }
        if (type === "s") {
          const index = boundedInteger(value, XLSX_LIMITS.cells - 1)
          if (strings[index] === undefined) throw workbookRefusal("Workbook references a missing shared string.")
          value = strings[index]; type = "inlineStr"
        } else if (type === "inlineStr" && value !== MISSING_FORMULA_RESULT) {
          if (!cell.hasInline) throw workbookRefusal("Workbook has a missing inline string.")
          value = cell.inline
        } else if (type === "str") type = "inlineStr"
        boundedText(value)
        if (type === "n" && value && (!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[Ee][+-]?\d+)?$/.test(value) || !Number.isFinite(Number(value)))) throw workbookRefusal("Workbook has an invalid numeric value.")
        budget.projectedBytes += xmlBytes(value).byteLength
        if (budget.projectedBytes > XLSX_LIMITS.projectedTextBytes) throw workbookRefusal("Workbook values exceed the 10 MiB text preview limit.")
        const style = cell.style === undefined || type === "inlineStr" ? "" : ` s="${cell.style}"`
        const body = type === "inlineStr" ? `<is><t xml:space="preserve">${xmlEscape(value)}</t></is>` : cell.hasValue ? `<v>${xmlEscape(value)}</v>` : ""
        cells.push(`<c r="${cell.address}" t="${type}"${style}>${body}</c>`)
        cell = null
      } else if (name === "row" && stack.at(-2) === "sheetData") flushRow()
    },
  })
  if (!hasSheetData) throw workbookRefusal("Workbook worksheet has no cell data part.")
  budget.rectangleCells += maxRow * maxColumn
  return { bytes: xmlBytes(`<worksheet xmlns="${NS}"><sheetData>${rows.join("")}</sheetData></worksheet>`), rows: maxRow, columns: maxColumn }
}

/** Never pass the original untrusted ZIP to the third-party reader. */
export async function parseXlsxWorkbook(bytes: Uint8Array): Promise<WorkbookData> {
  const files = unpackBoundedWorkbook(bytes), xmlBudget: XmlBudget = { nodes: 0 }
  const relations = new Map<string, { type: string; path: string }>()
  for (const [path, contents] of files) {
    if (!path.endsWith(".rels")) continue
    inspectWorkbookXml(contents, xmlBudget, { open(name, attrs) {
      if (name !== "Relationship") return
      if (attrs.TargetMode?.toLowerCase() === "external" || /^[\w+.-]+:|^\/\//.test(attrs.Target ?? "")) throw workbookRefusal("External workbook relationships are not supported.")
      if (path === "xl/_rels/workbook.xml.rels") {
        if (!attrs.Id || relations.has(attrs.Id)) throw workbookRefusal("Workbook relationships are duplicated or missing.")
        relations.set(attrs.Id, { type: attrs.Type?.slice(attrs.Type.lastIndexOf("/") + 1) ?? "", path: relationPath(attrs.Target ?? "") })
      }
    } })
  }
  inspectWorkbookXml(required(files, "[Content_Types].xml"), xmlBudget, { open(_, attrs) {
    if (/macroEnabled|vbaProject|macrosheet/i.test(attrs.ContentType ?? "")) throw workbookRefusal("Macro-enabled workbooks are not supported.")
  } })
  const sheets: { name: string; path: string }[] = [], names = new Set<string>()
  let epoch1904 = false
  inspectWorkbookXml(required(files, "xl/workbook.xml"), xmlBudget, { open(name, attrs, stack) {
    if (name === "workbookPr") epoch1904 = attrs.date1904 === "1" || attrs.date1904 === "true"
    if (name !== "sheet" || stack.at(-2) !== "sheets") return
    const target = relations.get(attrs.id)
    if (!attrs.name || attrs.name.length > 128 || names.has(attrs.name.toLowerCase()) || !target || target.type !== "worksheet") throw workbookRefusal("Workbook sheet metadata is unsupported.")
    if (sheets.length >= XLSX_LIMITS.sheets) throw workbookRefusal("Workbook exceeds the 10-sheet preview limit.")
    names.add(attrs.name.toLowerCase()); sheets.push({ name: attrs.name, path: target.path })
  } })
  if (!sheets.length) throw workbookRefusal("Workbook contains no supported worksheets.")
  const shared = [...relations.values()].find(relation => relation.type === "sharedStrings")?.path ?? "xl/sharedStrings.xml"
  const style = [...relations.values()].find(relation => relation.type === "styles")?.path ?? "xl/styles.xml"
  const strings = sharedStrings(files.get(shared), xmlBudget), styles = normalizeStyles(files.get(style), xmlBudget)
  const budget: WorkbookBudget = { cells: 0, projectedBytes: 0, formulas: 0, missing: 0, rectangleCells: 0 }
  const canonical: Record<string, Uint8Array> = Object.create(null)
  canonical["xl/workbook.xml"] = xmlBytes(`<workbook xmlns="${NS}" xmlns:r="${REL}"><workbookPr date1904="${Number(epoch1904)}"/><sheets>${sheets.map((sheet, index) => `<sheet name="${xmlEscape(sheet.name)}" sheetId="${index + 1}" r:id="sheet${index}"/>`).join("")}</sheets></workbook>`)
  canonical["xl/_rels/workbook.xml.rels"] = xmlBytes(`<Relationships xmlns="${PACKAGE_REL}">${sheets.map((_, index) => `<Relationship Id="sheet${index}" Type="${REL}/worksheet" Target="worksheets/sheet${index}.xml"/>`).join("")}${styles ? `<Relationship Id="styles" Type="${REL}/styles" Target="styles.xml"/>` : ""}</Relationships>`)
  if (styles) canonical["xl/styles.xml"] = styles
  const dimensions = sheets.map((sheet, index) => {
    const normalized = normalizeSheet(required(files, sheet.path), strings, xmlBudget, budget)
    canonical[`xl/worksheets/sheet${index}.xml`] = normalized.bytes
    return { rows: normalized.rows, columns: normalized.columns }
  })
  // Stored entries require no further inflate allocation or nested workers.
  if (Object.values(canonical).reduce((total, part) => total + part.byteLength, 0) > XLSX_LIMITS.expandedBytes) throw workbookRefusal("Normalized workbook exceeds the 20 MiB preview limit.")
  const safe = zipSync(canonical, { level: 0 })
  const parsed = await readExcelFile(safe.buffer as ArrayBuffer, { trim: false, parseNumber: value => value })
  let displayedBytes = 0
  return { formulas: budget.formulas, missingFormulaResults: budget.missing, sheets: parsed.map((sheet, index) => {
    const dimensionsForSheet = dimensions[index]
    // The reader trims trailing empty cells; restore the validated original
    // rectangle so cached empty strings and original coordinates survive.
    const rows = Array.from({ length: dimensionsForSheet.rows }, (_, row) => Array.from({ length: dimensionsForSheet.columns }, (_, column) => {
      const value = sheet.data[row]?.[column] ?? null
      const text = value instanceof Date ? value.toISOString() : value === null ? "" : typeof value === "boolean" ? value ? "TRUE" : "FALSE" : String(value)
      displayedBytes += xmlBytes(text).byteLength
      if (displayedBytes > XLSX_LIMITS.projectedTextBytes) throw workbookRefusal("Workbook values exceed the 10 MiB text preview limit.")
      return text
    }))
    return { name: sheet.sheet, table: { rows, columns: dimensionsForSheet.columns } }
  }) }
}
