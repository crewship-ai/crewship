// Native documents above 4 MiB for the soak scenario, in the engines' own JSON
// models. The server stores native state as opaque bytes, so the soak only
// needs documents that the engines would also read as ordinary content.
import { createHash } from "node:crypto"
import { emptyDocument, type NativeApplication } from "./../craft-lease/native"

export const MIN_NATIVE_BYTES = 4 * 1024 * 1024 + 1024
export const SOAK_SAVES = 2000

type Json = Record<string, unknown>

function paragraph(text: string): Json {
  return { kind: "para", text, runs: [{ len: [...text].length }], props: { style: "Normal" } }
}

/** A Word document of many paragraphs whose native state exceeds 4 MiB; `marker` becomes a last paragraph. */
export function bigWord(marker: string | null): Buffer {
  const doc = JSON.parse(emptyDocument("wordcraft").toString("utf8")) as { body: Json[] }
  const filler = "Soak paragraph ".repeat(70).slice(0, 1000)
  const body: Json[] = []
  let size = 0
  for (let i = 0; size < MIN_NATIVE_BYTES; i++) {
    const p = paragraph(`${String(i).padStart(6, "0")} ${filler}`)
    body.push(p)
    size += JSON.stringify(p).length + 1
  }
  if (marker) body.push(paragraph(marker))
  doc.body = body
  return Buffer.from(JSON.stringify(doc))
}

/** A Sheet workbook with enough numeric cells to exceed 4 MiB; `marker` is a last text cell. */
export function bigSheet(marker: string | null): Buffer {
  const wb = JSON.parse(emptyDocument("gridcraft").toString("utf8")) as { sheets: Array<{ cells: unknown }> }
  const cell = (v: number) => ({ value: { t: "Number", v }, formula: null, style: 0 })
  // The engine keys bands by row / 64 and rows by their absolute index.
  const bands: Record<string, { rows: Record<string, Record<string, unknown>> }> = {}
  const put = (r: number, cols: Record<string, unknown>) => {
    const band = String(Math.floor(r / 64))
    ;(bands[band] ??= { rows: {} }).rows[String(r)] = cols
  }
  let size = 0
  let count = 0
  let next = 0
  for (let r = 0; size < MIN_NATIVE_BYTES; r++) {
    const cols: Record<string, unknown> = {}
    for (let c = 0; c < 10; c++) {
      cols[String(c)] = cell(r * 10 + c + 0.5)
      count++
    }
    put(r, cols)
    next = r + 1
    size += JSON.stringify(cols).length + 8
  }
  if (marker) {
    put(next, { "0": { value: { t: "Text", v: marker }, formula: null, style: 0 } })
    count++
  }
  wb.sheets[0].cells = { bands, len: count }
  return Buffer.from(JSON.stringify(wb))
}

export function bigDocument(application: NativeApplication, marker: string | null): Buffer {
  return application === "gridcraft" ? bigSheet(marker) : bigWord(marker)
}

export const digest = (b: Buffer) => createHash("sha256").update(b).digest("hex")
