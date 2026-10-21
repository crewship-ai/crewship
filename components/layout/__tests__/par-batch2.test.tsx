import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"
import { Link2, Settings, Users } from "lucide-react"
import { readFileSync } from "node:fs"
import path from "node:path"

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })) }))

import { useAppStore } from "@/lib/store"
import { DrillPage, DrillNavItem } from "../drill-page"
import { PanelSpaces } from "../panel-spaces"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceField } from "../create-surface"

const ROOT = path.resolve(__dirname, "../../..")
const src = (f: string) => readFileSync(path.join(ROOT, f), "utf8")

beforeEach(() => {
  cleanup()
  act(() => useAppStore.getState().setSecondaryPanel(null))
})
afterEach(cleanup)

describe("DrillBack (PAR-105)", () => {
  it("is a plain row, not a bordered pill", () => {
    render(
      <DrillPage parent={{ href: "/settings", label: "Settings", icon: Settings }} title="Crew links" panelId="drill-x" icon={Link2}
        nav={<DrillNavItem label="Engineering" onSelect={() => {}} />}>
        <div>content</div>
      </DrillPage>,
    )
    const back = document.querySelector('[data-slot="drill-back"]') as HTMLElement
    expect(back).not.toBeNull()
    expect(back.className).not.toMatch(/(^|\s)border(\s|$)/)
    expect(back.className).not.toContain("shadow-xs")
    expect(back).toHaveTextContent("Settings")
  })
})

describe("PanelSpaces (PAR-108)", () => {
  const items = [{ key: "a", label: "Alpha", icon: Users, count: 3 }, { key: "b", label: "Beta", icon: Users }]
  it("sits in a card frame", () => {
    render(<PanelSpaces items={items} value="a" onChange={() => {}} />)
    const frame = document.querySelector('[data-slot="panel-spaces"]') as HTMLElement
    expect(frame.className).toContain("border")
    expect(frame.className).toContain("bg-card")
  })
  it("prints no numeric key chip beside the count, and still announces the key", () => {
    render(<PanelSpaces items={items} value="a" onChange={() => {}} />)
    expect(document.querySelector("kbd")).toBeNull()
    expect(screen.getByRole("tab", { name: /alpha/i })).toHaveAttribute("aria-keyshortcuts", "1")
  })
})

describe("CreateSurfaceField label (PAR-149)", () => {
  it("is sentence-case sans", () => {
    render(
      <CreateSurface open onOpenChange={() => {}}>
        <CreateSurfaceBody><CreateSurfaceField label="Secret name" htmlFor="x"><input id="x" /></CreateSurfaceField></CreateSurfaceBody>
      </CreateSurface>,
    )
    const label = screen.getByText("Secret name").closest("label") as HTMLElement
    expect(label.className).not.toMatch(/uppercase|font-mono/)
    expect(label.className).toContain("font-semibold")
  })
})

describe("shared legacy components use the kit conventions (PAR batch 2)", () => {
  const MONO_OR_CAPS = /font-mono|uppercase/
  it.each([
    "components/features/dashboard/dashboard-card.tsx",
    "components/features/dashboard/kpi-card.tsx",
    "components/features/dashboard/status-donut.tsx",
    "components/features/dashboard/legacy-overview.tsx",
    "components/features/inbox-v2/inbox-detail-frame.tsx",
  ])("%s has no mono or uppercase label/numeral", (f) => {
    const lines = src(f).split("\n").filter((l) => MONO_OR_CAPS.test(l) && !/<kbd/.test(l) && !/^\s*(\/\/|\*|\/\*|\{\/\*)/.test(l))
    expect(lines).toEqual([])
  })
  it("the Inbox position counter is sans tabular", () => {
    const line = src("components/features/inbox-v2/inbox-v2.tsx").split("\n").find((l) => l.includes("Item ${position + 1} of")) ?? ""
    expect(line).toContain("tabular-nums")
    expect(line).not.toContain("font-mono")
  })
  it("lists /issues/intake and /issues/manage as app routes", () => {
    const routes = src("lib/app-routes.ts")
    expect(routes).toContain('"/issues/intake"')
    expect(routes).toContain('"/issues/manage"')
  })
})
