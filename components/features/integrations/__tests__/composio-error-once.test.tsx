// A failed inventory load printed the same red banner twice, one under the
// other. Once is the error; twice reads as two failures.

import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-err-once", loading: false }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async () => new Response("{}", { status: 502 })),
}))

import { ComposioIntegrations } from "../composio-integrations"

describe("ComposioIntegrations inventory error", () => {
  it("says it once", async () => {
    render(<ComposioIntegrations section="accounts" embedded />)
    await screen.findAllByText(/Couldn.t load Composio inventory/)
    expect(screen.getAllByText(/Couldn.t load Composio inventory/)).toHaveLength(1)
  })
})
