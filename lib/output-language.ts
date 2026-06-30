// Pure, dependency-free output language detection for the activity
// output surfaces (trace side-panel Output/Logs tabs, sub-span detail).
//
// Goal: classify an arbitrary string output into *how* it should be
// rendered — markdown (fenced code blocks → shiki), raw JSON, a single
// raw code block (yaml/bash/…), or plain text. This mirrors the chat
// renderer so a routine step's output reads the same as the agent's
// chat reply.
//
// Contract: every exported function is pure and MUST NOT throw for any
// string input — unknown / undetectable content falls back to plain
// text. Kept free of shiki/React imports so it stays unit-testable.

/** Languages we can syntax-highlight via the shiki `CodeBlock`. Modelled
 *  as a string union (not shiki's `BundledLanguage`) to keep this module
 *  dependency-free; every member is a valid shiki bundled language. */
export type DetectedLanguage = "json" | "yaml" | "bash" | "markdown"

/** How an output value should be rendered. */
export type OutputKind =
  | "markdown" // has fenced code blocks → chat markdown renderer
  | "json" //     whole value is a JSON object/array → JSONViewer
  | "code" //     raw single-language block → CodeBlock(language)
  | "text" //     plain text / logs → monospace, preserved whitespace

export interface OutputAnalysis {
  kind: OutputKind
  /** Set only when `kind === "code"` — the shiki language to highlight. */
  language?: DetectedLanguage
}

// Fence info-string aliases → canonical highlightable language.
const LANGUAGE_ALIASES: Record<string, DetectedLanguage> = {
  json: "json",
  json5: "json",
  jsonc: "json",
  yaml: "yaml",
  yml: "yaml",
  bash: "bash",
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  console: "bash",
  markdown: "markdown",
  md: "markdown",
}

// A fenced block opener: three backticks at the start of a line
// (optionally indented). `m` flag so it matches on any line.
const FENCE_RE = /^[ \t]*```/m

/** Whether the text contains a fenced code block opener. Inline single
 *  backticks (`code`) do not count. */
export function hasCodeFence(text: string): boolean {
  return FENCE_RE.test(text)
}

/** Normalize a fence info string (the bit after ```) to a canonical
 *  highlightable language, or `null` when unrecognised. Reads only the
 *  first whitespace-delimited token (ignores `title=...` etc). */
export function normalizeFenceLanguage(info: string): DetectedLanguage | null {
  const token = info.trim().toLowerCase().split(/\s+/)[0] ?? ""
  return LANGUAGE_ALIASES[token] ?? null
}

/** True when the whole (trimmed) value parses as a JSON object or array.
 *  Bare scalars (`42`, `"x"`) are intentionally rejected — they read
 *  better as plain text and Table mode is meaningless for them. */
export function isJson(text: string): boolean {
  const trimmed = text.trim()
  if (!trimmed || !/^[[{]/.test(trimmed)) return false
  try {
    const value = JSON.parse(trimmed)
    return value !== null && typeof value === "object"
  } catch {
    return false
  }
}

// Ansible / CI log markers. These often contain `key: value`-looking
// lines (e.g. `ok: [host]`) that would otherwise be misread as YAML, so
// they're checked *before* the YAML heuristic and rendered as plain log.
const LOG_RE = /(^|\n)\s*(PLAY RECAP|TASK \[|PLAY \[|ok:|changed:|fatal:|skipping:)/

// Shell: a shebang naming a shell, or a leading `$ ` command prompt.
const SHEBANG_RE = /^#!.*\b(sh|bash|zsh)\b/m
const PROMPT_RE = /(^|\n)\s*\$ /

// YAML document marker on its own line.
const YAML_DOC_RE = /^---\s*$/m

function looksLikeYaml(text: string): boolean {
  const lines = text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
  if (lines.length === 0) return false
  const yamlish = lines.filter(
    (l) =>
      /^[\w.-]+:(\s|$)/.test(l) || // key: value  /  key:
      /^-\s/.test(l) || //           - list item
      /^-\s*[\w.-]+:/.test(l), //    - key: value
  ).length
  return yamlish / lines.length >= 0.6
}

/** Heuristic language detection for raw (non-fenced) text. Returns
 *  `"text"` when nothing matches. Order matters: JSON → log → bash →
 *  YAML, so ansible recaps aren't swallowed by the YAML heuristic. */
export function detectRawLanguage(text: string): DetectedLanguage | "text" {
  if (!text.trim()) return "text"
  if (isJson(text)) return "json"
  if (LOG_RE.test(text)) return "text"
  if (SHEBANG_RE.test(text) || PROMPT_RE.test(text)) return "bash"
  if (YAML_DOC_RE.test(text) || looksLikeYaml(text)) return "yaml"
  return "text"
}

/** Top-level decision: classify how an output string should render.
 *  Never throws. */
export function analyzeOutput(value: string): OutputAnalysis {
  if (typeof value !== "string" || value.trim() === "") {
    return { kind: "text" }
  }
  // 1. Fenced markdown — the common agent-reply shape. Hand the whole
  //    thing to the chat markdown renderer so each ```lang block is
  //    shiki-highlighted while prose stays prose.
  if (hasCodeFence(value)) return { kind: "markdown" }
  // 2. Whole value is a JSON object/array.
  if (isJson(value)) return { kind: "json" }
  // 3. Raw single-language block, or plain log/text.
  const lang = detectRawLanguage(value)
  if (lang === "json") return { kind: "json" }
  if (lang === "text") return { kind: "text" }
  return { kind: "code", language: lang }
}
