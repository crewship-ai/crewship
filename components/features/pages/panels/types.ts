/**
 * Panel vocabulary — the closed set from PRD `docs/prd/pages.md` §3.
 *
 * The set is closed on purpose: a new panel kind is a server release, never a
 * user-supplied string. Everything downstream (the registry, the renderer)
 * treats `schema` as untrusted text and narrows it through `isPanelSchema`
 * before it is allowed to select a component.
 */

/** The five schemas, in the order §3 lists them. */
export const PANEL_SCHEMAS = [
  "metric.v1",
  "series.v1",
  "status.v1",
  "table.v1",
  "narrative.v1",
] as const

export type PanelSchema = (typeof PANEL_SCHEMAS)[number]

/**
 * The subset this build actually renders. `series.v1` and `narrative.v1` are
 * staged later (§12) but are part of the closed enum from the first migration,
 * so the registry carries an entry for them rather than pretending they are
 * unknown strings.
 */
export const IMPLEMENTED_PANEL_SCHEMAS = ["metric.v1", "status.v1", "table.v1"] as const

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

/** Server-attached, never producer-claimed (§4.5). */
export interface PanelProvenance {
  /** `routine/nightly-close`, `script/watch-services.sh`, … */
  producer?: string | null
  runId?: string | null
  /** ISO-8601, or anything `new Date()` parses. */
  producedAt?: string | Date | null
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
  slaSeconds?: number
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
  value?: number | string | null
  unit?: string | null
  delta?: number | null
  /**
   * Which direction is an improvement. Absent by default: §3 does not say
   * whether a rising number is good, and colouring a delta green because it
   * went up is a guess the panel is not entitled to make.
   */
  deltaGood?: "up" | "down" | null
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
