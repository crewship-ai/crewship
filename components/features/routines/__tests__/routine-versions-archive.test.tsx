import { useState } from "react"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { describe, it, expect, vi } from "vitest"
import { RoutineVersionsTab } from "../routine-versions-tab"

const { fetcher } = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: () => useState<string | null>(null) }))
vi.mock("../routine-definition-canvas", () => ({ RoutineDefinitionCanvas: () => <div>Archived graph</div> }))
vi.mock("../routine-step-definition", () => ({ RoutineStepDefinition: () => null }))

describe("historical recipe inspection", () => {
  it("compares archives and prepares a draft without writing to the server", async () => {
    const definition = { steps: [{ id: "old-step", type: "transform" }] }
    fetcher.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.endsWith("/versions") ? [{ version: 2, is_head: true }, { version: 1 }] : url.includes("/diff?") ? { from_version: 1, to_version: 2, identical: false, unified_diff: "-old-step\n+new-step" } : { version: 1, definition } }))
    const draft = vi.fn()
    render(<RoutineVersionsTab workspaceId="ws" slug="recipe" onRolledBack={vi.fn()} onPrepareDraft={draft} />)
    fireEvent.click(await screen.findByRole("button", { name: "Compare with current" }))
    await screen.findByText("Changes from version 1 to version 2")
    fireEvent.click(screen.getByRole("button", { name: "Use as draft" }))
    expect(draft).toHaveBeenCalledWith(definition, 1)
    await waitFor(() => expect(fetcher.mock.calls.every(([, options]) => !options?.method || options.method === "GET")).toBe(true))
  })
})
