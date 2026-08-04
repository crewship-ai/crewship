// Inline editor diagnostics for a routine DSL buffer.
//
// Deliberately NOT a generic JSON Schema validator. Ajv against
// routine.v1.json answers a mistyped step kind with "must match exactly
// one schema in oneOf", anchored to the whole document — technically
// correct and useless to the person who typed `htpp`. What follows is
// the short list of mistakes people actually make, each phrased as the
// fix and pinned to the line that caused it.
//
// Positions come from the YAML document AST. YAML 1.2 is a superset of
// JSON, so one parser serves both formats and both get real line
// numbers — no scraping of engine error strings, which differ by
// runtime version.

import { parseDocument, isMap, isSeq, isScalar, type Node } from "yaml"

import type { DslFormat } from "./routine-dsl-format"
import { stepKinds } from "./routine-dsl-schema"

export interface DslDiagnostic {
  /** 1-indexed, matching the editor gutter. */
  line: number
  message: string
  severity: "error" | "warning"
}

const KNOWN_KINDS = new Set(stepKinds().map((k) => k.kind))

/**
 * Problems in `text`, newest parse each call.
 *
 * A syntax error short-circuits everything else: semantic checks need a
 * parsed document, and running them against a broken one produces a
 * cascade of follow-on complaints that bury the single error worth
 * reading.
 */
export function diagnose(text: string, _format: DslFormat): DslDiagnostic[] {
  let doc: ReturnType<typeof parseDocument>
  try {
    doc = parseDocument(text, { prettyErrors: true })
  } catch (e) {
    return [{ line: 1, message: messageOf(e), severity: "error" }]
  }

  if (doc.errors.length > 0) {
    const first = doc.errors[0]
    return [
      {
        line: first.linePos?.[0]?.line ?? 1,
        message: first.message.split("\n")[0],
        severity: "error",
      },
    ]
  }

  const out: DslDiagnostic[] = []
  const lineAt = lineResolver(text)

  const contents = doc.contents
  if (!isMap(contents)) {
    return [{ line: 1, message: "Definice musí být objekt s poli `name` a `steps`.", severity: "error" }]
  }

  const steps = contents.get("steps", true)
  if (!isSeq(steps)) {
    return [
      {
        line: lineAt(nodeOffset(contents.get("steps", true) as Node | undefined)) ?? 1,
        message: "Chybí pole `steps` — recept bez kroků nemá co spustit.",
        severity: "error",
      },
    ]
  }

  // Pass 1: collect declared ids so `needs` can be checked against the
  // whole routine. Step order is not the DAG — `needs` may name a step
  // declared further down — so this cannot be done in one pass.
  const declared = new Set<string>()
  for (const item of steps.items) {
    if (!isMap(item)) continue
    const id = item.get("id")
    if (typeof id === "string" && id) declared.add(id)
  }

  const seen = new Set<string>()
  for (const item of steps.items) {
    if (!isMap(item)) {
      out.push({
        line: lineAt(nodeOffset(item as Node)) ?? 1,
        message: "Krok musí být objekt.",
        severity: "error",
      })
      continue
    }

    const idNode = item.get("id", true)
    const id = isScalar(idNode) ? idNode.value : undefined
    const itemLine = lineAt(nodeOffset(item as Node)) ?? 1

    if (typeof id !== "string" || id === "") {
      out.push({ line: itemLine, message: "Krok nemá `id`.", severity: "error" })
    } else if (seen.has(id)) {
      out.push({
        line: lineAt(nodeOffset(idNode as Node)) ?? itemLine,
        message: `Krok s id \`${id}\` už existuje — id musí být v receptu jedinečné.`,
        severity: "error",
      })
    } else {
      seen.add(id)
    }

    const typeNode = item.get("type", true)
    const type = isScalar(typeNode) ? typeNode.value : undefined
    if (typeof type !== "string" || type === "") {
      out.push({ line: itemLine, message: "Krok nemá `type`.", severity: "error" })
    } else if (!KNOWN_KINDS.has(type)) {
      out.push({
        line: lineAt(nodeOffset(typeNode as Node)) ?? itemLine,
        message: `Neznámý typ kroku \`${type}\`. Povolené: ${[...KNOWN_KINDS].join(", ")}.`,
        severity: "error",
      })
    }

    const needsNode = item.get("needs", true)
    if (isSeq(needsNode)) {
      for (const need of needsNode.items) {
        const value = isScalar(need) ? need.value : undefined
        if (typeof value !== "string") continue
        if (declared.has(value)) continue
        out.push({
          line: lineAt(nodeOffset(need as Node)) ?? itemLine,
          message: `\`needs\` odkazuje na krok \`${value}\`, který v receptu není.`,
          severity: "error",
        })
      }
    }
  }

  return out
}

/** Byte offset where a node starts, if the parser recorded one. */
function nodeOffset(node: Node | undefined | null): number | undefined {
  const range = (node as { range?: [number, number, number] } | undefined)?.range
  return range?.[0]
}

/**
 * Offset → 1-indexed line, via a prefix table.
 *
 * Built once per diagnose() call rather than counting newlines per
 * lookup: a long routine with many findings would otherwise re-scan the
 * whole buffer for each one.
 */
function lineResolver(text: string): (offset?: number) => number | undefined {
  const starts: number[] = [0]
  for (let i = 0; i < text.length; i++) {
    if (text[i] === "\n") starts.push(i + 1)
  }
  return (offset?: number) => {
    if (offset === undefined || !Number.isFinite(offset)) return undefined
    let lo = 0
    let hi = starts.length - 1
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2)
      if (starts[mid] <= offset) lo = mid
      else hi = mid - 1
    }
    return lo + 1
  }
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message.split("\n")[0] : "nečitelný dokument"
}
