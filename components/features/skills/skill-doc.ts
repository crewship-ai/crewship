// SKILL.md as the Skills page reads and writes it (#3033). Pure: the
// Overview's structured reading, the SKILL.md tab's outline, the frontmatter
// the editor sends to the import endpoint and the Usage bars all come from
// here, so the page and the tests share one definition.

// ── reading the body ──────────────────────────────────────────────────────

export interface SkillDocSection {
  /** Anchor id for the outline, unique within the document. */
  id: string
  heading: string
  level: number
  /** The section's own lines, without the heading. */
  body: string
}

export type SkillDocRole = "when" | "steps" | "output" | "guardrails"

/** What the Overview shows: the SKILL.md sections that answer its four questions. */
export interface SkillDoc {
  sections: SkillDocSection[]
  when: string[]
  triggers: string[]
  steps: string[]
  output: string[]
  guardrails: string[]
  /** Headings of the sections the four roles above did not take. */
  other: SkillDocSection[]
}

// Heading words authors use for each role. Checked in this order, so
// "Output rules" is output and "Usage rules" is guardrails, not steps.
const ROLE_PATTERNS: [SkillDocRole, RegExp][] = [
  ["when", /\b(when to (use|activate|apply)|activation|triggers?|use (it |this )?when|when)\b/i],
  ["output", /\b(output|returns?|deliverables?|result|response format|format)\b/i],
  ["guardrails", /\b(guardrails?|rules|constraints|limits|safety|never|don'?ts?|caveats|boundaries)\b/i],
  ["steps", /\b(instructions?|steps|how to|how it works|workflow|process|procedure|usage|method|approach)\b/i],
]

export function sectionRole(heading: string): SkillDocRole | null {
  for (const [role, re] of ROLE_PATTERNS) if (re.test(heading)) return role
  return null
}

function slugifyHeading(h: string): string {
  return (
    h
      .toLowerCase()
      .replace(/[`*_~]/g, "")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "") || "section"
  )
}

/** Split a markdown body at its headings, ignoring `#` lines inside code fences. */
export function splitSections(body: string): SkillDocSection[] {
  const out: SkillDocSection[] = []
  const seen = new Map<string, number>()
  let fence = false
  let current: SkillDocSection | null = null
  const lines: string[] = []
  const flush = () => {
    if (current) out.push({ ...current, body: lines.join("\n").trim() })
    lines.length = 0
  }
  for (const line of body.split("\n")) {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence
    const m = !fence && /^(#{1,4})\s+(.+?)\s*#*\s*$/.exec(line)
    if (m) {
      flush()
      const base = slugifyHeading(m[2])
      const n = seen.get(base) ?? 0
      seen.set(base, n + 1)
      current = { id: n ? `${base}-${n + 1}` : base, heading: m[2].trim(), level: m[1].length, body: "" }
    } else if (current) {
      lines.push(line)
    }
  }
  flush()
  return out
}

/** The list items of a section: bullets and numbered lines, one level deep. */
export function listItems(body: string): string[] {
  const items: string[] = []
  for (const line of body.split("\n")) {
    const m = /^\s{0,3}(?:[-*+]|\d+[.)])\s+(.*\S)\s*$/.exec(line)
    if (m) items.push(m[1])
    else if (items.length && /^\s{2,}\S/.test(line) && !/^\s*(?:[-*+]|\d+[.)])\s/.test(line)) {
      items[items.length - 1] += " " + line.trim()
    }
  }
  return items
}

/** `Keywords: "create", "file"` → ["create", "file"]. */
function keywordList(item: string): string[] | null {
  const m = /^\**(keywords?|triggers?|trigger words)\**\s*:\s*(.+)$/i.exec(item)
  if (!m) return null
  return m[2]
    .split(/[,;]/)
    .map((k) => k.trim().replace(/^["'`“”]+|["'`“”.]+$/g, "").trim())
    .filter(Boolean)
}

export function readSkillDoc(content: string | null | undefined): SkillDoc {
  const sections = splitSections(content ?? "")
  const doc: SkillDoc = { sections, when: [], triggers: [], steps: [], output: [], guardrails: [], other: [] }
  for (const s of sections) {
    // The document title (`# File Crafter`) is not a section of its own.
    if (s.level === 1 && !s.body) continue
    const role = sectionRole(s.heading)
    const items = listItems(s.body)
    if (!role || items.length === 0) {
      if (s.level > 1 || s.body) doc.other.push(s)
      continue
    }
    if (role === "when") {
      for (const it of items) {
        const kw = keywordList(it)
        if (kw) doc.triggers.push(...kw)
        else doc.when.push(it)
      }
    } else {
      doc[role].push(...items)
    }
  }
  return doc
}

/** True when the Overview has something to show beyond the description. */
export function hasStructure(doc: SkillDoc): boolean {
  return doc.when.length + doc.triggers.length + doc.steps.length + doc.output.length + doc.guardrails.length > 0
}

// ── writing SKILL.md ──────────────────────────────────────────────────────

/** The frontmatter keys the importer reads (internal/skills/parser.go SkillMeta). */
export interface SkillMeta {
  name: string
  display_name?: string | null
  description?: string | null
  version?: string | null
  author?: string | null
  license?: string | null
  category?: string | null
  icon?: string | null
  credential_requirements?: string[]
  tags?: string[]
}

/** The importer's slug rule (parser.Slugify): lowercase, a-z0-9 and single dashes. */
export function slugifySkill(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
}

// A JSON string is a valid YAML double-quoted scalar, so every free-text value
// goes through JSON.stringify — a description with a colon or a quote cannot
// break the frontmatter. Bare words (slug, version, category, icon) stay bare.
const BARE = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/
function scalar(v: string): string {
  return BARE.test(v) ? v : JSON.stringify(v)
}

export function frontmatter(meta: SkillMeta): string {
  const lines = ["---", `name: ${scalar(meta.name)}`]
  const put = (k: keyof SkillMeta) => {
    const v = meta[k]
    if (typeof v === "string" && v.trim()) lines.push(`${k}: ${scalar(v.trim())}`)
  }
  put("display_name")
  put("description")
  put("version")
  put("author")
  put("license")
  put("category")
  put("icon")
  for (const k of ["credential_requirements", "tags"] as const) {
    const list = (meta[k] ?? []).map((x) => x.trim()).filter(Boolean)
    if (list.length) lines.push(`${k}:`, ...list.map((x) => `  - ${scalar(x)}`))
  }
  lines.push("---")
  return lines.join("\n")
}

export function skillMarkdown(meta: SkillMeta, body: string): string {
  return `${frontmatter(meta)}\n\n${body.trim()}\n`
}

/**
 * Set top-level frontmatter keys of a SKILL.md someone else wrote — the
 * generator's output — leaving every other line as it came. A key already
 * present is replaced (with its indented continuation lines); a missing one
 * is added before the closing `---`.
 */
export function patchFrontmatter(raw: string, patch: Record<string, string>): string {
  const lines = raw.replace(/^﻿/, "").split("\n")
  const open = lines.findIndex((l) => l.trim() === "---")
  const close = open < 0 ? -1 : lines.findIndex((l, i) => i > open && l.trim() === "---")
  if (open < 0 || close < 0) {
    const meta: SkillMeta = { name: patch.name ?? "skill" }
    for (const [k, v] of Object.entries(patch)) (meta as unknown as Record<string, string>)[k] = v
    return skillMarkdown(meta, raw)
  }
  const head = lines.slice(open + 1, close)
  const out: string[] = []
  const done = new Set<string>()
  for (let i = 0; i < head.length; i++) {
    const m = /^([A-Za-z_][\w-]*)\s*:/.exec(head[i])
    if (m && m[1] in patch) {
      out.push(`${m[1]}: ${scalar(patch[m[1]])}`)
      done.add(m[1])
      while (i + 1 < head.length && /^\s+\S|^\s*-\s/.test(head[i + 1])) i++
    } else {
      out.push(head[i])
    }
  }
  for (const [k, v] of Object.entries(patch)) if (!done.has(k)) out.push(`${k}: ${scalar(v)}`)
  return [...lines.slice(0, open + 1), ...out, ...lines.slice(close)].join("\n")
}

/** A starting body for "Write it": the sections the Overview reads. */
export const SKILL_BODY_TEMPLATE = `## When to Activate
-

## Instructions
1.

## Output Format
-

## Guardrails
- `

// ── usage by day ──────────────────────────────────────────────────────────

export interface UsageDay {
  /** Local date, YYYY-MM-DD. */
  date: string
  /** Two-letter weekday. */
  label: string
  uses: number
  errors: number
}

function localDate(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** Seven local days ending today, counted from skill.invoked journal entries. */
export function usageByDay(
  entries: { ts: string; entry_type: string; payload?: unknown }[],
  now: Date = new Date(),
): UsageDay[] {
  const days: UsageDay[] = []
  for (let i = 6; i >= 0; i--) {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i)
    days.push({ date: localDate(d), label: d.toLocaleDateString("en", { weekday: "short" }).slice(0, 2), uses: 0, errors: 0 })
  }
  const byDate = new Map(days.map((d) => [d.date, d]))
  for (const e of entries) {
    if (e.entry_type !== "skill.invoked") continue
    const day = byDate.get(localDate(new Date(e.ts)))
    if (!day) continue
    day.uses++
    const p = (e.payload ?? {}) as Record<string, unknown>
    if (Number(p.exit_code ?? 0) !== 0) day.errors++
  }
  return days
}
