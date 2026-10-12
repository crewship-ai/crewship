import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { FilesTab } from "../files-tab"
import { MissionsTab } from "../missions-tab"
import type { CrewRecord, MissionData } from "../types"

const crew: CrewRecord = {
  id: "c", workspace_id: "w", name: "Research", slug: "research", description: null,
  color: null, icon: null, avatar_style: null, issue_prefix: null, network_mode: "restricted",
  allowed_domains: [], container_memory_mb: 2048, container_cpus: 1, container_ttl_hours: 24,
  runtime_image: null, devcontainer_config: null, mise_config: null, escalation_config: null,
  cached_image: null, created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z",
}
describe("crew mission and file panels", () => {
  it("opens the shared files panel only after an explicit user action", () => {
    const open = vi.fn()
    render(<FilesTab onOpenFiles={open} />)
    expect(screen.getByRole("heading", { name: "Crew files" })).toBeInTheDocument()
    expect(screen.getAllByText("/crew/shared")).toHaveLength(2)
    expect(open).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Open Files panel" }))
    expect(open).toHaveBeenCalledTimes(1)
  })
  it("distinguishes unavailable issue counts from zero and shows the empty mission state", () => {
    const { rerender } = render(<MissionsTab crew={crew} recentMissions={[]} recentIssues={[]} issues={null} />)
    expect(screen.getByText("No missions yet for this crew.")).toBeInTheDocument()
    expect(screen.getAllByText("—")).toHaveLength(5)
    rerender(<MissionsTab crew={{ ...crew, issue_prefix: "RSC" }} recentMissions={[]} recentIssues={[]} issues={{ Backlog: 0, Todo: 3, InProgress: 2, InReview: 0, Done: 7 }} />)
    expect(screen.getByText("RSC")).toBeInTheDocument()
    expect(screen.getAllByText("0")).toHaveLength(2)
    expect(screen.getByText("3")).toBeInTheDocument()
    expect(screen.queryByText("—")).not.toBeInTheDocument()
  })
  it("links each mission to its timeline and each identified issue to its encoded detail route", () => {
    const missions: MissionData[] = ["RUNNING", "FAILED", "AWAITING_REVIEW"].map((status, i) => ({ id: `mission/${i}`, title: `Mission ${i}`, status, crew_id: "c", created_at: "2026-10-01T00:00:00Z" }))
    render(<MissionsTab crew={crew} recentMissions={missions} issues={null} recentIssues={[
      { id: "i1", identifier: "RSC/1?", title: "Review evidence", status: "IN_REVIEW" },
      { id: "i2", identifier: null, title: "Unidentified issue", status: "TODO" },
    ]} />)
    for (let i = 0; i < 3; i++) {
      const link = screen.getByRole("link", { name: new RegExp(`Mission ${i}`) })
      expect(link).toHaveAttribute("href", `/missions/mission%2F${i}/timeline`)
      expect(within(link).getByText(new Date(missions[i].created_at).toLocaleDateString())).toBeInTheDocument()
    }
    expect(screen.getByText("awaiting review")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /Review evidence/ })).toHaveAttribute("href", "/issues/RSC%2F1%3F")
    expect(screen.getByRole("link", { name: /Unidentified issue/ })).toHaveAttribute("href", "/issues")
    expect(screen.queryByText("No missions yet for this crew.")).not.toBeInTheDocument()
  })
})
