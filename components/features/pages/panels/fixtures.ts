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
  NarrativePayload,
  PanelSnapshot,
  PanelSpec,
  SeriesPayload,
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

// ── narrative.v1 ──────────────────────────────────────────────────────────

const narrativePanel: PanelSpec = {
  id: "shrnuti",
  schema: "narrative.v1",
  title: "Daily read",
  owner: "crew/ucetni",
  span: 6,
  sla_seconds: 86400,
}

const narrativePayload: NarrativePayload = {
  verdict: "Two suppliers are late and one invoice is disputed.",
  blocks: [
    { kind: "paragraph", text: "The ledger closed at 14:00 with 128 invoices settled." },
    { kind: "paragraph", text: "Three remain open past their terms:" },
    { kind: "list", text: "FA-2026-0041, 12 days over" },
    { kind: "list", text: "FA-2026-0048, 4 days over" },
    { kind: "list", text: "FA-2026-0052, disputed by the supplier" },
  ],
}

export const narrativeFixtures = {
  fresh: {
    panel: narrativePanel,
    data: { state: "fresh", payload: narrativePayload, provenance: PROVENANCE_FRESH },
  },
  stale: {
    panel: narrativePanel,
    data: { state: "stale", payload: narrativePayload, provenance: PROVENANCE_STALE },
  },
  failed: {
    panel: narrativePanel,
    data: {
      state: "failed",
      payload: null,
      failure: "the reporting agent exceeded its context window",
      provenance: PROVENANCE_STALE,
    },
  },
  neverProduced: {
    panel: narrativePanel,
    data: { state: "never_produced" },
  },
  /** No conclusion. A verdict is prose, so its absence is not an em dash. */
  noVerdict: {
    panel: narrativePanel,
    data: {
      state: "fresh",
      payload: { blocks: [{ kind: "paragraph", text: "Nothing moved today." }] },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** The agent ran and had nothing to say. Measured, not missing. */
  emptyBlocks: {
    panel: narrativePanel,
    data: { state: "fresh", payload: { blocks: [] }, provenance: PROVENANCE_FRESH },
  },
  /**
   * §8 rule 3's permitted half: the payload names an entity by id and the
   * renderer builds the href from its own route table.
   */
  entityRefs: {
    panel: narrativePanel,
    data: {
      state: "fresh",
      payload: {
        verdict: "One item needs a decision.",
        blocks: [
          { kind: "paragraph", text: "Opened for review:", ref: { kind: "issue", id: "1935" } },
          { kind: "list", text: "Produced by", ref: { kind: "run", id: "run_8812" } },
        ],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  /**
   * §8 rules 1, 2, 3 and 10 as a fixture. Every one of these fields is refused
   * by the published schema at the API boundary, so it can only arrive from a
   * payload stored by an older build — and the renderer must still emit text,
   * never markup, never an image, never a link built from producer input.
   */
  hostilePayload: {
    panel: narrativePanel,
    data: {
      state: "fresh",
      payload: {
        blocks: [
          { kind: "paragraph", text: "<script>alert(1)</script> is five words here" },
          { kind: "image", text: "chart.png", src: "https://evil.example/x.png" },
          { kind: "paragraph", text: "elsewhere", href: "https://evil.example" },
          { kind: "paragraph", text: "escaped", ref: { kind: "issue", id: "../../admin" } },
          { kind: "paragraph", text: "unknown noun", ref: { kind: "webhook", id: "abc" } },
        ],
      } as NarrativePayload,
      provenance: PROVENANCE_FRESH,
    },
  },
} satisfies Record<string, { panel: PanelSpec; data: PanelSnapshot }>

// ── series.v1 ─────────────────────────────────────────────────────────────

const seriesPanel: PanelSpec = {
  id: "latence",
  schema: "series.v1",
  title: "Latency by day",
  owner: "crew/lookout",
  span: 8,
  sla_seconds: 3600,
}

const seriesPayload: SeriesPayload = {
  unit: "ms",
  labels: ["mon", "tue", "wed", "thu", "fri"],
  series: [
    { name: "api", values: [120, 134, 118, 141, 129] },
    { name: "worker", values: [80, 0, 95, 88, 91] }, // a measured zero on tue
  ],
}

export const seriesFixtures = {
  fresh: {
    panel: seriesPanel,
    data: { state: "fresh", payload: seriesPayload, provenance: PROVENANCE_FRESH },
  },
  stale: {
    panel: seriesPanel,
    data: { state: "stale", payload: seriesPayload, provenance: PROVENANCE_STALE },
  },
  failed: {
    panel: seriesPanel,
    data: {
      state: "failed",
      payload: null,
      failure: "latency-rollup could not reach the metrics store",
      provenance: PROVENANCE_STALE,
    },
  },
  neverProduced: {
    panel: seriesPanel,
    data: { state: "never_produced" },
  },
  /**
   * A gap and a measured zero in the same series. §9b.4 per data point: the
   * zero draws a bar, the gap draws an em dash and no bar.
   */
  gaps: {
    panel: seriesPanel,
    data: {
      state: "fresh",
      payload: {
        unit: "ms",
        labels: ["mon", "tue", "wed"],
        series: [{ name: "api", values: [120, null, 0] }],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** Nothing measured anywhere. Every point is an em dash; the legend stays. */
  allGaps: {
    panel: seriesPanel,
    data: {
      state: "fresh",
      payload: {
        unit: "ms",
        labels: ["mon", "tue"],
        series: [{ name: "api", values: [null, null] }],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** Five series: the palette's whole width, and one past the direct-label cut. */
  fiveSeries: {
    panel: seriesPanel,
    data: {
      state: "fresh",
      payload: {
        unit: "ks",
        labels: ["mon", "tue"],
        series: [
          { name: "ucetni", values: [3, 4] },
          { name: "lookout", values: [1, 2] },
          { name: "devops", values: [5, 6] },
          { name: "produkt", values: [2, 1] },
          { name: "podpora", values: [4, 3] },
        ],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** Seven series: §3's merge. Four keep their name, the rest become "other". */
  sevenSeries: {
    panel: seriesPanel,
    data: {
      state: "fresh",
      payload: {
        unit: "ks",
        labels: ["mon", "tue"],
        series: [
          { name: "ucetni", values: [1, 1] },
          { name: "lookout", values: [2, 2] },
          { name: "devops", values: [3, 3] },
          { name: "produkt", values: [4, 4] },
          { name: "podpora", values: [5, 5] },
          { name: "prodej", values: [6, 6] },
          { name: "pravni", values: [7, 7] },
        ],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** The same seven, reordered. Every colour must land where it did before. */
  sevenSeriesReordered: {
    panel: seriesPanel,
    data: {
      state: "fresh",
      payload: {
        unit: "ks",
        labels: ["mon", "tue"],
        series: [
          { name: "produkt", values: [4, 4] },
          { name: "devops", values: [3, 3] },
          { name: "lookout", values: [2, 2] },
          { name: "ucetni", values: [1, 1] },
          { name: "pravni", values: [7, 7] },
          { name: "prodej", values: [6, 6] },
          { name: "podpora", values: [5, 5] },
        ],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** A domain that crosses zero: the bars hang off a visible baseline. */
  negatives: {
    panel: seriesPanel,
    data: {
      state: "fresh",
      payload: {
        unit: "Kč",
        labels: ["Q1", "Q2", "Q3"],
        series: [{ name: "cash flow", values: [-40, 0, 60] }],
      },
      provenance: PROVENANCE_FRESH,
    },
  },
  /** A produced payload that describes nothing. Still not an em dash. */
  noLabels: {
    panel: seriesPanel,
    data: {
      state: "fresh",
      payload: { unit: "ms", labels: [], series: [] },
      provenance: PROVENANCE_FRESH,
    },
  },
} satisfies Record<string, { panel: PanelSpec; data: PanelSnapshot }>

// ── the sealed placeholder (§7.1 rule 2, §11b.14) ─────────────────────────

/**
 * A panel this viewer may not see. The server sends `{panel_id, span, sealed,
 * owner_crew_name}` and nothing else — so this fixture is the WHOLE panel, and
 * its emptiness is the fixture's point.
 */
export const sealedFixtures = {
  withCrew: {
    panel: {
      id: "mzdy",
      schema: "",
      span: 6,
      sealed: true,
      owner_crew_name: "Účetní",
    },
    data: { state: "never_produced" },
  },
  /** The crew name did not arrive. Still sealed; still not an error. */
  withoutCrew: {
    panel: { id: "mzdy", schema: "", span: 6, sealed: true, owner_crew_name: null },
    data: { state: "never_produced" },
  },
  /**
   * A panel with no schema that is NOT sealed. §11b.14 keys the renderer on
   * `sealed`, so this one is a serialisation bug and must still read as one.
   */
  unsealedWithoutSchema: {
    panel: { id: "rozbite", schema: "", span: 6 },
    data: { state: "never_produced" },
  },
} satisfies Record<string, { panel: PanelSpec; data: PanelSnapshot }>

/** Every fixture above, for the sweeps that must hold for all of them. */
export const PANEL_FIXTURES: PanelFixture[] = [
  ...Object.entries(metricFixtures).map(([name, f]) => ({ name: `metric.v1/${name}`, ...f })),
  ...Object.entries(seriesFixtures).map(([name, f]) => ({ name: `series.v1/${name}`, ...f })),
  ...Object.entries(statusFixtures).map(([name, f]) => ({ name: `status.v1/${name}`, ...f })),
  ...Object.entries(tableFixtures).map(([name, f]) => ({ name: `table.v1/${name}`, ...f })),
  ...Object.entries(narrativeFixtures).map(([name, f]) => ({ name: `narrative.v1/${name}`, ...f })),
  ...Object.entries(sealedFixtures).map(([name, f]) => ({ name: `sealed/${name}`, ...f })),
]
