import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { IssuePeek, workHolder } from "../issue-peek"
import type { Mission } from "@/lib/types/mission"

const row = {
  id: "m1",
  identifier: "ENG-4",
  title: "Write the launch FAQ",
  status: "REVIEW",
  priority: "high",
  crew_name: "Research",
  owner: { id: "u1", name: "Pavel Srba" },
  delegate: { id: "a1", name: "Scout" },
  description: "Goal: answer the ten questions support gets most.",
  created_at: "",
  updated_at: "2026-10-16T10:00:00Z",
} as unknown as Mission

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
}

describe("IssuePeek", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockResolvedValue(json(200, { ...row, title: "Write the launch FAQ (fresh)" }))
  })

  it("shows the row at once, then the fresh copy, with owner and delegate apart", async () => {
    render(<IssuePeek open identifier="ENG-4" row={row} workspaceId="ws" onOpenChange={() => {}} onOpenFull={() => {}} />)
    expect(screen.getByText("Write the launch FAQ")).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText("Write the launch FAQ (fresh)")).toBeInTheDocument())
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/issues/ENG-4?workspace_id=ws")
    expect(screen.getByText("accountable person")).toBeInTheDocument()
    expect(screen.getByText("agent doing the work · Research crew")).toBeInTheDocument()
    expect(screen.getByText("Scout holds the work")).toBeInTheDocument()
    expect(screen.getByText(/answer the ten questions/)).toBeInTheDocument()
  })

  it("J/K moves, Enter opens full page, Esc closes", async () => {
    const onPrev = vi.fn()
    const onNext = vi.fn()
    const onOpenFull = vi.fn()
    const onOpenChange = vi.fn()
    render(<IssuePeek open identifier="ENG-4" row={row} workspaceId="ws" onOpenChange={onOpenChange} onPrev={onPrev} onNext={onNext} onOpenFull={onOpenFull} />)
    const sheet = screen.getByRole("dialog")
    fireEvent.keyDown(sheet, { key: "j" })
    expect(onNext).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(sheet, { key: "k" })
    expect(onPrev).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(sheet, { key: "Enter" })
    expect(onOpenFull).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole("button", { name: "Open full page (Enter)" }))
    expect(onOpenFull).toHaveBeenCalledTimes(2)
    fireEvent.keyDown(sheet, { key: "Escape" })
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it("says the issue is gone when the server does not know it", async () => {
    apiFetch.mockResolvedValue(json(404, { detail: "Issue not found" }))
    render(<IssuePeek open identifier="ENG-404" row={null} workspaceId="ws" onOpenChange={() => {}} onOpenFull={() => {}} />)
    await waitFor(() => expect(screen.getByText("Issue not found")).toBeInTheDocument())
  })
})

describe("workHolder", () => {
  it.each([
    [{ work_mode: "human", worker_user_id: "u2", worker_name: "Jana", work_revision: 3 }, "Jana holds the work", "person"],
    [{ delegate: { id: "a1", name: "Scout" }, crew_name: "Research" }, "Scout holds the work", "agent"],
    [{}, "Nobody holds the work", "nobody"],
  ])("%j", (over, title, kind) => {
    const h = workHolder({ ...row, delegate: null, owner: null, ...over } as unknown as Mission)
    expect(h.title).toBe(title)
    expect(h.kind).toBe(kind)
  })
})
