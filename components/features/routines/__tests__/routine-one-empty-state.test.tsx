/**
 * RT-10: the routine tabs draw their empty states with the one layout
 * EmptyState (an h3 title), not the retired components/ui/detail one, and the
 * routines _shared module no longer re-exports it.
 */
import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/hooks/use-pipeline-webhooks", () => ({
  usePipelineWebhooks: () => ({
    webhooks: [], loading: false, error: null,
    refresh: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(),
  }),
}))

import { RoutineWebhooksTab } from "../routine-webhooks-tab"

afterEach(cleanup)

describe("routine empty states use the layout EmptyState", () => {
  it("the webhooks empty state title is a heading", () => {
    render(<RoutineWebhooksTab workspaceId="ws-1" pipelineId="p-1" slug="nightly" />)
    expect(screen.getByRole("heading", { name: "No webhooks yet" })).toBeTruthy()
  })

  it("_shared does not re-export EmptyState and no routine file imports it from ui/detail", () => {
    const dir = resolve(__dirname, "..")
    const shared = readFileSync(resolve(dir, "_shared.tsx"), "utf8")
    expect(shared).not.toMatch(/\bEmptyState\b/)
    for (const f of ["routine-webhooks-tab.tsx", "routine-runs-tab.tsx"]) {
      const src = readFileSync(resolve(dir, f), "utf8")
      expect(src, f).not.toMatch(/import\s*\{[^}]*\bEmptyState\b[^}]*\}\s*from\s*"\.\/_shared"/)
    }
  })
})
