import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

import { IssueOptionsMenu } from "../issue-options-menu"
import { apiFetch } from "@/lib/api-fetch"
import type { Mission } from "@/lib/types/mission"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const ISSUE = {
  id: "m1",
  identifier: "ENG-7",
  crew_id: "crew-1",
  title: "Rotate the staging token",
  description: "Goal: rotate it",
  priority: "high",
  status: "DONE",
  project_id: "p1",
  labels: [{ id: "l1", name: "infra", color: "#0aa", label_group: null }],
  owner: { id: "u1", name: "Pavel" },
  delegate: { id: "a1", name: "Scout" },
} as unknown as Mission

function open() {
  const trigger = screen.getByRole("button", { name: "Issue options" })
  fireEvent.keyDown(trigger, { key: "Enter" })
}

describe("IssueOptionsMenu: Duplicate issue", () => {
  beforeEach(() => vi.mocked(apiFetch).mockReset())

  it("creates a copy on the existing create call (title, brief, priority, project, labels) and opens it", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ ok: true, status: 201, json: async () => ({ identifier: "ENG-8" }) } as unknown as Response)
    const onDuplicated = vi.fn()
    render(<IssueOptionsMenu issue={ISSUE} projects={[]} workspaceId="ws-1" onChanged={vi.fn()} onDeleted={vi.fn()} onDuplicated={onDuplicated} />)
    open()
    fireEvent.click(await screen.findByText("Duplicate issue"))
    await waitFor(() => expect(onDuplicated).toHaveBeenCalledWith("ENG-8"))
    const [url, init] = vi.mocked(apiFetch).mock.calls[0]
    expect(String(url)).toContain("/api/v1/crews/crew-1/issues")
    expect(init?.method).toBe("POST")
    const body = JSON.parse(String(init?.body))
    expect(body).toMatchObject({ title: "Rotate the staging token (copy)", description: "Goal: rotate it", priority: "high", project_id: "p1", labels: ["l1"] })
    // A copy starts fresh: no status carried over, no owner or delegate copied.
    expect(body.status).toBeUndefined()
    expect(body.assignee_id).toBeUndefined()
  })

  it("says so when the copy fails, and does not navigate", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ ok: false, status: 500, json: async () => ({ detail: "boom" }) } as unknown as Response)
    const onDuplicated = vi.fn()
    render(<IssueOptionsMenu issue={ISSUE} projects={[]} workspaceId="ws-1" onChanged={vi.fn()} onDeleted={vi.fn()} onDuplicated={onDuplicated} />)
    open()
    fireEvent.click(await screen.findByText("Duplicate issue"))
    const { toast } = await import("@/lib/toast")
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("boom"))
    expect(onDuplicated).not.toHaveBeenCalled()
  })

  it("is not offered to someone who cannot create", () => {
    render(<IssueOptionsMenu issue={{ ...ISSUE, allowed_actions: ["comment"] } as Mission} projects={[]} workspaceId="ws-1" onChanged={vi.fn()} onDeleted={vi.fn()} onDuplicated={vi.fn()} />)
    open()
    expect(screen.queryByText("Duplicate issue")).toBeNull()
  })
})
