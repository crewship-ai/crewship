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

// ── Restore says what the endpoint actually does ─────────────────────────
//
// POST /checkpoints/{id}/restore restores nothing. cartographer.Restore says
// so in its own doc comment — "no DB rows are mutated, no containers are torn
// down, no memory is rewound" — and journals the attempt as "restore preview
// for checkpoint …". The rewind is deferred to a handler that does not exist.
//
// The UI reported "Mission restored to checkpoint", and threw away the one
// thing the call does produce: warn_divergence, the events a real restore
// would have to abandon. Claiming success AND discarding the useful half was
// the worst available combination.
describe("<MissionTimeline> — restoring a checkpoint", () => {
  async function openMenu() {
    const trigger = screen.getByRole("button", { name: /actions/i })
    fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerId: 1 })
    fireEvent.pointerUp(trigger, { button: 0, pointerId: 1 })
    fireEvent.click(trigger)
  }

  it("calls it a preview, in the menu and in the result", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ warn_divergence: ["jrn_a", "jrn_b", "jrn_c"] }),
    })
    renderTimeline([checkpointEntry()])
    await openMenu()

    // Not "Restore" — it does not.
    const item = await screen.findByText(/preview restore/i)
    expect(screen.queryByText(/^Restore$/)).toBeNull()
    fireEvent.click(item)

    const { toast } = await import("sonner")
    await waitFor(() => expect(toast.info).toHaveBeenCalled())
    const [headline, opts] = (toast.info as unknown as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(headline).toMatch(/nothing has been rewound/i)
    // The divergence count is the payload's whole point.
    expect(opts.description).toMatch(/abandon 3 later events/i)
    expect(toast.success).not.toHaveBeenCalled()
  })

  it("says so even when there is nothing to abandon", async () => {
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ warn_divergence: [] }) })
    renderTimeline([checkpointEntry()])
    await openMenu()
    fireEvent.click(await screen.findByText(/preview restore/i))

    const { toast } = await import("sonner")
    await waitFor(() => expect(toast.info).toHaveBeenCalled())
    const [, opts] = (toast.info as unknown as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(opts.description).toMatch(/no later events/i)
    expect(opts.description).toMatch(/not implemented/i)
  })

  it("still reports a refusal as a refusal", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 404, json: async () => ({}) })
    renderTimeline([checkpointEntry()])
    await openMenu()
    fireEvent.click(await screen.findByText(/preview restore/i))

    const { toast } = await import("sonner")
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(toast.info).not.toHaveBeenCalled()
  })
})

