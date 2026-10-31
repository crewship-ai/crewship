// What a reader of a saved native document would see, for the long work
// session rows. Word: JSON of the body; Sheet: the workbook's cells.
import { createHash } from "node:crypto"

type Node = Record<string, unknown>

/** Every object in the native JSON, depth first. */
export function* walk(value: unknown): Generator<Node> {
  if (Array.isArray(value)) for (const v of value) yield* walk(v)
  else if (value && typeof value === "object") {
    yield value as Node
    for (const v of Object.values(value)) yield* walk(v)
  }
}

export const sha256 = (text: string): string => "sha256:" + createHash("sha256").update(text).digest("hex")

export interface WordFacts {
  heading: boolean
  bold: boolean
  table: boolean
  image: boolean
  font: boolean
  paragraphs: number
}

export interface WordMarkers {
  heading: string
  bold: string
  cell: string
  font: string
}

/** Structure the session built, found in the saved Word JSON. */
export function wordFacts(raw: string, m: WordMarkers): WordFacts {
  const doc = JSON.parse(raw) as { body?: Node[] }
  const nodes = [...walk(doc.body ?? [])]
  const paras = nodes.filter((n) => n.kind === "para")
  const runsOf = (p: Node) => ((p.runs as Node[] | undefined) ?? []).map((r) => (r.props as Node | undefined) ?? {})
  return {
    heading: paras.some((p) => String(p.text ?? "").includes(m.heading) && /^Heading/.test(String((p.props as Node | undefined)?.style ?? ""))),
    bold: paras.some((p) => String(p.text ?? "").includes(m.bold) && runsOf(p).some((r) => r.bold === true)),
    table: nodes.some((n) => n.kind === "table") && nodes.some((n) => n.kind === "para" && String(n.text ?? "").includes(m.cell)),
    image: nodes.some((n) => /^(image|picture|drawing|inlineImage)$/i.test(String(n.kind ?? n.type ?? ""))) || /"(image|blip|drawing)[A-Za-z]*"/i.test(JSON.stringify(doc.body ?? [])),
    font: paras.some((p) => String(p.text ?? "").includes(m.font) && runsOf(p).some((r) => typeof r.size === "number" && r.size > 12)),
    paragraphs: paras.length,
  }
}

/** Number of marker occurrences in the document text, for "nothing lost" counts. */
export function present(raw: string, markers: string[]): { found: number; missing: string[] } {
  const missing = markers.filter((m) => !raw.includes(m))
  return { found: markers.length - missing.length, missing }
}
