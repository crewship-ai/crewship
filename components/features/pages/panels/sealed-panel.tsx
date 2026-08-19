"use client"

import * as React from "react"
import { Lock } from "lucide-react"

import { PanelFrame } from "./panel-frame"
import type { PanelProps } from "./types"

/**
 * The sealed placeholder — a panel that exists on this page and that THIS
 * viewer may not see (PRD §7.1 rule 2, §2.3, wire shape pinned in §11b.14).
 *
 * It is not a fallback and not an error. Everything below is a rendering of a
 * decision the server already made and already acted on: the panel arrives as
 * `{panel_id, span, sealed: true, owner_crew_name}` and nothing else — no
 * schema, no payload, no producer, no SLA — because per-panel filtering in
 * this product removes the data from the RESPONSE rather than hiding it in the
 * client (§2.3: the two ways to build per-panel permissions are not equal).
 * So there is nothing here to reveal; the component's whole job is to make the
 * absence legible.
 *
 * Two things it must do, and one it must not:
 *
 *  - **Hold its grid slot.** §2.3 argues at length that the page has the same
 *    shape for every viewer; a page that silently reflows per reader is a page
 *    two people cannot talk about. The frame is the same `SectionCard` every
 *    other panel uses and the caller gives it the same `span`.
 *  - **Name the owning crew.** The server takes trouble to send
 *    `owner_crew_name` precisely so the reader knows who to ask, and dropping
 *    it would waste the one useful fact in the placeholder.
 *  - **Not offer an instruction.** §9b.3 says empty states are instructions,
 *    but that rule assumes the reader can do something. Here they cannot, and
 *    "push a first payload" would be advice to someone with no access to the
 *    panel at all. There is no em dash either: `—` means "no basis to compute"
 *    (§9b.4), and this is not a missing measurement — it is a measurement that
 *    exists and is not ours to read.
 *
 * The registry routes here BEFORE it looks a schema up. §11b.14 is explicit:
 * *"The renderer keys on `sealed`, not on a missing field, so a serialisation
 * bug can never be mistaken for a permission decision."* The inverse is the
 * bug this component fixes: a sealed panel used to fall through to
 * `UnknownSchemaPanel` and tell the reader "this version of Crewship does not
 * render `` panels. Upgrade Crewship" — the exact Grafana failure mode §2.3
 * names, a dashboard that opens but whose panels fail inside it, and a lie
 * about the cause on top.
 */
export function SealedPanel({ panel, data, now, publicView, className }: PanelProps) {
  const crew = typeof panel.owner_crew_name === "string" ? panel.owner_crew_name.trim() : ""

  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={now}
      publicView={publicView}
      className={className}
      icon={Lock}
      // Not "no data yet": there is data, and its absence here is a permission
      // decision rather than a producer that has not run.
      statusWord="hidden"
      // The producer name and the run id are the internal vocabulary the seal
      // exists to withhold.
      showProvenance={false}
    >
      <p data-slot="panel-sealed" data-owner-crew={crew || undefined} className="type-page-value text-muted-foreground">
        {crew ? `Hidden · crew ${crew}` : "Hidden · owned by another crew"}
      </p>
    </PanelFrame>
  )
}
