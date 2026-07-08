import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, cleanup } from "@testing-library/react"
import { AuxStatusSection } from "../sections/aux-status-section"

// Drive the component through its real fetch path with a stubbed
// apiFetch so we can assert the slot → description mapping renders for
// the *actual* backend slot ids (#866.4).
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

// Backend slot ids from internal/llm/aux.go.
const BACKEND_SLOTS = ["curator", "keeper", "behavior", "memory_health", "negative"]

function slotsResponse() {
  return {
    ok: true,
    status: 200,
    json: async () => ({
      slots: BACKEND_SLOTS.map((slot) => ({
        slot,
        provider: "anthropic",
        model: "claude-haiku-4-5",
        timeout_ms: 5000,
        source: "fallback" as const,
      })),
    }),
  }
}

describe("AuxStatusSection slot descriptions (#866.4)", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue(slotsResponse())
  })
  afterEach(() => cleanup())

  it("renders a non-empty description for every backend slot", async () => {
    render(<AuxStatusSection />)

    for (const slot of BACKEND_SLOTS) {
      const row = await waitFor(() => {
        const el = document.querySelector(`[data-testid="aux-slot-${slot}"]`)
        expect(el).toBeTruthy()
        return el as HTMLElement
      })
      // The description spans the full-width col-span-12 cell. If the key
      // map drifts from the backend slot ids, that cell renders empty.
      expect(row.textContent).toMatch(/[a-z]/i)
      const desc = row.querySelector(".col-span-12")
      expect(desc?.textContent?.trim().length ?? 0).toBeGreaterThan(0)
    }
  })

  it("shows the read-only diagnostic badge", async () => {
    render(<AuxStatusSection />)
    await waitFor(() => expect(screen.getByText(/^read-only$/i)).toBeTruthy())
  })
})
