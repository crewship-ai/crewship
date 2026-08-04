// YAML ↔ JSON for the routine DSL, browser side.
//
// The split mirrors what the CLI already does: humans author YAML,
// the server stores canonical JSON. internal/pipeline/parse_yaml.go
// (`ToCanonicalJSON`, #1423) is the same conversion in Go, reached by
// `crewship routine validate routine.yaml` and by `--definition`.
//
// The point is not fewer braces. It is `prompt: |` — the production
// accounting routine carries 600-character prompts as a single JSON
// line of \n escapes, which is unreadable and unreviewable. YAML block
// scalars turn those into actual text.
//
// `yaml` (eemeli) rather than js-yaml on purpose: it implements YAML
// 1.2, so the Norway problem is gone — a step id of `no` stays the
// string "no" instead of silently becoming the boolean false.

import { parse as parseYaml, stringify as stringifyYaml } from "yaml"

export type DslFormat = "json" | "yaml"

export type ParseResult =
  | { ok: true; value: Record<string, unknown> }
  | { ok: false; message: string; line?: number }

export type ConvertResult = { ok: true; text: string } | { ok: false; message: string; line?: number }

/**
 * Render a value as YAML.
 *
 * `lineWidth: 0` disables folding: a wrapped prompt is a prompt whose
 * line breaks stop meaning what the author intended, and a diff of it
 * is unreadable. `blockQuote: "literal"` keeps multi-line strings as
 * `|` blocks rather than quoted escapes.
 */
export function toYaml(value: unknown): string {
  return stringifyYaml(value, {
    indent: 2,
    lineWidth: 0,
    blockQuote: "literal",
    // A routine is a document, not a graph — an anchor/alias pair would
    // be technically smaller and much harder to read.
    aliasDuplicateObjects: false,
  })
}

/** Parse a DSL document. A routine is always a mapping at the top. */
export function parseDsl(text: string, format: DslFormat): ParseResult {
  if (format === "yaml") return parseYamlDoc(text)
  return parseJsonDoc(text)
}

function parseYamlDoc(text: string): ParseResult {
  let value: unknown
  try {
    value = parseYaml(text, { prettyErrors: true })
  } catch (e) {
    // Duck-typed on `linePos` rather than `instanceof YAMLParseError`.
    // instanceof compares constructor identity, which breaks the moment
    // the library is resolved through two module instances (ESM vs CJS,
    // or a bundler splitting it) — and it fails by silently dropping the
    // line number, so the marker lands nowhere and nobody notices.
    const err = e as { message?: string; linePos?: { line: number }[] }
    const line = err?.linePos?.[0]?.line
    const message = err?.message ? err.message.split("\n")[0] : "neplatný YAML"
    return { ok: false, message, line }
  }
  return asMapping(value, "YAML")
}

function parseJsonDoc(text: string): ParseResult {
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch (e) {
    const message = e instanceof Error ? e.message : "neplatný JSON"
    return { ok: false, message, line: locateSyntaxError(text) }
  }
  return asMapping(value, "JSON")
}

/**
 * Line of a JSON syntax error, found by re-parsing as YAML.
 *
 * V8's own message is not a reliable source: depending on the engine
 * version it says "at position 42", "line 3 column 2", or — as of
 * Node 26 for some inputs — gives a context snippet and no position at
 * all. Scraping it means the marker silently lands nowhere on whichever
 * runtime phrases it differently.
 *
 * YAML 1.2 is a superset of JSON, so the YAML parser reads the same
 * document and reports a real line/column. JSON.parse stays the
 * authority on VALIDITY; this is only used to locate. Returns undefined
 * when YAML disagrees and accepts the text, rather than guessing.
 */
function locateSyntaxError(text: string): number | undefined {
  try {
    parseYaml(text, { prettyErrors: true })
    return undefined
  } catch (e) {
    return (e as { linePos?: { line: number }[] })?.linePos?.[0]?.line
  }
}

function asMapping(value: unknown, label: string): ParseResult {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return { ok: false, message: `${label}: definice musí být objekt` }
  }
  return { ok: true, value: value as Record<string, unknown> }
}

/** Convert between the two formats, or pass the text through unchanged. */
export function convertDsl(text: string, from: DslFormat, to: DslFormat): ConvertResult {
  if (from === to) return { ok: true, text }
  const parsed = parseDsl(text, from)
  if (!parsed.ok) return parsed
  return {
    ok: true,
    text: to === "yaml" ? toYaml(parsed.value) : JSON.stringify(parsed.value, null, 2),
  }
}
