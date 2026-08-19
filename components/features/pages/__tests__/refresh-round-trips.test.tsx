/**
 * `refresh:` survives every hop on the client (PRD `docs/prd/pages.md` §12 v1.1).
 *
 * This branch has now had the same bug four times — a panel field enumerated
 * by hand and dropped in ONE path, so the page saves and the field quietly
 * disappears. `refresh:` is the worst version of it: the field compiles to an
 * `automations` row server-side, so losing it on the way out of the editor
 * leaves a page that goes on LOOKING like it refreshes and silently stops
 * running the routine. Nothing in the UI would say so.
 *
 * So both client hops are pinned here: the wire → view normaliser, and the
 * editor's document ↔ wire translation in both directions.
 */
import { describe, it, expect } from "vitest"

import { toPanelView, type WirePanel } from "@/hooks/use-pages"
import { pageDocumentText, parsePageBuffer } from "@/components/features/pages/page-editor"

const REFRESHING_PANEL: WirePanel = {
  id: "incident",
  schema: "narrative.v1",
  owner: "crew/devops",
  producer: "routine/incident-rozbor",
  sla_seconds: 3600,
  span: 12,
  refresh: "on:wake",
}

describe("refresh reaches the view (§12 v1.1)", () => {
  it("carries the declared trigger onto the panel view", () => {
    expect(toPanelView(REFRESHING_PANEL).refresh).toBe("on:wake")
  })

  it("is null when the panel declares none, never undefined", () => {
    expect(toPanelView({ ...REFRESHING_PANEL, refresh: undefined }).refresh).toBeNull()
  })

  // §11b.14 fixes the sealed placeholder's shape. A trigger arriving beside one
  // is a serialisation bug, and the answer is the same one `actions` gets.
  it("is dropped on a sealed placeholder whatever the wire said", () => {
    const view = toPanelView({ panel_id: "incident", span: 12, sealed: true, refresh: "on:wake" })
    expect(view.refresh).toBeNull()
  })
})

describe("refresh survives the editor round trip (§10b.1)", () => {
  const page = {
    id: "cpage00000000000000001",
    slug: "fleet-201",
    name: "Flotila .201",
    panels: [REFRESHING_PANEL],
  }

  it("renders into the document a human edits", () => {
    const text = pageDocumentText(page)
    expect(text).toContain("refresh: on:wake")
  })

  it("comes back out of the buffer and onto the wire", () => {
    const parsed = parsePageBuffer(pageDocumentText(page))
    expect(parsed.ok).toBe(true)
    if (!parsed.ok) return
    expect(parsed.body.panels[0].refresh).toBe("on:wake")
  })

  // The regression in one assertion: a save must not be the thing that
  // disarms the page.
  it("is not dropped by a save that changed only the page name", () => {
    const text = pageDocumentText(page).replace("Flotila .201", "Flotila .202")
    const parsed = parsePageBuffer(text)
    expect(parsed.ok).toBe(true)
    if (!parsed.ok) return
    expect(parsed.body.panels[0].refresh).toBe("on:wake")
  })

  it("sends nothing for a panel that declares none", () => {
    const parsed = parsePageBuffer(
      pageDocumentText({ ...page, panels: [{ ...REFRESHING_PANEL, refresh: undefined }] }),
    )
    expect(parsed.ok).toBe(true)
    if (!parsed.ok) return
    expect(parsed.body.panels[0].refresh).toBeUndefined()
  })
})
