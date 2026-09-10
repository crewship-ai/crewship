import type { DefinitionChange, DefinitionChangeKind, DefinitionChangeTone, DefinitionDiff } from "@/lib/pages/editor-contract"
import { unifiedDiffText } from "./source-diff"

/**
 * Comparison of two Page documents (`internal/pages/spec.go` → `Document`).
 *
 * Every sentence this module produces is computed from the two documents. It
 * never reads a `summary`, a `changelog` or any other field the candidate
 * brought with it: an agent describing its own change is not review evidence,
 * and a reviewer cannot tell an authored sentence from a derived one once both
 * are in the same list. Anything the candidate wrote about itself can only
 * appear as an unmodelled key name and inside `raw`.
 *
 * Pure, synchronous, and written for hostile input — this runs over a document
 * an agent authored. It builds no HTML and evaluates nothing.
 */

type Dict = Record<string, unknown>

/**
 * The keys this comparator actually reads. Anything else in either document is
 * reported in `unmodelled`, which is the whole reason `raw` exists: the derived
 * list is honest about being partial rather than quietly complete.
 */
const DOCUMENT_KEYS = new Set(["apiVersion", "kind", "metadata", "spec"])
const METADATA_KEYS = new Set(["name", "slug", "description"])
const SPEC_KEYS = new Set(["panels"])
const PANEL_KEYS = new Set(["id", "title", "schema", "icon", "owner", "producer", "sla", "span", "public", "tab", "actions", "refresh", "wake", "on_failure"])
const ACTION_KEYS = new Set(["id", "kind", "label", "routine", "confirm"])

/** A panel that declares no span gets the full grid (`pages.DefaultSpan`). */
const DEFAULT_SPAN = "12"

/** Values are truncated before they enter a sentence: a summary is a line, not a payload. */
const MAX_VALUE = 60

export function compareDefinitions(before: unknown, after: unknown): DefinitionDiff {
  const unmodelled = new Set<string>()
  // Absent, null, or something that is not a document at all: in every one of
  // those cases there is nothing to compare against, and the flag says so on
  // the diff itself rather than as an entry in a list of key names.
  const baselineMissing = !isDict(before)
  const live = model(before, unmodelled, "before")
  const candidate = model(after, unmodelled, "after")

  const changes: DefinitionChange[] = []
  comparePage(live, candidate, changes)
  for (const panel of candidate.panels) {
    const previous = live.byId.get(panel.id)
    if (!previous) changes.push(panelAdded(panel))
    else comparePanel(previous, panel, changes)
  }
  for (const panel of live.panels) {
    if (!candidate.byId.has(panel.id)) {
      changes.push({ kind: "panel-removed", tone: "remove", panelId: panel.id, summary: `Removes panel ${quoted(panel.id)} from the live Page.`, before: panel.id })
    }
  }

  // Pretty-printed with sorted keys so that key order — which carries no
  // meaning in either JSON or YAML — cannot show up as a change.
  const livePretty = stableStringify(before)
  const candidatePretty = stableStringify(after)
  return {
    changes,
    unmodelled: [...unmodelled].sort(),
    raw: `--- live definition\n+++ candidate definition\n${unifiedDiffText(livePretty, candidatePretty)}`,
    identical: livePretty === candidatePretty,
    baselineMissing,
  }
}

// ── Derived changes ──────────────────────────────────────────────────────────

function comparePage(live: ModelledDocument, candidate: ModelledDocument, changes: DefinitionChange[]): void {
  if (live.name !== candidate.name) {
    changes.push({
      kind: "page-renamed",
      tone: live.name === "" ? "add" : candidate.name === "" ? "remove" : "change",
      summary:
        live.name === ""
          ? `Names the Page ${quoted(candidate.name)}.`
          : candidate.name === ""
            ? `The Page loses its name ${quoted(live.name)}.`
            : `Renames the Page from ${quoted(live.name)} to ${quoted(candidate.name)}.`,
      before: live.name,
      after: candidate.name,
    })
  }
  if (live.description !== candidate.description) {
    const tone: DefinitionChangeTone = live.description === "" ? "add" : candidate.description === "" ? "remove" : "change"
    changes.push({
      kind: "page-described",
      tone,
      summary:
        tone === "add"
          ? `Adds the Page description ${quoted(candidate.description)}.`
          : tone === "remove"
            ? "Removes the Page description."
            : `Changes the Page description to ${quoted(candidate.description)}.`,
      before: live.description,
      after: candidate.description,
    })
  }
  // These three exist on the live Page or they do not exist at all: gaining one
  // is what a missing baseline looks like, and `baselineMissing` says that
  // better than a sentence would.
  if (live.slug !== candidate.slug && live.slug !== "") {
    // The slug is the address, not a label: every link anyone has shared points
    // at the old one, and none of them survives the move.
    changes.push({
      kind: "page-slug-changed",
      tone: candidate.slug === "" ? "remove" : "change",
      summary:
        candidate.slug === ""
          ? `Removes the Page address /pages/${bare(live.slug)}; existing links to it stop working.`
          : `Changes the Page address from /pages/${bare(live.slug)} to /pages/${bare(candidate.slug)}; existing links to the old address stop working.`,
      before: live.slug,
      after: candidate.slug,
    })
  }
  for (const envelope of [
    { field: "apiVersion", before: live.apiVersion, after: candidate.apiVersion },
    { field: "kind", before: live.kind, after: candidate.kind },
  ]) {
    if (envelope.before === envelope.after || envelope.before === "") continue
    changes.push({
      kind: "document-version-changed",
      tone: envelope.after === "" ? "remove" : "change",
      summary:
        envelope.after === ""
          ? `Removes the document ${envelope.field} ${bare(envelope.before)}.`
          : `Changes the document ${envelope.field} from ${bare(envelope.before)} to ${bare(envelope.after)}.`,
      before: envelope.before,
      after: envelope.after,
    })
  }
}

function panelAdded(panel: ModelledPanel): DefinitionChange {
  const notes: string[] = []
  if (panel.public) notes.push("visible on public links")
  if (panel.actions.length > 0) notes.push(`with ${count(panel.actions.length, "action")}`)
  return {
    kind: "panel-added",
    tone: "add",
    panelId: panel.id,
    summary: `Adds panel ${quoted(panel.id)} to the live Page${notes.length ? `, ${notes.join(", ")}` : ""}.`,
    after: panel.id,
  }
}

function comparePanel(live: ModelledPanel, candidate: ModelledPanel, changes: DefinitionChange[]): void {
  const id = quoted(candidate.id)
  const field = (kind: DefinitionChangeKind, summary: string, before: string, after: string, tone: DefinitionChangeTone = "change") =>
    changes.push({ kind, tone, panelId: candidate.id, summary, before, after })

  if (live.title !== candidate.title) {
    field(
      "panel-retitled",
      live.title === ""
        ? `Panel ${id} is titled ${quoted(candidate.title)}.`
        : candidate.title === ""
          ? `Panel ${id} loses its title ${quoted(live.title)}.`
          : `Panel ${id} changes title from ${quoted(live.title)} to ${quoted(candidate.title)}.`,
      live.title,
      candidate.title,
    )
  }
  if (live.schema !== candidate.schema) {
    field("panel-schema-changed", `Panel ${id} changes schema from ${bare(live.schema)} to ${bare(candidate.schema)}.`, live.schema, candidate.schema)
  }
  if (live.owner !== candidate.owner) {
    field("panel-owner-changed", `Panel ${id} changes owner from ${bare(live.owner)} to ${bare(candidate.owner)}.`, live.owner, candidate.owner)
  }
  if (live.producer !== candidate.producer) {
    field("panel-producer-changed", `Panel ${id} changes producer from ${bare(live.producer)} to ${bare(candidate.producer)}.`, live.producer, candidate.producer)
  }
  if (live.public !== candidate.public) {
    // Reach, not decoration: this one is the reason `public` is per panel.
    field(
      "panel-visibility-changed",
      candidate.public ? `Panel ${id} becomes visible on public links.` : `Panel ${id} is no longer visible on public links.`,
      String(live.public),
      String(candidate.public),
    )
  }
  if (live.sla !== candidate.sla) {
    field("panel-sla-changed", `Panel ${id} changes SLA from ${bare(live.sla)} to ${bare(candidate.sla)}.`, live.sla, candidate.sla)
  }
  if (live.tab !== candidate.tab) {
    field(
      "panel-tab-changed",
      live.tab === ""
        ? `Panel ${id} moves onto tab ${quoted(candidate.tab)}.`
        : candidate.tab === ""
          ? `Panel ${id} no longer belongs to a tab.`
          : `Panel ${id} moves from tab ${quoted(live.tab)} to tab ${quoted(candidate.tab)}.`,
      live.tab,
      candidate.tab,
    )
  }
  if (live.refresh !== candidate.refresh) {
    field(
      "panel-refresh-changed",
      live.refresh === ""
        ? `Panel ${id} starts refreshing on ${bare(candidate.refresh)}.`
        : candidate.refresh === ""
          ? `Panel ${id} no longer refreshes automatically.`
          : `Panel ${id} changes refresh from ${bare(live.refresh)} to ${bare(candidate.refresh)}.`,
      live.refresh,
      candidate.refresh,
    )
  }
  if (live.wake.digest !== candidate.wake.digest) {
    const from = live.wake.count
    const to = candidate.wake.count
    field(
      "panel-wake-changed",
      from === 0
        ? `Panel ${id} gains ${count(to, "wake gate")}.`
        : to === 0
          ? `Panel ${id} loses its wake gates.`
          : from === to
            ? `Panel ${id} changes the conditions of its ${count(to, "wake gate")}.`
            : `Panel ${id} changes its wake gates from ${from} to ${to}.`,
      live.wake.digest,
      candidate.wake.digest,
    )
  }
  if (live.failure.digest !== candidate.failure.digest) {
    // Written as the consequence, not the field: what a reviewer needs to know
    // is whether this panel going quiet still produces work for a human.
    const gone = candidate.failure.issue === ""
    const arrived = live.failure.issue === ""
    changes.push({
      kind: "panel-failure-handling-changed",
      tone: gone ? "remove" : arrived ? "add" : "change",
      panelId: candidate.id,
      summary: gone
        ? `Panel ${id} going quiet no longer raises an issue for ${bare(live.failure.issue)}: from now on it fails silently.`
        : arrived
          ? `Panel ${id} going quiet now raises an issue for ${bare(candidate.failure.issue)}.`
          : live.failure.issue !== candidate.failure.issue
            ? `Panel ${id} going quiet now raises an issue for ${bare(candidate.failure.issue)} instead of ${bare(live.failure.issue)}.`
            : `Panel ${id} changes what happens when it goes quiet, still raising an issue for ${bare(candidate.failure.issue)}.`,
      before: live.failure.digest,
      after: candidate.failure.digest,
    })
  }
  if (live.span !== candidate.span) {
    field("panel-span-changed", `Panel ${id} changes width from ${bare(live.span)} to ${bare(candidate.span)} columns.`, live.span, candidate.span)
  }
  if (live.icon !== candidate.icon) {
    // Cosmetic, and the sentence says so by being plain: a panel that declares
    // no icon falls back to the one its schema implies.
    field(
      "panel-icon-changed",
      live.icon === ""
        ? `Panel ${id} sets its icon to ${bare(candidate.icon)}.`
        : candidate.icon === ""
          ? `Panel ${id} drops its ${bare(live.icon)} icon and falls back to the one its schema implies.`
          : `Panel ${id} changes icon from ${bare(live.icon)} to ${bare(candidate.icon)}.`,
      live.icon,
      candidate.icon,
    )
  }

  for (const action of candidate.actions) {
    const previous = live.actionsById.get(action.id)
    if (!previous) {
      changes.push({
        kind: "action-added",
        tone: "add",
        panelId: candidate.id,
        actionId: action.id,
        summary:
          action.kind === "call" && action.routine !== ""
            ? `Adds action ${quoted(action.id)} on panel ${id}, calling routine ${bare(action.routine)}.`
            : `Adds action ${quoted(action.id)} on panel ${id}${action.kind === "" ? "" : ` (${bare(action.kind)})`}.`,
        after: action.id,
      })
      continue
    }
    compareAction(candidate.id, previous, action, changes)
  }
  for (const action of live.actions) {
    if (!candidate.actionsById.has(action.id)) {
      changes.push({
        kind: "action-removed",
        tone: "remove",
        panelId: candidate.id,
        actionId: action.id,
        summary: `Removes action ${quoted(action.id)} from panel ${id}.`,
        before: action.id,
      })
    }
  }
}

function compareAction(panelId: string, live: ModelledAction, candidate: ModelledAction, changes: DefinitionChange[]): void {
  const where = `Action ${quoted(candidate.id)} on panel ${quoted(panelId)}`
  const field = (kind: DefinitionChangeKind, summary: string, before: string, after: string) =>
    changes.push({ kind, tone: "change", panelId, actionId: candidate.id, summary, before, after })

  if (live.kind !== candidate.kind) {
    // What the button DOES. `call` dispatches a routine on the server; `link`
    // and `toggle` and `custom` do not, so leaving `call` silently stops
    // something from running and the sentence has to name it.
    const orphaned = live.kind === "call" && live.routine !== "" ? ` Routine ${bare(live.routine)} is no longer called.` : ""
    field(
      "action-kind-changed",
      `${where} changes from ${bare(live.kind)} to ${bare(candidate.kind)}.${orphaned}`,
      live.kind,
      candidate.kind,
    )
  }
  if (live.routine !== candidate.routine) {
    field(
      "action-routine-changed",
      live.routine === ""
        ? `${where} now calls routine ${bare(candidate.routine)}.`
        : candidate.routine === ""
          ? `${where} no longer names a routine.`
          : `${where} changes routine from ${bare(live.routine)} to ${bare(candidate.routine)}.`,
      live.routine,
      candidate.routine,
    )
  }
  if (live.label !== candidate.label) {
    field("action-relabelled", `${where} changes label from ${quoted(live.label)} to ${quoted(candidate.label)}.`, live.label, candidate.label)
  }
  if (live.confirm !== candidate.confirm) {
    field(
      "action-confirm-changed",
      live.confirm === ""
        ? `${where} now asks for confirmation.`
        : candidate.confirm === ""
          ? `${where} no longer asks for confirmation.`
          : `${where} changes its confirmation step.`,
      live.confirm,
      candidate.confirm,
    )
  }
}

// ── Modelling ────────────────────────────────────────────────────────────────

interface ModelledAction {
  id: string
  kind: string
  label: string
  routine: string
  /** Stable JSON of the confirm block; "" when the action declares none. */
  confirm: string
}

/**
 * `on_failure` normalised. A block that routes nowhere is not a declaration:
 * absent, null, `{}` and `{issue: ""}` all mean the panel going quiet raises
 * nothing, and none of them may read as a change against the others.
 */
interface ModelledFailure {
  /** The crew an issue is raised for, "" when nothing is raised. */
  issue: string
  /** Stable JSON of the whole block, "" when it declares nothing. */
  digest: string
}

interface ModelledPanel {
  id: string
  title: string
  schema: string
  icon: string
  failure: ModelledFailure
  owner: string
  producer: string
  sla: string
  span: string
  public: boolean
  tab: string
  refresh: string
  wake: { count: number; digest: string }
  actions: ModelledAction[]
  actionsById: Map<string, ModelledAction>
}

interface ModelledDocument {
  apiVersion: string
  kind: string
  name: string
  slug: string
  description: string
  panels: ModelledPanel[]
  byId: Map<string, ModelledPanel>
}

/**
 * Read one document into the shape the comparison works on, recording every key
 * it had to skip. One walk does both: a separate "collect unknown keys" pass
 * would be free to disagree with what the comparison actually read.
 */
function model(input: unknown, unmodelled: Set<string>, side: "before" | "after"): ModelledDocument {
  const empty: ModelledDocument = { apiVersion: "", kind: "", name: "", slug: "", description: "", panels: [], byId: new Map() }
  if (input === null || input === undefined) return empty
  if (!isDict(input)) {
    // A string, a number or an array where a document belongs. On the baseline
    // side `baselineMissing` already carries that; on the candidate side
    // nothing else would, and every panel would silently read as removed.
    if (side === "after") unmodelled.add("<after is not a Page document>")
    return empty
  }
  for (const key of Object.keys(input)) if (!DOCUMENT_KEYS.has(key)) unmodelled.add(key)

  const metadata = input.metadata
  if (isDict(metadata)) {
    for (const key of Object.keys(metadata)) if (!METADATA_KEYS.has(key)) unmodelled.add(`metadata.${key}`)
  } else if (metadata !== undefined) {
    unmodelled.add("metadata:not-an-object")
  }

  const spec = input.spec
  if (isDict(spec)) {
    for (const key of Object.keys(spec)) if (!SPEC_KEYS.has(key)) unmodelled.add(`spec.${key}`)
  } else if (spec !== undefined) {
    unmodelled.add("spec:not-an-object")
  }

  const document: ModelledDocument = {
    apiVersion: text(input.apiVersion),
    kind: text(input.kind),
    name: isDict(metadata) ? text(metadata.name) : "",
    slug: isDict(metadata) ? text(metadata.slug) : "",
    description: isDict(metadata) ? text(metadata.description) : "",
    panels: [],
    byId: new Map(),
  }

  const panels = isDict(spec) ? spec.panels : undefined
  if (panels !== undefined && !Array.isArray(panels)) unmodelled.add("spec.panels:not-a-list")
  if (!Array.isArray(panels)) return document

  for (const entry of panels) {
    if (!isDict(entry)) {
      unmodelled.add("spec.panels[]:not-an-object")
      continue
    }
    for (const key of Object.keys(entry)) if (!PANEL_KEYS.has(key)) unmodelled.add(`spec.panels[].${key}`)
    const id = typeof entry.id === "string" ? entry.id : ""
    if (id === "") {
      // Panels are matched by id. One without a usable id cannot be matched,
      // and matching it by position would invent a pairing.
      unmodelled.add("spec.panels[].id:missing")
      continue
    }
    if (document.byId.has(id)) {
      // First wins, and the collision is named — a second panel under the same
      // id is silently unreviewable otherwise.
      unmodelled.add("spec.panels[].id:duplicate")
      continue
    }
    const panel = modelPanel(id, entry, unmodelled)
    document.panels.push(panel)
    document.byId.set(id, panel)
  }
  return document
}

function modelPanel(id: string, entry: Dict, unmodelled: Set<string>): ModelledPanel {
  const wake = entry.wake
  const failure = modelFailure(entry.on_failure)
  const panel: ModelledPanel = {
    id,
    title: text(entry.title),
    schema: text(entry.schema),
    icon: text(entry.icon),
    failure,
    owner: text(entry.owner),
    producer: text(entry.producer),
    sla: text(entry.sla),
    span: text(entry.span) === "" ? DEFAULT_SPAN : text(entry.span),
    public: entry.public === true,
    tab: text(entry.tab),
    refresh: text(entry.refresh),
    wake: { count: Array.isArray(wake) ? wake.length : 0, digest: wake === undefined ? "" : stableStringify(wake) },
    actions: [],
    actionsById: new Map(),
  }

  const actions = entry.actions
  if (actions !== undefined && !Array.isArray(actions)) unmodelled.add("spec.panels[].actions:not-a-list")
  if (!Array.isArray(actions)) return panel
  for (const raw of actions) {
    if (!isDict(raw)) {
      unmodelled.add("spec.panels[].actions[]:not-an-object")
      continue
    }
    for (const key of Object.keys(raw)) if (!ACTION_KEYS.has(key)) unmodelled.add(`spec.panels[].actions[].${key}`)
    const actionId = typeof raw.id === "string" ? raw.id : ""
    if (actionId === "") {
      unmodelled.add("spec.panels[].actions[].id:missing")
      continue
    }
    if (panel.actionsById.has(actionId)) {
      unmodelled.add("spec.panels[].actions[].id:duplicate")
      continue
    }
    const action: ModelledAction = {
      id: actionId,
      kind: text(raw.kind),
      label: text(raw.label),
      routine: text(raw.routine),
      confirm: raw.confirm === undefined || raw.confirm === null ? "" : stableStringify(raw.confirm),
    }
    panel.actions.push(action)
    panel.actionsById.set(actionId, action)
  }
  return panel
}

function modelFailure(value: unknown): ModelledFailure {
  if (!isDict(value)) return { issue: "", digest: "" }
  const issue = text(value.issue)
  const digest = stableStringify(value)
  // `{}` and `{issue: ""}` declare nothing; treat them as the absence they are.
  if (issue === "" && Object.keys(value).every(key => text(value[key]) === "")) return { issue: "", digest: "" }
  return { issue, digest }
}

// ── Text helpers ─────────────────────────────────────────────────────────────

function isDict(value: unknown): value is Dict {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/** A comparable, displayable form of a scalar field, whatever arrived in it. */
function text(value: unknown): string {
  if (typeof value === "string") return value
  if (value === null || value === undefined) return ""
  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") return String(value)
  return stableStringify(value)
}

/** Summaries are one line of plain text: no control characters, bounded length. */
function clean(value: string): string {
  const flat = value.replace(/[\u0000-\u001f\u007f]+/g, " ").replace(/\s+/g, " ").trim()
  return flat.length > MAX_VALUE ? `${flat.slice(0, MAX_VALUE - 1)}…` : flat
}

function quoted(value: string): string {
  return `"${clean(value)}"`
}

function bare(value: string): string {
  const flat = clean(value)
  return flat === "" ? "none" : flat
}

function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`
}

/**
 * JSON with sorted object keys and two-space indent — the form both documents
 * are rendered in before they are diffed, so `raw` shows content differences
 * and nothing else. Cycles and absurd nesting are bounded rather than thrown
 * on: this serialises an agent-authored document.
 */
function stableStringify(value: unknown): string {
  const seen = new Set<object>()
  const walk = (node: unknown, pad: string, depth: number): string => {
    if (typeof node === "bigint") return JSON.stringify(node.toString())
    if (typeof node === "number") return Number.isFinite(node) ? String(node) : "null"
    if (node === null || typeof node === "boolean") return String(node)
    if (typeof node === "string") return JSON.stringify(node)
    if (typeof node !== "object") return "null" // undefined, function, symbol
    if (seen.has(node)) return '"[circular]"'
    if (depth > 64) return '"[too deep]"'
    seen.add(node)
    const inner = `${pad}  `
    let out: string
    if (Array.isArray(node)) {
      out = node.length === 0 ? "[]" : `[\n${node.map(item => inner + walk(item, inner, depth + 1)).join(",\n")}\n${pad}]`
    } else {
      const record = node as Dict
      const keys = Object.keys(record)
        .filter(key => record[key] !== undefined && typeof record[key] !== "function")
        .sort()
      out = keys.length === 0 ? "{}" : `{\n${keys.map(key => `${inner}${JSON.stringify(key)}: ${walk(record[key], inner, depth + 1)}`).join(",\n")}\n${pad}}`
    }
    seen.delete(node)
    return out
  }
  return walk(value, "", 0)
}
