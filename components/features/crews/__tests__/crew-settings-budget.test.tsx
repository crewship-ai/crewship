import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"

const role = vi.hoisted(() => ({ value: "ADMIN" }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: role.value, capabilities: [] }) }))
vi.mock("@/lib/api-fetch", async (orig) => ({
  ...(await orig<typeof import("@/lib/api-fetch")>()),
  apiFetch: vi.fn(async (url: string) =>
    String(url).includes("/budget")
      ? ({ ok: true, status: 200, json: async () => ({ crew_id: "c1", window: "month", limit_usd: null, spent_usd: 0, state: "none" }) } as Response)
      : ({ ok: true, status: 200, json: async () => [] } as Response)),
}))
vi.mock("../crew-canvas-tabs/settings-tab", () => ({ SettingsTab: () => <div>profile</div> }))
vi.mock("../crew-members", () => ({ CrewMembers: () => <div>members</div> }))
vi.mock("../crew-yaml", () => ({ CrewCommandLine: () => <div>cli</div>, crewCommands: () => [] }))

import { CrewSettings } from "../crew-settings"

afterEach(cleanup)
const crew = { id: "c1", name: "Research", slug: "research" } as never

describe("Settings › Budget", () => {
  it.each([["OWNER", true], ["ADMIN", true], ["MANAGER", false], ["MEMBER", false]])("%s sees the card: %s", async (r, shown) => {
    role.value = r
    render(<CrewSettings workspaceId="ws" crew={crew} agents={[]} patch={vi.fn()} onApplyAvatarStyle={vi.fn()} />)
    if (shown) expect(await screen.findByRole("region", { name: "Budget" })).toBeInTheDocument()
    else expect(screen.queryByRole("region", { name: "Budget" })).toBeNull()
  })
})
