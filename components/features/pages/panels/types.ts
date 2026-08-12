/**
 * Panel vocabulary — the closed set from PRD `docs/prd/pages.md` §3.
 *
 * The set is closed on purpose: a new panel kind is a server release, never a
 * user-supplied string. Everything downstream (the registry, the renderer)
 * treats `schema` as untrusted text and narrows it through `isPanelSchema`
 * before it is allowed to select a component.
 */

/**
 * The five schemas of §3, in the order it lists them, plus `embed.v1` — the
 * sandboxed escape hatch of §3.1, which ships in v1.2 but whose *name* is
 * reserved from the first migration "so the closed enum does not need a
 * breaking change to admit it". It is reserved in Go (`internal/pages/schema.go`
 * `SchemaEmbed`) and in the migration's `CHECK`, so a page carrying one is a
 * valid page; leaving it out here would render it as an unknown schema — "this
 * version does not render embed.v1" — when the truth is "not yet".
 */
export const PANEL_SCHEMAS = [
  "metric.v1",
  "series.v1",
  "status.v1",
  "table.v1",
  "narrative.v1",
  "embed.v1",
] as const

export type PanelSchema = (typeof PANEL_SCHEMAS)[number]

/**
 * The subset this build actually renders — five of the six. `embed.v1` is
 * staged later (§3.1: it needs a second origin and a sandbox proxy, not a
 * payload type) but is part of the closed enum from the first migration, so
 * the registry carries an entry for it rather than pretending it is an unknown
 * string. Kept in step with `producibleSchemas` in internal/pages/schema.go:
 * a schema the server accepts pushes for and the client draws nothing for is
 * a panel that silently stops telling the truth.
 */
export const IMPLEMENTED_PANEL_SCHEMAS = [
  "metric.v1",
  "series.v1",
  "status.v1",
  "table.v1",
  "narrative.v1",
] as const

/**
 * Narrows an untrusted string to the closed enum.
 *
 * Uses a Set rather than `schema in registry`, so inherited keys — `__proto__`,
 * `constructor`, `toString` — can never select a component.
 */
const PANEL_SCHEMA_SET: ReadonlySet<string> = new Set<string>(PANEL_SCHEMAS)

export function isPanelSchema(value: unknown): value is PanelSchema {
  return typeof value === "string" && PANEL_SCHEMA_SET.has(value)
}

/**
 * Freshness (§4). The first three are computed server-side and never by the
 * producer. `never_produced` is the fourth *rendering* case — a panel that has
 * no payload row at all — and §9b.4 gives it its own line in the table.
 */
export const PANEL_STATES = ["fresh", "stale", "failed", "never_produced"] as const
export type PanelState = (typeof PANEL_STATES)[number]

/**
 * Server-attached, never producer-claimed (§4.5).
 *
 * The field names are the WIRE names. §11b.4 pins provenance as a nested
 * `{producer, run_id, produced_at}` and the repo's API convention is
 * snake_case throughout (`internal/api/saved_view_handler.go`), so a panel
 * type spelling these `runId` / `producedAt` is a client that quietly reads a
 * field the server never sends — the exact "client and server that both pass
 * their own tests" §11b exists to prevent. `scripts/test-harness/test-pages.sh`
 * probes for `provenance.run_id` and `provenance.produced_at`.
 */
export interface PanelProvenance {
  /** `routine/nightly-close`, `script/watch-services.sh`, … */
  producer?: string | null
  run_id?: string | null
  /** ISO-8601, or anything `new Date()` parses. */
  produced_at?: string | Date | null
}

/** The panel as declared in the page spec (§6 layer 1, §10 `page_panels`). */
export interface PanelSpec {
  id: string
  /** Untrusted until narrowed — this is a string, not a `PanelSchema`. */
  schema: string
  title?: string
  /** Permission anchor, not a label. */
  owner?: string | null
  /** 1..12, consumed by the page grid — not by the panel itself. */
  span?: number
  /** §11b.3: `sla_seconds` (integer) on the wire; `sla: 30s` is YAML sugar. */
  sla_seconds?: number
  /**
   * §7.1 rule 2 / §11b.14: this panel exists on the page but this viewer may
   * not see it, so the server sent `{panel_id, span, sealed: true,
   * owner_crew_name}` and NOTHING else — no schema, no payload, no producer,
   * no SLA.
   *
   * The renderer keys on this flag and never on a missing field: *"a
   * serialisation bug can never be mistaken for a permission decision."* A
   * panel with no schema that is not sealed is a bug and renders as one.
   */
  sealed?: boolean
  /**
   * The crew that owns the sealed panel, so the placeholder can say *"Hidden ·
   * crew Účetní"* rather than leaving a blank rectangle. The server takes
   * trouble to send it precisely so the reader knows who to ask.
   *
   * Spelled camelCase because `hooks/use-pages.ts` normalises it — the wire
   * name pinned in §11b.14 is `owner_crew_name`, and this field carries it
   * verbatim: `PanelSpec` already spells `sla_seconds` the wire way, and a
   * type that mixes both conventions is a type nobody can guess.
   */
  owner_crew_name?: string | null
}

/** The panel payload as produced by a machine (§6 layer 2), plus its state. */
export interface PanelSnapshot<P = unknown> {
  state: PanelState
  payload?: P | null
  provenance?: PanelProvenance | null
  /** Internal failure text. Never rendered on a public page (§7.3.2b). */
  failure?: string | null
  /** Overrides the default "how to make data arrive" sentence (§9b.3). */
  emptyHint?: string | null
}

export interface PanelProps {
  panel: PanelSpec
  data: PanelSnapshot
  /** Injected clock — absolute ages are computed, so tests pin `now`. */
  now?: Date
  /** Public view: age yes, reason no (§7.3.2b). */
  publicView?: boolean
  className?: string
}

// ── Payload cores (§3) ────────────────────────────────────────────────────

export interface MetricPayload {
  /**
   * `null` — and only `null` — is "no basis to compute" (§9b.4). A measured
   * `0` is a `0`, and so is an empty string: `internal/pages/payload.go`
   * `IsNoData()` treats JSON null alone as no data, and a client that also
   * swallowed `""` would draw an em dash over something the server counted.
   */
  value?: number | string | null
  unit?: string | null
  delta?: number | null
  /**
   * Which direction is an improvement (§11b.9). Absent by default: §3 does not
   * say whether a rising number is good, and green-up on an error rate would be
   * a lie. The wire name is `delta_good` — there has never been a `deltaGood`
   * on the wire, and reading one is how this opt-in never fires.
   */
  delta_good?: "up" | "down" | null
  target?: number | null
  sparkline?: number[] | null
}

export const STATUS_ITEM_STATES = ["ok", "warning", "critical"] as const
export type StatusItemState = (typeof STATUS_ITEM_STATES)[number]

export interface StatusItem {
  name: string
  /** Untrusted: an unrecognised value degrades to a neutral row. */
  state: string
  label?: string | null
}

export interface StatusPayload {
  items?: StatusItem[] | null
}

export type TableAlign = "left" | "center" | "right"

export interface TableColumn {
  key: string
  label?: string | null
  align?: TableAlign | null
}

export type TableCell = string | number | boolean | null | undefined

/** Keyed rows are the documented shape; positional rows are accepted too. */
export type TableRow = Record<string, TableCell> | TableCell[]

export interface TablePayload {
  columns?: TableColumn[] | null
  rows?: TableRow[] | null
}

// ── narrative.v1 (§3, §8) ─────────────────────────────────────────────────

/**
 * The block kinds. Two, both prose. There is no `html`, no `code` and no
 * `image` — §8 rule 1 says the agent fills a schema and never emits markup,
 * and rule 2 says images are absent from the schema rather than sanitised.
 */
export const NARRATIVE_BLOCK_KINDS = ["paragraph", "list"] as const
export type NarrativeBlockKind = (typeof NARRATIVE_BLOCK_KINDS)[number]

/**
 * The nouns a block may point at. §8 rule 3: a block references an internal
 * entity BY ID and the renderer builds the URL — it may never carry one.
 * Slack AI's private-channel exfiltration was a rendered link, so this type
 * has no field a destination could travel in, and the route table lives in
 * the panel component.
 */
export const ENTITY_REF_KINDS = ["issue", "run", "page", "agent", "crew"] as const
export type EntityRefKind = (typeof ENTITY_REF_KINDS)[number]

export interface EntityRef {
  /** Untrusted: an unrecognised kind renders as plain text, never as a link. */
  kind?: string | null
  id?: string | null
}

export interface NarrativeBlock {
  /** Untrusted: an unrecognised kind renders as a paragraph, never as markup. */
  kind?: string | null
  text?: string | null
  ref?: EntityRef | null
}

export interface NarrativePayload {
  blocks?: NarrativeBlock[] | null
  /**
   * The one-line conclusion. Optional and never null — the em dash means "no
   * basis to compute a value" (§9b.4), and a missing sentence is not a missing
   * measurement, so the glyph is not borrowed here.
   */
  verdict?: string | null
}

// ── series.v1 (§3) ────────────────────────────────────────────────────────

export interface SeriesEntry {
  name?: string | null
  /**
   * One point per label. `null` is no basis to compute for that point alone
   * and draws no bar; `0` is a measured zero and draws a bar of zero height.
   * §9b.4, applied per data point rather than per panel.
   */
  values?: (number | null)[] | null
}

export interface SeriesPayload {
  /** One unit for the whole panel (§3). A series carries none of its own. */
  unit?: string | null
  labels?: string[] | null
  series?: SeriesEntry[] | null
}
