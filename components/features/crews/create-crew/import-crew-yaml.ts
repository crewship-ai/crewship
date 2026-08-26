import { parse as parseYAML } from "yaml"
import {
  CPU_MAX,
  CPU_MIN,
  MEMORY_MAX_MB,
  MEMORY_MIN_MB,
  asCrewColor,
  normalizeSlug,
  type WizardState,
} from "./types"

/**
 * Read a `kind: Crew` manifest into the wizard, rather than applying it.
 *
 * `crewship apply -f crew.yaml` has been able to create a crew from YAML for
 * a while; the browser has had no way in at all. This is that way in, and it
 * deliberately stops one step short of what the CLI does: apply is
 * all-or-nothing and tells you what happened afterwards, this fills the form
 * in and lets you look at it first.
 *
 * ## What it does NOT bring, and why that is stated rather than hidden
 *
 * The wizard's submit path (submit.ts) creates a crew — `POST /api/v1/crews`
 * plus a PATCH for overrides. It has no call that creates an agent, a
 * credential, an MCP server, a skill, a sidecar service or a shared file. A
 * manifest can declare all six. Importing one and silently keeping the 5% we
 * can express would hand back a crew that is missing the four agents the file
 * was mostly about, with nothing on screen having said so.
 *
 * So the parser counts them and returns them in `notImported`, and the caller
 * is expected to show that list. `crewship apply` remains the answer for the
 * whole document.
 *
 * ## Two manifest shapes
 *
 * Both are spelled `kind: Crew`. The SPEC-2 standalone kind
 * (internal/manifest/kinds/crew.go) has no `agents:` at all; the legacy
 * combined-manifest crew entry (internal/manifest/schema.go) requires it, and
 * that is the shape every file in examples/manifests/ uses. Reading is
 * lenient on purpose — we take the fields we know from wherever they appear
 * and count the rest.
 */

/** A field group the file declared and the wizard cannot create. */
export interface UnimportedBlock {
  label: string
  count: number
}

export interface CrewImportResult {
  patch: Partial<WizardState>
  notImported: UnimportedBlock[]
  /** Names pulled out of `spec.agents`, to show what is being left behind. */
  agentNames: string[]
}

export class CrewImportError extends Error {
  constructor(message: string) {
    super(message)
    this.name = "CrewImportError"
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v)
}

function str(v: unknown): string {
  return typeof v === "string" ? v.trim() : ""
}

function countOf(v: unknown): number {
  if (Array.isArray(v)) return v.length
  if (isRecord(v)) return Object.keys(v).length
  return 0
}

/** Clamp a manifest-supplied number into the range the wizard's chips allow. */
function clampNum(v: unknown, min: number, max: number): number | null {
  const n = typeof v === "number" ? v : Number(v)
  if (!Number.isFinite(n) || n <= 0) return null
  return Math.min(max, Math.max(min, n))
}

/**
 * Build the devcontainer.json string the wizard's RuntimeConfig round-trips.
 *
 * The manifest models this as typed sub-fields (image / features / env /
 * post_create_command) plus a `raw:` passthrough; devcontainer.json wants
 * `containerEnv` and `postCreateCommand`. parseDevcontainerFull keeps any key
 * it does not model in `passthrough`, so `raw:` contents survive a round trip
 * — which is why raw is spread FIRST and the typed fields overwrite it, the
 * same precedence the Go side uses (kinds/crew.go: "typed fields winning on
 * collision").
 */
function devcontainerJSON(dc: Record<string, unknown>): string {
  const out: Record<string, unknown> = {}

  if (isRecord(dc.raw)) Object.assign(out, dc.raw)

  const image = str(dc.image)
  if (image) out.image = image
  if (isRecord(dc.features) && Object.keys(dc.features).length > 0) out.features = dc.features
  if (isRecord(dc.env) && Object.keys(dc.env).length > 0) out.containerEnv = dc.env
  const post = str(dc.post_create_command)
  if (post) out.postCreateCommand = post

  return Object.keys(out).length > 0 ? JSON.stringify(out, null, 2) : ""
}

/** The mise column's shape: `{"tools": {...}}`, JSON even though mise is TOML. */
function miseJSON(mise: Record<string, unknown>): string {
  const tools = isRecord(mise.tools) ? mise.tools : null
  if (!tools || Object.keys(tools).length === 0) return ""
  // Versions arrive as YAML scalars — `node: 22` parses to a number, and the
  // column is map[string]string on the Go side (devcontainer.MiseConfig).
  const asStrings: Record<string, string> = {}
  for (const [tool, version] of Object.entries(tools)) {
    asStrings[tool] = version == null ? "" : String(version)
  }
  return JSON.stringify({ tools: asStrings })
}

/**
 * Parse a crew manifest into a wizard patch.
 *
 * @throws {CrewImportError} with a message meant to be shown as-is.
 */
export function parseCrewManifest(text: string): CrewImportResult {
  if (!text.trim()) throw new CrewImportError("The file is empty.")

  let doc: unknown
  try {
    doc = parseYAML(text)
  } catch (e) {
    // The yaml package's messages carry line/column, which is the useful part.
    throw new CrewImportError(`This is not valid YAML — ${e instanceof Error ? e.message : String(e)}`)
  }

  if (!isRecord(doc)) throw new CrewImportError("A manifest must be a YAML mapping with apiVersion, kind and metadata.")

  const kind = str(doc.kind)
  if (!kind) throw new CrewImportError("No `kind:` in this file. A crew manifest starts with `kind: Crew`.")
  if (kind === "CrewTemplate") {
    throw new CrewImportError(
      "`kind: CrewTemplate` deploys a template that already exists on the server — the list below does that. Import takes `kind: Crew`.",
    )
  }
  if (kind !== "Crew") {
    throw new CrewImportError(`This file is \`kind: ${kind}\`. Import takes \`kind: Crew\`.`)
  }

  const metadata = isRecord(doc.metadata) ? doc.metadata : {}
  const spec = isRecord(doc.spec) ? doc.spec : {}

  const patch: Partial<WizardState> = {
    // A file being imported is never "browse": the wizard has no template to
    // deploy, so submit.ts must take the POST /crews path.
    mode: "empty",
    pickedTemplateSlug: null,
    pickedTemplateMeta: null,
  }

  // ── Identity ────────────────────────────────────────────────────────────
  // metadata carries it in both shapes; the legacy crew entry also allows
  // spec.name / spec.description, so spec fills gaps metadata left.
  const name = str(metadata.name) || str(spec.name)
  const slug = str(metadata.slug) || str(spec.slug)
  const description = str(metadata.description) || str(spec.description)
  const icon = str(metadata.icon) || str(spec.icon)
  const color = str(metadata.color) || str(spec.color)

  if (name) patch.name = name
  if (slug || name) {
    // Through the same normaliser the field enforces — a manifest slug is not
    // guaranteed to satisfy validSlugRe, and a bad one imported verbatim
    // would come back as a 400 on Create.
    patch.slug = normalizeSlug(slug || name)
    patch.slugTouched = true
  }
  if (description) patch.description = description
  if (icon) patch.icon = icon
  // asCrewColor drops a hex code to "blue" — the manifest allows `#RRGGBB`
  // and the wizard's palette is eight tokens.
  if (color) patch.color = asCrewColor(color)

  // ── Container ───────────────────────────────────────────────────────────
  const runtimeImage = str(spec.runtime_image)
  if (runtimeImage) patch.runtimeImage = runtimeImage

  const dc = isRecord(spec.devcontainer) ? spec.devcontainer : null
  if (dc) {
    const json = devcontainerJSON(dc)
    if (json) patch.devcontainerConfig = json

    const mem = clampNum(dc.memory_mb, MEMORY_MIN_MB, MEMORY_MAX_MB)
    if (mem !== null) patch.memoryMB = mem
    const cpus = clampNum(dc.cpus, CPU_MIN, CPU_MAX)
    if (cpus !== null) patch.cpus = cpus
  }

  const mise = isRecord(spec.mise) ? spec.mise : null
  if (mise) {
    const json = miseJSON(mise)
    if (json) patch.miseConfig = json
  }

  // ── What the wizard cannot create ───────────────────────────────────────
  const agents = Array.isArray(spec.agents) ? spec.agents : []
  const agentNames = agents
    .map((a) => (isRecord(a) ? str(a.name) || str(a.slug) : ""))
    .filter(Boolean)

  const notImported: UnimportedBlock[] = []
  const declare = (label: string, n: number) => {
    if (n > 0) notImported.push({ label, count: n })
  }
  declare("agents", agents.length)
  declare("credentials", countOf(spec.credentials))
  declare("MCP servers", countOf(spec.mcp_servers))
  declare("skills", countOf(spec.skills))
  declare("services", countOf(spec.services))
  declare("shared files", countOf(spec.files))

  return { patch, notImported, agentNames }
}
