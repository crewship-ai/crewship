import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, act } from "@testing-library/react"

import { ResourcesStrip } from "@/components/features/logs/resources-strip"
import { PREPEND_FLUSH_MS } from "@/hooks/use-batched-prepend"
import type { JournalEntry } from "@/lib/types/journal"

// The strip self-fetches, so both data hooks are stubbed: the list hands out
// a spy prependLive and a fixed buffer, the stream captures whatever onEntry
// the strip wires to it.
//
// The shared state lives in vi.hoisted because vi.mock factories are hoisted
// above every module-level declaration. The factories below only *close over*
// these bindings — they are read when the mocked hook is called, by which
// time a plain `const` would have initialised — but relying on that is a trap
// one edit away from a temporal-dead-zone crash, and vi.hoisted is the
// documented way to share state with a hoisted factory.
const mocks = vi.hoisted(() => ({
  prependLive: vi.fn(),
  capturedOnEntry: null as ((e: unknown) => void) | null,
  listEntries: [] as unknown[],
}))

vi.mock("@/hooks/use-journal-list", () => ({
  useJournalList: () => ({
    entries: mocks.listEntries,
    nextCursor: null,
    loading: false,
    loadingMore: false,
    error: null,
    refresh: vi.fn(),
    loadMore: vi.fn(),
    prependLive: mocks.prependLive,
  }),
}))

vi.mock("@/hooks/use-journal-stream", () => ({
  useJournalStream: (opts: { onEntry: (e: unknown) => void }) => {
    mocks.capturedOnEntry = opts.onEntry
    return { status: "connected", lastError: null, gapDetected: false, reconnect: vi.fn() }
  },
}))

function metric(id: string, cpu: number, offsetMs = 0): JournalEntry {
  return {
    id,
    workspace_id: "ws_test",
    crew_id: "crew_a",
    ts: new Date(Date.now() - offsetMs).toISOString(),
    entry_type: "container.metrics",
    severity: "info",
    actor_type: "system",
    summary: "metrics",
    payload: { cpu_pct: cpu, ram_pct: 40, net_rx: 100, net_tx: 100 },
  } as unknown as JournalEntry
}

beforeEach(() => {
  mocks.prependLive.mockClear()
  mocks.capturedOnEntry = null
  mocks.listEntries = []
  vi.useFakeTimers({ shouldAdvanceTime: true })
})

afterEach(() => {
  vi.useRealTimers()
})

describe("ResourcesStrip", () => {
  it("batches live metrics into one prepend instead of one per sample", () => {
    render(<ResourcesStrip workspaceId="ws_test" crewId="crew_a" />)
    expect(mocks.capturedOnEntry).toBeTypeOf("function")

    act(() => {
      for (let i = 0; i < 40; i++) mocks.capturedOnEntry!(metric(`m${i}`, i))
    })
    // container.metrics is the highest-volume entry type in the product;
    // the strip used to hand prependLive straight to onEntry, so this was
    // 40 state updates and 40 recharts re-renders.
    expect(mocks.prependLive).not.toHaveBeenCalled()

    act(() => {
      vi.advanceTimersByTime(PREPEND_FLUSH_MS)
    })
    expect(mocks.prependLive).toHaveBeenCalledTimes(1)
    const batch = mocks.prependLive.mock.calls[0][0]
    expect(Array.isArray(batch)).toBe(true)
    expect(batch).toHaveLength(40)
  })

  it("renders the four cells and the latest reading", () => {
    mocks.listEntries = [metric("m1", 12, 60_000), metric("m2", 37, 0)]
    render(<ResourcesStrip workspaceId="ws_test" crewId="crew_a" />)

    for (const label of ["CPU", "MEM", "NET", "DISK"]) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
    // Newest sample wins the readout.
    expect(screen.getByText("37%")).toBeInTheDocument()
  })

  it("does not subscribe without a workspace", () => {
    render(<ResourcesStrip workspaceId={null} />)
    act(() => {
      vi.advanceTimersByTime(PREPEND_FLUSH_MS * 2)
    })
    expect(mocks.prependLive).not.toHaveBeenCalled()
  })
})
