/**
 * Panel fixtures — every schema in every freshness state.
 *
 * These are the shapes the API slice will serve (§3 payload cores, §4 states),
 * frozen here so the panels can be built and reviewed before that lands, and
 * so a later change to the wire shape breaks a test rather than a page.
 *
 * The clock is fixed: an absolute age is a computed number, and a test that
 * asserts "2 h 15 min old" must not depend on when it runs.
 */
import type {
  MetricPayload,
  PanelSnapshot,
  PanelSpec,
  StatusPayload,
  TablePayload,
} from "./types"

export interface PanelFixture {
  name: string
  panel: PanelSpec
  data: PanelSnapshot
}

/** Local-time constructors, so the fixtures are timezone-independent. */
export const FIXTURE_NOW = new Date(2026, 7, 12, 14, 55)
export const PRODUCED_FRESH = new Date(2026, 7, 12, 14, 54)
/** 2 h 15 min before FIXTURE_NOW. */
export const PRODUCED_STALE = new Date(2026, 7, 12, 12, 40)

/**
 * Wire names, not camelCase (§11b.4): `provenance: {producer, run_id,
 * produced_at}`. The fixtures are the shapes the API serves, so spelling them
 * any other way here would let a client that reads a field the server never
 * sends go on passing its own tests.
 */
const PROVENANCE_FRESH = {
  producer: "routine/nightly-close",
  run_id: "run_8812",
  produced_at: PRODUCED_FRESH,
}

const PROVENANCE_STALE = {
  producer: "routine/nightly-close",
  run_id: "run_8790",
  produced_at: PRODUCED_STALE,
}

// ── metric.v1 ─────────────────────────────────────────────────────────────

const metricPanel: PanelSpec = {
  id: "faktury",
  schema: "metric.v1",
  title: "Invoices closed",
  owner: "crew/finance",
  span: 4,
  sla_seconds: 3600,
}

const metricPayload: MetricPayload = {
  value: 128,
  unit: "invoices",
  delta: 12,
  target: 150,
  sparkline: [101, 97, 110, 118, 122, 115, 128],
}

export const metricFixtures = {
  fresh: {
    panel: metricPanel,
    data: { state: "fresh", payload: metricPayload, provenance: PROVENANCE_FRESH },
  },
  /** A measured zero. The whole point of §9b.4. */
  zero: {
    panel: metricPanel,
    data: {
      state: "fresh",
      payload: { value: 0, unit: "invoices", target: 150 },
      provenance: PROVENANCE_FRESH,
    },
  },
  stale: {
    panel: metricPanel,
    data: { state: "stale", payload: metricPayload, provenance: PROVENANCE_STALE },
  },
  failed: {
    panel: metricPanel,
    data: {
      state: "failed",
      payload: null,
      failure: "producer exited 1: nightly-close could not reach the ledger",
      provenance: PROVENANCE_STALE,
    },
  },
  neverProduced: {
    panel: metricPanel,
    data: { state: "never_produced" },
  },
  /** Produced, but the producer had no number to give. */
  noValue: {
    panel: metricPanel,
    data: {
      state: "fresh",
      payload: { value: null, unit: "invoices" },
      provenance: PROVENANCE_FRESH,
    },
  },
  /**
   * An empty string is a value the producer measured (§9b.4, and
   * `Cell.IsNoData()` in internal/pages/payload.go, which is true for JSON
   * null and nothing else). It is not the em dash.
   */
  emptyStringValue: {
    panel: metricPanel,
    data: {
      state: "fresh",
      payload: { value: "", unit: "invoices" },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** §11b.9: the producer says which way is good, so a rise may go green. */
  deltaGoodUp: {
    panel: metricPanel,
    data: {
      state: "fresh",
      payload: { value: 128, delta: 12, delta_good: "up" },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** The same rise on an error rate. Green here would be the lie §11b.9 names. */
  deltaGoodDown: {
    panel: metricPanel,
    data: {
      state: "fresh",
      payload: { value: 128, delta: 12, delta_good: "down" },
      provenance: PROVENANCE_FRESH,
    },
  },
  brokenSparkline: {
    panel: metricPanel,
    data: {
      state: "fresh",
      payload: {
        value: 5,
        sparkline: [Number.NaN, Number.POSITIVE_INFINITY, 3] as number[],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
} satisfies Record<string, { panel: PanelSpec; data: PanelSnapshot }>

// ── status.v1 ─────────────────────────────────────────────────────────────

const statusPanel: PanelSpec = {
  id: "sluzby",
  schema: "status.v1",
  title: "Services",
  owner: "crew/lookout",
  span: 8,
  sla_seconds: 30,
}

const statusPayload: StatusPayload = {
  items: [
    { name: "api", state: "critical", label: "502 on /v1/pages" },
    { name: "worker", state: "ok", label: "running" },
    { name: "db", state: "warning", label: "disk at 88 percent" },
  ],
}

export const statusFixtures = {
  fresh: {
    panel: statusPanel,
    data: { state: "fresh", payload: statusPayload, provenance: PROVENANCE_FRESH },
  },
  stale: {
    panel: statusPanel,
    data: { state: "stale", payload: statusPayload, provenance: PROVENANCE_STALE },
  },
  failed: {
    panel: statusPanel,
    data: {
      state: "failed",
      payload: null,
      failure: "watch-services.sh exited 137",
      provenance: PROVENANCE_STALE,
    },
  },
  neverProduced: {
    panel: statusPanel,
    data: { state: "never_produced" },
  },
  /** A payload is machine-written; it does not get to invent a fourth state. */
  unknownState: {
    panel: statusPanel,
    data: {
      state: "fresh",
      payload: { items: [{ name: "probe", state: "wobbly" }] },
      provenance: PROVENANCE_FRESH,
    },
  },
  emptyItems: {
    panel: statusPanel,
    data: { state: "fresh", payload: { items: [] }, provenance: PROVENANCE_FRESH },
  },
} satisfies Record<string, { panel: PanelSpec; data: PanelSnapshot }>

// ── table.v1 ──────────────────────────────────────────────────────────────

const tablePanel: PanelSpec = {
  id: "crews",
  schema: "table.v1",
  title: "Crews",
  owner: "crew/devops",
  span: 12,
  sla_seconds: 900,
}

const tablePayload: TablePayload = {
  columns: [
    { key: "crew", label: "Crew", align: "left" },
    { key: "open", label: "Open", align: "right" },
    { key: "closed", label: "Closed", align: "right" },
  ],
  rows: [
    { crew: "ucetni", open: 3, closed: 12 },
    { crew: "lookout", open: 0, closed: 4 }, // a measured zero
    { crew: "devops", open: null, closed: 7 }, // no basis for this cell
  ],
}

export const tableFixtures = {
  fresh: {
    panel: tablePanel,
    data: { state: "fresh", payload: tablePayload, provenance: PROVENANCE_FRESH },
  },
  stale: {
    panel: tablePanel,
    data: { state: "stale", payload: tablePayload, provenance: PROVENANCE_STALE },
  },
  failed: {
    panel: tablePanel,
    data: {
      state: "failed",
      payload: null,
      failure: "crew-rollup routine timed out after 60s",
      provenance: PROVENANCE_STALE,
    },
  },
  neverProduced: {
    panel: tablePanel,
    data: { state: "never_produced" },
  },
  positionalRows: {
    panel: tablePanel,
    data: {
      state: "fresh",
      payload: {
        columns: [
          { key: "crew", label: "Crew" },
          { key: "open", label: "Open", align: "right" },
        ],
        rows: [
          ["ucetni", 3],
          ["lookout", 0],
        ],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  emptyRows: {
    panel: tablePanel,
    data: {
      state: "fresh",
      payload: { columns: tablePayload.columns, rows: [] },
      provenance: PROVENANCE_FRESH,
    },
  },
  /**
   * One cell holds `""` and one holds `null`. The schema calls those two
   * different claims, and Go's `IsNoData()` agrees: only the null is an em
   * dash, and the empty string renders as an empty cell.
   */
  emptyStringCell: {
    panel: tablePanel,
    data: {
      state: "fresh",
      payload: {
        columns: tablePayload.columns,
        rows: [
          { crew: "", open: 0, closed: 4 },
          { crew: "devops", open: null, closed: 7 },
        ],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
} satisfies Record<string, { panel: PanelSpec; data: PanelSnapshot }>

/** Every fixture above, for the sweeps that must hold for all of them. */
export const PANEL_FIXTURES: PanelFixture[] = [
  ...Object.entries(metricFixtures).map(([name, f]) => ({ name: `metric.v1/${name}`, ...f })),
  ...Object.entries(statusFixtures).map(([name, f]) => ({ name: `status.v1/${name}`, ...f })),
  ...Object.entries(tableFixtures).map(([name, f]) => ({ name: `table.v1/${name}`, ...f })),
]
