"use client"

import * as React from "react"
import type { ComponentType } from "react"
import { ErrorBoundary } from "react-error-boundary"

import { MetricPanel } from "./metric-panel"
import { NarrativePanel } from "./narrative-panel"
import { SeriesPanel } from "./series-panel"
import { StatusPanel } from "./status-panel"
import { TablePanel } from "./table-panel"
import { SealedPanel } from "./sealed-panel"
import { PanelErrorPanel, PendingSchemaPanel, UnknownSchemaPanel } from "./fallback-panel"
import { isPanelSchema, type PanelProps, type PanelSchema } from "./types"

/**
 * The dispatch table (PRD §9): validate, then look up a closed enum in a flat
 * map, then render. The same pattern Perses, Grafana Scenes and
 * react-jsonschema-form use.
 *
 * No `eval`. No dynamic `import()` of a user-supplied path. No
 * `dangerouslySetInnerHTML` — §8 rule 10, pinned by a test that reads this
 * directory's source. The only thing a page spec can do with its `schema`
 * field is select one of these components — and it cannot even reach
 * `Object.prototype`, because the narrowing goes through a Set, not an `in`
 * check.
 */
export const PANEL_REGISTRY: Record<PanelSchema, ComponentType<PanelProps>> = {
  "metric.v1": MetricPanel,
  "series.v1": SeriesPanel,
  "status.v1": StatusPanel,
  "table.v1": TablePanel,
  "narrative.v1": NarrativePanel,
  // Reserved from the first migration (§3.1) — a page may legitimately carry
  // one, so it gets the "arrives in a later release" copy, not "this version
  // does not render embed.v1". It needs a second origin and a sandbox proxy
  // rather than a payload type, which is why it is the last one out.
  "embed.v1": PendingSchemaPanel,
}

/** Untrusted string in, component out. Never throws, never returns undefined. */
export function resolvePanelComponent(schema: unknown): ComponentType<PanelProps> {
  return isPanelSchema(schema) ? PANEL_REGISTRY[schema] : UnknownSchemaPanel
}

/**
 * Renders one panel. The error boundary is the second half of "never throws":
 * the registry cannot pick a bad component, and a bad *payload* costs only its
 * own panel.
 *
 * The sealed check comes FIRST, ahead of the schema lookup, and §11b.14 says
 * why in as many words: *"The renderer keys on `sealed`, not on a missing
 * field, so a serialisation bug can never be mistaken for a permission
 * decision."* Both directions of that matter. A sealed panel carries no schema
 * — the server strips it — so leaving the lookup first sent every one of them
 * to `UnknownSchemaPanel` and told the reader "this version of Crewship does
 * not render `` panels. Upgrade Crewship", which is a lie about the cause and
 * is the Grafana failure mode §2.3 exists to avoid. And a panel with an empty
 * schema that is NOT sealed is a serialisation bug, still hits the fallback,
 * and must go on doing so.
 */
export function PanelRenderer(props: PanelProps) {
  const sealed = props.panel?.sealed === true
  const Component = sealed ? SealedPanel : resolvePanelComponent(props.panel?.schema)
  return (
    <ErrorBoundary
      resetKeys={[props.panel?.id, props.panel?.schema, props.data?.state, sealed]}
      fallbackRender={() => <PanelErrorPanel {...props} />}
    >
      <Component {...props} />
    </ErrorBoundary>
  )
}
