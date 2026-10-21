// COM-14: the routine page's save bar names the primary the way v67 does,
// "Save as draft" — the edits become a draft, nothing goes live.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { RoutinePage } from "../routine-page"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams("tab=settings"),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({ useWorkspaceAgentDirectory: () => ({ agents: [], error: false }) }))
vi.mock("@/components/features/settings/resource-share-dialog", () => ({ ResourceShareDialog: () => <button type="button">Share</button> }))
vi.mock("../routine-budget-card", () => ({ RoutineBudgetCard: () => <div /> }))
vi.mock("@/components/features/access/your-access", () => ({ YourAccess: () => null }))
vi.mock("../routine-reach-card", () => ({ RoutineReachCard: () => null }))

const routine = {
  id: "pipe-1", slug: "weekly", name: "Weekly", description: "Reads.", definition: { display_name: "Weekly", description: "Reads.", steps: [] },
  definition_hash: "h", dsl_version: "1.0", ephemeral: false, workspace_visible: true, invocation_count: 0, authored_via: "user_api",
  created_at: "", updated_at: "", head_version: 3, author_crew_id: "crew",
} as unknown as RoutineDetail

const json = (body: unknown) => ({ ok: true, status: 200, json: async () => body, clone() { return this }, text: async () => JSON.stringify(body) })

beforeEach(() => {
  h.fetcher.mockReset()
  h.fetcher.mockImplementation(async (url: string) => (url.endsWith("/weekly/draft") ? { ...json({}), ok: false, status: 404 } : json([])))
})

describe("RoutinePage save bar", () => {
  it("names the primary Save as draft", async () => {
    render(
      <RoutinePage
        routine={routine} workspaceId="ws" records={[]} lifecycle="active" canEdit canManage canRun
        onChanged={vi.fn()} onRun={vi.fn()} onOpenRun={vi.fn()} onGovernance={vi.fn()} onDelete={vi.fn()}
      />,
    )
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Weekly digest" } })
    const bar = within(screen.getByRole("region", { name: "Unsaved changes" }))
    expect(bar.getByRole("button", { name: "Save as draft" })).toBeInTheDocument()
  })
})
