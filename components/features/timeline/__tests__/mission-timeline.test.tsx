import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { MissionTimeline } from "../mission-timeline"
import type { JournalEntry } from "@/lib/types/journal"

// The fork endpoint takes a CHECKPOINT id. cartographer.Create emits the
// checkpoint.created journal entry with the checkpoint id under `refs`
// (internal/cartographer/store.go), never under `payload` — so the timeline's
// `payload.checkpoint_id ?? entry.id` resolution always fell through to the
// JOURNAL entry id. Even against the correct URL that is a guaranteed 404.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

function checkpointEntry(over: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "jrn_00000000",
    workspace_id: "ws_1",
    ts: new Date().toISOString(),
    entry_type: "checkpoint.created",
    severity: "notice",
    actor_type: "user",
    summary: 'checkpoint "green build" @ cursor jrn_00000000',
    mission_id: "mis_source",
    refs: {
      checkpoint_id: "cp_real",
      journal_cursor: "jrn_00000000",
      mission_id: "mis_source",
    },
    payload: {},
    ...over,
  } as JournalEntry
}

function renderTimeline(entries: JournalEntry[]) {
  render(<MissionTimeline missionId="mis_source" entries={entries} loading={false} error={null} />)
}

// Radix opens on pointer events, not a bare click — same polyfill dance as
// components/features/admin/__tests__/keeper-governance-panel.test.tsx.
async function openForkDialog() {
  const trigger = screen.getByRole("button", { name: /actions/i })
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerId: 1 })
  fireEvent.pointerUp(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
  fireEvent.click(await screen.findByText(/fork from here/i))
}

beforeEach(() => {
  // happy-dom has no pointer capture; Radix calls into it on trigger press.
  // @ts-expect-error polyfill
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  // @ts-expect-error polyfill
  Element.prototype.setPointerCapture = vi.fn()
  // @ts-expect-error polyfill
  Element.prototype.releasePointerCapture = vi.fn()
  vi.clearAllMocks()
  apiFetch.mockResolvedValue({
    ok: true,
    status: 201,
    json: async () => ({ new_mission_id: "mis_fork", new_checkpoint_id: "cp_new" }),
  })
})
afterEach(() => {
  cleanup()
})

describe("MissionTimeline fork target", () => {
  it("forks the checkpoint id from refs, not the journal entry id", async () => {
    renderTimeline([checkpointEntry()])
    await openForkDialog()
    fireEvent.click(await screen.findByRole("button", { name: /^fork$/i }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(apiFetch.mock.calls[0][0]).toBe("/api/v1/checkpoints/cp_real/fork")
  })

  it("refuses to fork a checkpoint entry that carries no checkpoint id", async () => {
    renderTimeline([checkpointEntry({ refs: {}, payload: {} })])
    await openForkDialog()

    expect(await screen.findByRole("button", { name: /^fork$/i })).toBeDisabled()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
