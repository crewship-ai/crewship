// routine-flow — pure, framework-free derivation of a read-only
// horizontal "data flow" diagram and a plain-language step list for a
// routine detail page. Consumes the raw DSL (routine.definition) plus the
// server-derived `manifest` (blast radius) and emits an ordered list of
// flow nodes the diagram component renders, and a list of human-readable
// step lines the "What it does" panel renders.
//
// Everything here is DEFENSIVE — a malformed routine must never throw, it
// degrades to the trigger/output bookends. No React, no I/O, fully unit
// testable (lib/__tests__/routine-flow.test.ts).

// ── Manifest shape (mirrors internal/pipeline/manifest.go → JSON) ──────────
export interface ManifestCred {
  type: string
  scope?: string
}
export interface ManifestDatastore {
  type: string
  name?: string
  note?: string
}
export interface ManifestTool {
  type: string
  name?: string
}
export interface RoutineManifest {
  integrations: string[]
  egress: string[]
  credentials: ManifestCred[]
  agents: string[]
  routines: string[]
  datastores: ManifestDatastore[]
  tools: ManifestTool[]
  has_http: boolean
  has_code: boolean
}

// ── Flow node model ────────────────────────────────────────────────────────
// `kind` drives color in the diagram (trigger=amber, agent=indigo,
// store=cyan, tool=violet, out=green, step=neutral). `iconKey` is a stable
// string the React layer maps to a lucide-react icon (keeps this module
// JSX-free).
export type FlowNodeKind = "trigger" | "step" | "agent" | "store" | "tool" | "out"

export type FlowIconKey =
  | "trigger"
  | "http"
  | "agent"
  | "transform"
  | "code"
  | "wait"
  | "call"
  | "store-redis"
  | "store-postgres"
  | "store-mysql"
  | "store-mongodb"
  | "store"
  | "tool"
  | "out"

export interface FlowNode {
  id: string
  kind: FlowNodeKind
  label: string
  detail?: string
  iconKey: FlowIconKey
}

// ── tiny defensive accessors ───────────────────────────────────────────────
function asRecord(v: unknown): Record<string, unknown> | null {
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null
}
function asArray(v: unknown): unknown[] {
  return Array.isArray(v) ? v : []
}
function str(v: unknown): string {
  return typeof v === "string" ? v : ""
}
function truncate(s: string, n = 40): string {
  const t = s.trim()
  return t.length > n ? `${t.slice(0, n - 1)}…` : t
}

// hostOf parses a hostname from an http URL, mirroring Go's hostFromURL:
// templated (`{{ }}`) or unparseable URLs yield "" so the caller can fall
// back to the raw string.
function hostOf(raw: string): string {
  const s = raw.trim()
  if (!s || s.includes("{{")) return ""
  try {
    return new URL(s).hostname
  } catch {
    return ""
  }
}

// ── datastore + tool label/icon mapping ────────────────────────────────────
const STORE_LABELS: Record<string, string> = {
  redis: "Redis",
  postgres: "Postgres",
  postgresql: "Postgres",
  mysql: "MySQL",
  mongodb: "MongoDB",
  mongo: "MongoDB",
}
function storeLabel(type: string): string {
  const k = type.toLowerCase()
  return STORE_LABELS[k] ?? (type ? type[0].toUpperCase() + type.slice(1) : "Store")
}
function storeIconKey(type: string): FlowIconKey {
  switch (type.toLowerCase()) {
    case "redis":
      return "store-redis"
    case "postgres":
    case "postgresql":
      return "store-postgres"
    case "mysql":
      return "store-mysql"
    case "mongodb":
    case "mongo":
      return "store-mongodb"
    default:
      return "store"
  }
}

// ── per-step → flow node ───────────────────────────────────────────────────
function stepToNode(raw: unknown, i: number): FlowNode | null {
  const s = asRecord(raw)
  if (!s) return null
  const type = str(s["type"])
  const id = str(s["id"]) || `step-${i}`

  switch (type) {
    case "http": {
      const http = asRecord(s["http"])
      const url = str(http?.["url"])
      const host = hostOf(url)
      return {
        id,
        kind: "step",
        label: "Fetch",
        detail: host || url || str(http?.["method"]) || "HTTP",
        iconKey: "http",
      }
    }
    case "agent_run":
      return {
        id,
        kind: "agent",
        label: "Agent",
        detail: str(s["agent_slug"]) || truncate(str(s["prompt"])) || "AI step",
        iconKey: "agent",
      }
    case "transform": {
      const t = asRecord(s["transform"])
      return {
        id,
        kind: "step",
        label: "Transform",
        detail: truncate(str(t?.["expression"])) || "reshape",
        iconKey: "transform",
      }
    }
    case "code": {
      const c = asRecord(s["code"])
      return {
        id,
        kind: "step",
        label: "Code",
        detail: str(c?.["runtime"]) || "script",
        iconKey: "code",
      }
    }
    case "wait": {
      const w = asRecord(s["wait"])
      return {
        id,
        kind: "step",
        label: "Wait",
        detail: str(w?.["kind"]) || "pause",
        iconKey: "wait",
      }
    }
    case "call_pipeline":
      return {
        id,
        kind: "step",
        label: "Routine",
        detail: str(s["pipeline_slug"]) || "sub-routine",
        iconKey: "call",
      }
    default:
      // Unknown/future step type — render a neutral node rather than drop it.
      return { id, kind: "step", label: type || "Step", iconKey: "transform" }
  }
}

/**
 * buildFlowNodes derives the ordered, read-only data-flow node chain:
 *   trigger → [one node per DSL step] → [datastore nodes] → [tool nodes] → output
 *
 * Resource nodes (datastores 🟥/🐘, tools 📜) come from the manifest and are
 * appended after the step chain — the manifest aggregates them across the
 * whole routine without a per-step link, so they sit between the last step
 * and the terminal output node. Pure + never throws.
 */
export function buildFlowNodes(dsl: unknown, manifest?: RoutineManifest | null): FlowNode[] {
  const def = asRecord(dsl)
  const nodes: FlowNode[] = []

  // 1. Trigger bookend.
  const inputs = asArray(def?.["inputs"])
  nodes.push({
    id: "trigger",
    kind: "trigger",
    label: "Trigger",
    detail: inputs.length > 0 ? `${inputs.length} input${inputs.length === 1 ? "" : "s"}` : "manual / scheduled",
    iconKey: "trigger",
  })

  // 2. One node per step, in DSL order.
  asArray(def?.["steps"]).forEach((raw, i) => {
    const node = stepToNode(raw, i)
    if (node) nodes.push(node)
  })

  // 3. Resource nodes from the manifest (datastores, then tools).
  if (manifest) {
    asArray(manifest.datastores).forEach((raw, i) => {
      const ds = asRecord(raw)
      if (!ds) return
      const type = str(ds["type"])
      if (!type) return
      const note = str(ds["note"])
      const name = str(ds["name"])
      nodes.push({
        id: `store-${type}-${name || i}`,
        kind: "store",
        label: storeLabel(type),
        detail: note || name || undefined,
        iconKey: storeIconKey(type),
      })
    })
    asArray(manifest.tools).forEach((raw, i) => {
      const t = asRecord(raw)
      if (!t) return
      const type = str(t["type"])
      if (!type) return
      const name = str(t["name"])
      nodes.push({
        id: `tool-${type}-${name || i}`,
        kind: "tool",
        label: type,
        detail: name || undefined,
        iconKey: "tool",
      })
    })
  }

  // 4. Output bookend.
  const outputs = asArray(def?.["outputs"])
  nodes.push({
    id: "output",
    kind: "out",
    label: "Output",
    detail: outputs.length > 0 ? `${outputs.length} output${outputs.length === 1 ? "" : "s"}` : "done",
    iconKey: "out",
  })

  return nodes
}

// ── plain-language steps ("What it does") ──────────────────────────────────
export type StepDeterminism = "ai" | "script"

/**
 * stepDeterminism classifies a step as AI (non-deterministic LLM call) or
 * script (deterministic). Only `agent_run` invokes a model; everything else
 * — http, code, transform, wait, call_pipeline — is deterministic.
 */
export function stepDeterminism(type: string): StepDeterminism {
  return type === "agent_run" ? "ai" : "script"
}

export interface PlainStep {
  id: string
  // 0 = the trigger line; 1..N = the executable steps in order.
  index: number
  title: string
  detail?: string
  determinism: StepDeterminism | "trigger"
}

function describeStep(s: Record<string, unknown>): { title: string; detail?: string } {
  const type = str(s["type"])
  switch (type) {
    case "http": {
      const http = asRecord(s["http"])
      const method = str(http?.["method"]) || "GET"
      const url = str(http?.["url"])
      const host = hostOf(url) || url
      return { title: `Fetch over HTTP${host ? ` from ${host}` : ""}`, detail: `${method} ${truncate(url, 60) || "—"}` }
    }
    case "agent_run": {
      const slug = str(s["agent_slug"])
      const prompt = truncate(str(s["prompt"]), 80)
      return {
        title: `Agent${slug ? ` @${slug}` : ""} decides what to do`,
        detail: prompt || "AI step — output is non-deterministic",
      }
    }
    case "code": {
      const c = asRecord(s["code"])
      const rt = str(c?.["runtime"]) || "script"
      return { title: `Run a ${rt} script`, detail: rt }
    }
    case "transform": {
      const t = asRecord(s["transform"])
      return { title: "Reshape the data", detail: truncate(str(t?.["expression"]), 60) || undefined }
    }
    case "wait": {
      const w = asRecord(s["wait"])
      const kind = str(w?.["kind"]) || "condition"
      return { title: `Pause until ${kind}`, detail: kind }
    }
    case "call_pipeline": {
      const slug = str(s["pipeline_slug"])
      return { title: `Call routine${slug ? ` ${slug}` : ""}`, detail: slug || undefined }
    }
    default:
      return { title: type || "Step" }
  }
}

/**
 * buildPlainSteps renders the routine as an ordered, human-readable list:
 * a trigger line followed by one line per executable step, each tagged with
 * its determinism (AI vs script). Pure + never throws.
 */
export function buildPlainSteps(dsl: unknown): PlainStep[] {
  const def = asRecord(dsl)
  const out: PlainStep[] = [
    {
      id: "trigger",
      index: 0,
      title: "Runs on trigger — manual, schedule, or webhook",
      determinism: "trigger",
    },
  ]
  let n = 0
  asArray(def?.["steps"]).forEach((raw, i) => {
    const s = asRecord(raw)
    if (!s) return
    n += 1
    const { title, detail } = describeStep(s)
    out.push({
      id: str(s["id"]) || `step-${i}`,
      index: n,
      title,
      detail,
      determinism: stepDeterminism(str(s["type"])),
    })
  })
  return out
}
