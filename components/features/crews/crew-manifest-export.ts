import { stringify } from "yaml"

import { apiFetch } from "@/lib/api-fetch"

// The crewship.yaml of a crew, composed in the browser from the same GETs
// `crewship export crew <slug>` makes. A port of internal/manifest/export.go
// (ExportCrew + MarshalDocument): same endpoints, same field order, same
// omitempty rules, same sorting, so the file is byte for byte what the CLI
// prints. Credential values are never read: only the slot (env, provider,
// type) is written. There is no server route for this (#3451); when one
// ships this module is replaced by a single GET.
//
// Keep in step with export.go and schema.go; the parity test in
// __tests__/crew-manifest-export.test.ts compares against recorded CLI output.

export interface ExportOptions {
  /** Write credential slots (default true; the CLI's --no-credentials is false). */
  credentials?: boolean
  /** Inline the skill bodies (default true; --no-skill-bodies is false). */
  skillBodies?: boolean
}

export interface CrewExport {
  yaml: string
  /** Parts that could not be read; the rest of the file is still right. */
  warnings: string[]
}

type Get = (url: string) => Promise<Response>
type Json = Record<string, unknown>

const SCHEMA_LINE = "# yaml-language-server: $schema=https://schemas.crewship.ai/v1/manifest.json\n"
const API_VERSION = "crewship/v1"
/** serviceconfig.Redacted: a service block the caller may not see. */
const REDACTED = "[redacted]"

// ---- yaml.v3 compatible ordering and emptiness -----------------------------

const isDigit = (c: string) => c >= "0" && c <= "9"
const isLetter = (c: string) => c.toLowerCase() !== c.toUpperCase()

/** yaml.v3's keyList.Less for string keys: natural order, digits by value. */
function keyLess(a: string, b: string): boolean {
  const ar = Array.from(a)
  const br = Array.from(b)
  let digits = false
  for (let i = 0; i < ar.length && i < br.length; i++) {
    if (ar[i] === br[i]) { digits = isDigit(ar[i]); continue }
    const al = isLetter(ar[i])
    const bl = isLetter(br[i])
    if (al && bl) return ar[i].codePointAt(0)! < br[i].codePointAt(0)!
    if (al || bl) return digits ? al : bl
    let an = 0
    let bn = 0
    if (ar[i] === "0" || br[i] === "0") {
      for (let j = i - 1; j >= 0 && isDigit(ar[j]); j--) {
        if (ar[j] !== "0") { an = 1; bn = 1; break }
      }
    }
    let ai = i
    let bi = i
    for (; ai < ar.length && isDigit(ar[ai]); ai++) an = an * 10 + (ar[ai].charCodeAt(0) - 48)
    for (; bi < br.length && isDigit(br[bi]); bi++) bn = bn * 10 + (br[bi].charCodeAt(0) - 48)
    if (an !== bn) return an < bn
    if (ai !== bi) return ai < bi
    return ar[i].codePointAt(0)! < br[i].codePointAt(0)!
  }
  return ar.length < br.length
}

/** A decoded JSON value as yaml.v3 writes a map[string]any: keys sorted. */
function sorted(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sorted)
  if (value && typeof value === "object") {
    const keys = Object.keys(value as Json).sort((x, y) => (keyLess(x, y) ? -1 : keyLess(y, x) ? 1 : 0))
    return new Map(keys.map((k) => [k, sorted((value as Json)[k])]))
  }
  return value
}

const str = (v: unknown): string => (typeof v === "string" ? v : "")
const empty = (v: unknown) => v === "" || v === 0 || v === false || v == null || (Array.isArray(v) && v.length === 0) || (v instanceof Map && v.size === 0)

/** An ordered mapping where `omit` fields are left out when empty (omitempty). */
function ordered(fields: Array<[string, unknown, boolean?]>): Map<string, unknown> {
  const m = new Map<string, unknown>()
  for (const [k, v, omit] of fields) if (!omit || !empty(v)) m.set(k, v)
  return m
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []
}
function stringMap(v: unknown): Map<string, string> {
  const m = new Map<string, string>()
  if (v && typeof v === "object") for (const k of Object.keys(v as Json).sort((x, y) => (keyLess(x, y) ? -1 : keyLess(y, x) ? 1 : 0))) m.set(k, String((v as Json)[k]))
  return m
}

const indentOf = (line: string) => line.length - line.trimStart().length

/**
 * yaml.v3 and the yaml library differ in one place: under a key that sits in a
 * list item ("- slug: x" then "  llm:" or "  inline: |") the library indents
 * the children four columns past the key, yaml.v3 two. Everything below such a
 * key is pulled left by two (block scalar text included; text inside a block
 * scalar is never mistaken for a key).
 */
function alignListItemChildren(text: string): string {
  const lines = text.split("\n")
  const KEY = /^( *)(- )?[^\s#"'\-][^:]*:(?: |$)/
  const shift = new Array<number>(lines.length).fill(0)
  let skipUntil = -1 // lines below this index are block scalar text
  const itemMap = (i: number, keyCol: number, dash: boolean): boolean => {
    if (dash) return true
    for (let j = i - 1; j >= 0; j--) {
      if (lines[j].trim() === "") continue
      const ind = indentOf(lines[j])
      if (ind < keyCol) return ind === keyCol - 2 && lines[j].trimStart().startsWith("- ")
    }
    return false
  }
  for (let i = 0; i < lines.length; i++) {
    if (i <= skipUntil) continue
    const m = KEY.exec(lines[i])
    if (!m) continue
    const keyCol = m[1].length + (m[2] ? 2 : 0)
    const block = /: [|>][-+]?\d*$/.test(lines[i])
    let end = i
    for (let p = i + 1; p < lines.length; p++) {
      if (lines[p] !== "" && indentOf(lines[p]) <= keyCol) break
      end = p
    }
    if (itemMap(i, keyCol, Boolean(m[2]))) for (let p = i + 1; p <= end; p++) shift[p] += 2
    if (block) skipUntil = end
  }
  return lines.map((l, i) => (l === "" || shift[i] === 0 ? l : l.slice(shift[i]))).join("\n")
}

// ---- services: the stored JSON back into the Service shape -----------------

function service(s: Json): Map<string, unknown> {
  const vol = (x: Json) => ordered([["quota_bytes", Number(x.quota_bytes) || 0, true], ["generation", Number(x.generation) || 0, true], ["name", str(x.name)], ["mount", str(x.mount)]])
  const health = (h: Json) => ordered([
    ["test", strings(h.test)], ["interval", str(h.interval), true], ["timeout", str(h.timeout), true],
    ["retries", Number(h.retries) || 0, true], ["start_period", str(h.start_period), true],
  ])
  const auto = (a: Json) => ordered([
    ["name", str(a.name)], ["inject_as_env", str(a.inject_as_env), true], ["inject_as_command", strings(a.inject_as_command), true],
    ["inject_to_agents", typeof a.inject_to_agents === "boolean" ? a.inject_to_agents : null, true],
    ["length", Number(a.length) || 0, true],
  ])
  const list = (v: unknown) => (Array.isArray(v) ? (v as Json[]) : [])
  return ordered([
    ["quota_enforced", s.quota_enforced === true, true],
    ["name", str(s.name)], ["image", str(s.image)],
    ["command", strings(s.command), true], ["env", stringMap(s.env), true], ["env_refs", strings(s.env_refs), true],
    ["ports", strings(s.ports), true], ["volumes", list(s.volumes).map(vol), true],
    ["healthcheck", s.healthcheck && typeof s.healthcheck === "object" ? health(s.healthcheck as Json) : null, true],
    ["auto_credentials", list(s.auto_credentials).map(auto), true],
    ["allow_unauthenticated", s.allow_unauthenticated === true, true],
  ])
}

// ---- the composition --------------------------------------------------------

async function json(get: Get, path: string): Promise<unknown> {
  const res = await get(path)
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status}`)
  return res.json()
}
const rowsOf = (body: unknown, key: string): Json[] => {
  if (Array.isArray(body)) return body as Json[]
  const inner = body && typeof body === "object" ? (body as Json)[key] : null
  return Array.isArray(inner) ? (inner as Json[]) : []
}
const byText = (f: (x: Json) => string) => (a: Json, b: Json) => { const x = f(a); const y = f(b); return x < y ? -1 : x > y ? 1 : 0 }

const BLANK_SKILL = (slug: string) => `---\nname: ${slug}\ndescription: (exported reference, supply body before re-apply)\n---\n`

export async function exportCrewManifest(
  workspaceId: string,
  slug: string,
  opts: ExportOptions = {},
  get: Get = (url) => apiFetch(url),
): Promise<CrewExport> {
  const withSkillBodies = opts.skillBodies !== false
  const withCredentials = opts.credentials !== false
  const ws = `workspace_id=${encodeURIComponent(workspaceId)}`
  const read = (path: string) => json(get, `${path}${path.includes("?") ? "&" : "?"}${ws}`)
  const warnings: string[] = []

  let crews: Json[]
  try { crews = rowsOf(await read("/api/v1/crews"), "crews") } catch (e) { throw new Error(`look up crew "${slug}": list crews: ${e instanceof Error ? e.message : e}`) }
  const crew = crews.find((c) => c.slug === slug)
  if (!crew) throw new Error(`crew "${slug}" not found`)
  if (str(crew.services_json) === REDACTED) throw new Error(`crew "${slug}": private service configuration cannot be exported; use the original manifest`)

  let agents: Json[]
  try { agents = rowsOf(await read(`/api/v1/agents?crew_id=${crew.id}`), "agents") } catch (e) { throw new Error(`list agents: ${e instanceof Error ? e.message : e}`) }
  let servers: Json[] = []
  try { servers = rowsOf(await read(`/api/v1/crews/${crew.id}/integrations`), "integrations") } catch { /* not fatal: the rest is still useful */ }

  const spec: Array<[string, unknown, boolean?]> = []

  // devcontainer
  const hasDev = [crew.runtime_image, crew.network_mode, crew.devcontainer_config, crew.mise_config, crew.container_memory_mb, crew.container_cpus, crew.container_ttl_hours].some((v) => v != null)
  if (hasDev) {
    let raw: unknown = null
    if (str(crew.devcontainer_config) !== "") {
      try {
        const parsed: unknown = JSON.parse(str(crew.devcontainer_config))
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) raw = sorted(parsed)
      } catch { /* not JSON: omitted, as the CLI omits what it cannot decode */ }
    }
    // Pointers (memory, cpus, ttl) are written whenever set, even to 0.
    const dev = new Map<string, unknown>()
    if (str(crew.mise_config) !== "") dev.set("mise", crew.mise_config)
    if (str(crew.runtime_image) !== "") dev.set("runtime_image", crew.runtime_image)
    if (crew.container_memory_mb != null) dev.set("memory_mb", crew.container_memory_mb)
    if (crew.container_cpus != null) dev.set("cpus", crew.container_cpus)
    if (crew.container_ttl_hours != null) dev.set("ttl_hours", crew.container_ttl_hours)
    if (str(crew.network_mode) !== "") dev.set("network_mode", crew.network_mode)
    if (strings(crew.allowed_domains).length) dev.set("allowed_domains", strings(crew.allowed_domains))
    if (raw) dev.set("raw", raw)
    spec.push(["devcontainer", dev])
  }

  // agents, with their skill and credential bindings
  agents.sort(byText((a) => str(a.slug)))
  const skillSlugs = new Set<string>()
  const credEnvs = new Set<string>()
  const agentDecls: Array<Map<string, unknown>> = []
  for (const a of agents) {
    const skills: string[] = []
    const envRefs: string[] = []
    try {
      const bindings = rowsOf(await read(`/api/v1/agents/${a.id}/skills`), "skills")
      const slugOf = (b: Json) => str((b.skill as Json | undefined)?.slug)
      bindings.sort(byText(slugOf))
      for (const b of bindings) if (slugOf(b)) { skills.push(slugOf(b)); skillSlugs.add(slugOf(b)) }
    } catch { warnings.push(`Skill bindings of ${str(a.slug)} could not be read; ${str(a.slug)} is exported without them.`) }
    try {
      const bindings = rowsOf(await read(`/api/v1/agents/${a.id}/credentials`), "credentials")
      bindings.sort(byText((b) => str(b.env_var_name)))
      for (const b of bindings) { envRefs.push(str(b.env_var_name)); credEnvs.add(str(b.env_var_name)) }
    } catch { warnings.push(`Credential bindings of ${str(a.slug)} could not be read; ${str(a.slug)} is exported without them.`) }
    const llm = ordered([["provider", str(a.llm_provider), true], ["model", str(a.llm_model), true]])
    agentDecls.push(ordered([
      ["slug", str(a.slug)], ["name", str(a.name)],
      ["role_title", str(a.role_title), true], ["agent_role", str(a.agent_role), true], ["cli_adapter", str(a.cli_adapter), true],
      ["llm", llm, true],
      ["tool_profile", str(a.tool_profile), true], ["timeout_seconds", Number(a.timeout_seconds) || 0, true], ["memory_enabled", a.memory_enabled === true, true],
      ["prompt", str(a.system_prompt), true], ["suggested_prompts", str(a.suggested_prompts), true], ["ask_forms", str(a.ask_forms), true],
      ["skills", skills, true], ["env_refs", envRefs, true],
    ]))
  }

  // credentials: one slot per env any agent binds
  if (credEnvs.size > 0 && withCredentials) {
    try {
      const all = rowsOf(await read("/api/v1/credentials"), "credentials")
      const byName = new Map(all.map((c) => [str(c.name), c]))
      spec.push(["credentials", [...credEnvs].sort().map((env) => ordered([["env", env], ["provider", str(byName.get(env)?.provider)], ["type", str(byName.get(env)?.type)]])), true])
    } catch { /* the slots are an extra; the file is still right without them */ }
  }

  // mcp servers
  servers.sort(byText((s) => str(s.name)))
  spec.push(["mcp_servers", servers.map((s) => ordered([
    ["name", str(s.name)], ["display_name", str(s.display_name), true], ["transport", str(s.transport)],
    ["command", str(s.command), true], ["endpoint", str(s.endpoint), true], ["enabled", s.enabled === true],
  ])), true])

  // skills: every skill an agent binds
  if (skillSlugs.size > 0) {
    try {
      const all = rowsOf(await read("/api/v1/skills"), "skills")
      const bySlug = new Map(all.map((s) => [str(s.slug), s]))
      const decls: Array<Map<string, unknown>> = []
      for (const sl of [...skillSlugs].sort()) {
        let inline = ""
        const found = bySlug.get(sl)
        if (withSkillBodies && found && str(found.id)) {
          try { inline = str(((await read(`/api/v1/skills/${found.id}`)) as Json).content) } catch { inline = "" }
        }
        if (inline === "") inline = BLANK_SKILL(sl)
        decls.push(ordered([["slug", sl], ["inline", withSkillBodies ? inline : "", true]]))
      }
      spec.push(["skills", decls, true])
    } catch { /* skill declarations are an extra */ }
  }

  spec.push(["agents", agentDecls])
  if (str(crew.services_json) !== "") {
    try {
      const svcs: unknown = JSON.parse(str(crew.services_json))
      if (Array.isArray(svcs)) spec.push(["services", svcs.map((s) => service(s as Json)), true])
    } catch { /* unreadable stored services are left out, as the CLI does */ }
  }

  // Go writes struct fields in declaration order; the spec lists them in it.
  const order = ["devcontainer", "credentials", "mcp_servers", "skills", "agents", "services"]
  spec.sort((x, y) => order.indexOf(x[0]) - order.indexOf(y[0]))

  const doc = ordered([
    ["apiVersion", API_VERSION], ["kind", "Crew"],
    ["metadata", ordered([
      ["name", str(crew.name)], ["slug", str(crew.slug)], ["description", str(crew.description), true],
      ["icon", str(crew.icon), true], ["color", str(crew.color), true],
    ])],
    ["spec", ordered(spec)],
  ])

  const text = stringify(doc, { indent: 4, lineWidth: 0, minContentWidth: 0, blockQuote: true })
  return { yaml: SCHEMA_LINE + alignListItemChildren(text), warnings }
}
