"use client"

import * as React from "react"
import { ChartLine, CircleHelp, FileText, Frame, TriangleAlert } from "lucide-react"

import { PanelFrame } from "./panel-frame"
import type { PanelProps } from "./types"

/**
 * The schema string did not narrow to the closed enum. That is not an error
 * state of the *data* — the panel simply is not renderable here — so it does
 * not borrow the em dash, which means "no basis to compute a value".
 *
 * It never throws: an unknown schema is the expected outcome of an older
 * client meeting a newer page.
 */
export function UnknownSchemaPanel({ panel, data, now, publicView, className }: PanelProps) {
  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={now}
      publicView={publicView}
      className={className}
      icon={CircleHelp}
    >
      <p className="text-body text-muted-foreground">
        This version of Crewship does not render{" "}
        <code className="font-mono text-[12px] text-foreground">{String(panel.schema)}</code>{" "}
        panels. Upgrade Crewship, or change this panel to one of the schemas this build knows.
      </p>
    </PanelFrame>
  )
}

/**
 * A schema that is in the closed vocabulary but is not implemented in this
 * slice — `series.v1`, `narrative.v1` and `embed.v1` are staged later (§12).
 * Distinct from an unknown schema on purpose: the page is valid, the renderer
 * is behind. `embed.v1` in particular is reserved from the first migration
 * (§3.1) precisely so it never has to read as unknown.
 */
export function PendingSchemaPanel({ panel, data, now, publicView, className }: PanelProps) {
  const icon =
    panel.schema === "narrative.v1" ? FileText : panel.schema === "embed.v1" ? Frame : ChartLine
  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={now}
      publicView={publicView}
      className={className}
      icon={icon}
    >
      <p className="text-body text-muted-foreground">
        <code className="font-mono text-[12px] text-foreground">{String(panel.schema)}</code>{" "}
        panels arrive in a later release. The panel and its data are being kept — nothing pushed
        to it is lost.
      </p>
    </PanelFrame>
  )
}

/**
 * The boundary fallback. A panel payload is machine-written and may be
 * agent-written (§8): one malformed payload must cost its own panel, never
 * the page around it.
 */
export function PanelErrorPanel({ panel, data, now, publicView, className }: PanelProps) {
  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={now}
      publicView={publicView}
      className={className}
      icon={TriangleAlert}
    >
      <p className="text-body text-destructive">
        This panel could not be rendered from its latest payload. The rest of the page is
        unaffected; re-push the panel or check the producer&apos;s output shape.
      </p>
    </PanelFrame>
  )
}
