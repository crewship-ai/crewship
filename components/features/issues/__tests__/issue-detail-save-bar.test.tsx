// Issue edits go through the page's one Save bar.
//
// Every property of an issue used to PATCH the moment it was picked — a
// misclick on "Done" fired the automations that react to it, and the
// description saved itself 1.5 s into a sentence. Now an edit is a draft:
// the bar counts it, one Save sends one PATCH, and a status that sets
// something off asks first.

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, act, cleanup } from "@testing-library/react"

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: [], loading: false }) }))
vi.mock("@/hooks/use-automations", () => ({
  useAutomations: () => ({
    automations: [{
      id: "au1", workspace_id: "ws1", name: "Send the invoice reminder", enabled: true,
      event_type: "mission.status_change", matcher: {}, action_kind: "pipeline", action: {},
      debounce_seconds: 0, max_per_hour: 0, created_at: "", updated_at: "",
    }],
  }),
}))
// The real editor calls onChange per keystroke and onBlur 1.5 s after the
// last one (its autosave); the double does the same.
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: (p: { content: string; onChange: (md: string) => void; onBlur?: () => void }) => (
    <textarea
      aria-label="Description"
      defaultValue={p.content}
      onChange={(e) => {
        p.onChange(e.target.value)
        setTimeout(() => p.onBlur?.(), 1500)
      }}
    />
  ),
}))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({ RunActivityTimeline: () => null, RUN_WORK_ENTRY_TYPES: ["exec"] }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { IssueDetailSurface } from "../issue-detail-surface"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"

const ISSUE = {
  id: "id-QUA-1", identifier: "QUA-1", title: "Review the overdue invoice", description: "Old text",
  status: "IN_PROGRESS", priority: "medium", crew_id: "crew-1",
  created_at: "2026-08-01T12:00:00Z", updated_at: "2026-08-01T12:00:00Z", labels: [],
}

let patches: Record<string, unknown>[] = []

beforeEach(() => {
  patches = []
  global.fetch = vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    if (init?.method === "PATCH") {
      patches.push(JSON.parse(String(init.body)))
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) } as unknown as Response)
    }
    if (/\/issues\/QUA-1\?/.test(u)) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(ISSUE) } as unknown as Response)
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]), headers: new Headers() } as unknown as Response)
  }) as unknown as typeof fetch
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

async function show() {
  render(
    <PageSaveProvider>
      <IssueDetailSurface workspaceId="ws1" identifier="QUA-1" />
      <PageSaveBar />
    </PageSaveProvider>,
  )
  await screen.findByRole("button", { name: "Change status" })
}
const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })
const pick = (picker: string, option: string) => {
  fireEvent.click(screen.getByRole("button", { name: picker }))
  fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${option}`) }))
}

describe("issue edits on the page Save bar", () => {
  it("stages status and priority as two unsaved changes, sending nothing", async () => {
    await show()
    pick("Change status", "Todo")
    pick("Change priority", "High")
    expect(bar()).toHaveTextContent("2 unsaved changes")
    expect(patches).toEqual([])
  })

  it("Save sends one PATCH with both fields", async () => {
    await show()
    pick("Change status", "Todo")
    pick("Change priority", "High")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(patches).toHaveLength(1))
    expect(patches[0]).toMatchObject({ status: "TODO", priority: "high" })
  })

  it("Done asks first and names the automation it sets off", async () => {
    await show()
    pick("Change status", "Done")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent("Send the invoice reminder")
    expect(patches).toEqual([])
    fireEvent.click(screen.getByRole("button", { name: /Mark done/ }))
    await waitFor(() => expect(patches).toEqual([{ status: "DONE" }]))
  })

  it("cancelling the question keeps the draft", async () => {
    await show()
    pick("Change status", "Done")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await screen.findByRole("alertdialog")
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(patches).toEqual([])
    expect(bar()).toHaveTextContent("1 unsaved change")
  })

  it("typing a description never saves on its own", async () => {
    await show()
    vi.useFakeTimers()
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "New text" } })
    act(() => { vi.advanceTimersByTime(2000) })
    vi.useRealTimers()
    expect(patches).toEqual([])
    expect(bar()).toHaveTextContent("1 unsaved change")
  })

  it("a title edited and left by clicking away stays as a draft", async () => {
    await show()
    fireEvent.click(screen.getByRole("button", { name: "Edit title" }))
    const input = screen.getByRole("textbox", { name: "Issue title" })
    fireEvent.change(input, { target: { value: "Review the overdue invoice today" } })
    fireEvent.blur(input)
    expect(screen.getByRole("heading", { name: "Review the overdue invoice today" })).toBeInTheDocument()
    expect(bar()).toHaveTextContent("1 unsaved change")
    expect(patches).toEqual([])
  })

  it("Discard puts the issue back", async () => {
    await show()
    pick("Change priority", "High")
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(bar()).toBeNull()
    expect(screen.getByRole("button", { name: "Change priority" })).toHaveTextContent("Medium")
  })
})
