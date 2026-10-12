import { fireEvent, render, screen, within } from "@testing-library/react"
import { Folder, Users } from "lucide-react"
import { describe, expect, it, vi } from "vitest"
import { PageShell } from "@/components/layout/page-shell"
import { PropertyRow } from "@/components/layout/property-row"
import { SectionCard } from "@/components/ui/section-card"
import { Progress } from "@/components/ui/progress"
import { CrewDangerZone } from "@/components/features/crews/crew-danger-zone"
import { CrewStats } from "@/components/features/crews/crew-stats"
import { CrewNameSlugFields } from "@/components/features/crews/crew-name-slug-fields"
import { ScopeSection } from "@/components/features/chat/files/scope-section"
import { PresenceDot, type PresenceStatus } from "@/components/features/chat/messages/presence-dot"

describe("page layout composition", () => {
  it("renders heading, toolbar, content, actions and both numeric and textual statistics", () => {
    const onCreate = vi.fn()
    render(<PageShell title="Crews" description="Choose a team" actions={<button onClick={onCreate}>Create crew</button>}
      toolbar={<label>Search crews<input /></label>} stats={[
        { title: "Agents", value: 0, subtitle: "Available", icon: Users },
        { title: "Health", value: "Ready", subtitle: "No incidents", icon: Users, iconClassName: "custom-icon", animatedIcon: <span>Healthy icon</span> },
      ]}><p>Team directory</p></PageShell>)
    expect(screen.getByRole("heading", { name: "Crews" })).toBeInTheDocument()
    for (const text of ["Choose a team", "Team directory", "Agents", "0", "Ready", "No incidents", "Healthy icon"]) expect(screen.getByText(text)).toBeInTheDocument()
    expect(screen.getByLabelText("Search crews")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Create crew" }))
    expect(onCreate).toHaveBeenCalledOnce()
  })

  it.each(["default", "compact", "unpadded"])("preserves consumer attributes and %s spacing without optional slots", (density) => {
    render(<PageShell title="Empty" description="No teams" data-testid="shell" className="consumer-shell"
      density={density === "compact" ? "compact" : undefined} unpadded={density === "unpadded"}
      stats={density === "compact" ? [] : undefined}><p>Empty directory</p></PageShell>)
    const shell = screen.getByTestId("shell")
    expect(shell).toHaveClass("consumer-shell")
    if (density === "unpadded") expect(shell).not.toHaveClass("p-4", "p-6")
    else expect(shell).toHaveClass(density === "compact" ? "p-4" : "p-6")
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
    expect(screen.getByText("Empty directory")).toBeInTheDocument()
  })

  it.each([false, true])("shows a property value with optional icon and action: %s", (extra) => {
    const action = vi.fn()
    render(<PropertyRow label="Owner" icon={extra ? Users : undefined} action={extra ? <button onClick={action}>Change owner</button> : undefined} data-testid="row">Alice</PropertyRow>)
    expect(within(screen.getByTestId("row")).getByText("Owner")).toBeInTheDocument()
    expect(screen.getByText("Alice")).toBeInTheDocument()
    if (extra) { fireEvent.click(screen.getByRole("button", { name: "Change owner" })); expect(action).toHaveBeenCalledOnce() }
    else expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })

  it.each(["none", "title", "description", "actions"])("keeps a section usable with only %s in its header", (slot) => {
    render(<SectionCard title={slot === "title" ? "Team" : undefined} description={slot === "description" ? "Members" : undefined}
      actions={slot === "actions" ? <button>Invite</button> : undefined} bare={slot === "none"} surface={slot === "description" ? "subtle" : "card"}>Section content</SectionCard>)
    expect(screen.getByText("Section content")).toBeInTheDocument()
    if (slot === "title") expect(screen.getByText("Team")).toBeInTheDocument()
    if (slot === "description") expect(screen.getByText("Members")).toBeInTheDocument()
    if (slot === "actions") expect(screen.getByRole("button", { name: "Invite" })).toBeInTheDocument()
  })

  it.each([{ value: undefined, expected: 0 }, { value: -10, expected: 0 }, { value: 25, expected: 25 }, { value: 140, expected: 100 }])("exposes bounded progress for $value", ({ value, expected }) => {
    render(<Progress aria-label="Upload progress" value={value} indicatorClassName={value === 25 ? "custom-fill" : undefined} />)
    expect(screen.getByRole("progressbar", { name: "Upload progress" })).toHaveAttribute("aria-valuenow", String(expected))
  })
})

describe("crew settings interactions", () => {
  it("requires confirmation to delete and preserves the crew after cancelling", () => {
    const onDelete = vi.fn()
    render(<CrewDangerZone crewName="Research" onDelete={onDelete} />)
    fireEvent.click(screen.getByRole("button", { name: "Delete Crew" }))
    const dialog = screen.getByRole("alertdialog")
    expect(within(dialog).getByRole("heading", { name: 'Delete "Research"?' })).toBeInTheDocument()
    expect(onDelete).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
    expect(onDelete).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Delete Crew" }))
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Delete Crew" }))
    expect(onDelete).toHaveBeenCalledOnce()
  })

  it.each([false, true])("keeps labels associated and marks manual slug edits with custom ids=%s", (custom) => {
    const onNameChange = vi.fn(), onSlugChange = vi.fn(), onSlugManualEdit = vi.fn()
    render(<CrewNameSlugFields name="Research" slug="research" onNameChange={onNameChange} onSlugChange={onSlugChange} onSlugManualEdit={onSlugManualEdit}
      nameId={custom ? "crew-name" : undefined} slugId={custom ? "crew-slug" : undefined} namePlaceholder="Team name" />)
    const name = screen.getByLabelText("Name *"), slug = screen.getByLabelText("Slug")
    expect(name).toHaveValue("Research"); expect(slug).toHaveValue("research")
    fireEvent.change(name, { target: { value: "Data Team" } })
    expect(onNameChange).toHaveBeenCalledWith("Data Team")
    expect(onSlugManualEdit).not.toHaveBeenCalled()
    fireEvent.change(slug, { target: { value: "Data Team!!" } })
    expect(onSlugManualEdit).toHaveBeenCalledOnce()
    expect(onSlugChange).toHaveBeenCalledWith("data-team")
  })

  it("updates agent and member counts including zero", () => {
    const { rerender } = render(<CrewStats agentCount={0} memberCount={7} />)
    expect(screen.getByText("Agents")).toBeInTheDocument(); expect(screen.getByText("0")).toBeInTheDocument()
    expect(screen.getByText("Members")).toBeInTheDocument(); expect(screen.getByText("7")).toBeInTheDocument()
    rerender(<CrewStats agentCount={3} memberCount={1} />)
    expect(screen.queryByText("0")).not.toBeInTheDocument()
    expect(screen.getByText("3")).toBeInTheDocument(); expect(screen.getByText("1")).toBeInTheDocument()
  })
})

describe("file scopes and presence", () => {
  it("opens a collapsed file scope and exposes its count and badge", () => {
    render(<ScopeSection icon={Folder} title="Project files" count={0} badge={<span>Read only</span>} defaultOpen={false}>No files uploaded</ScopeSection>)
    const toggle = screen.getByRole("button", { name: /Project files/ })
    expect(toggle).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByText("No files uploaded")).not.toBeInTheDocument()
    expect(screen.getByText("0")).toBeInTheDocument(); expect(screen.getByText("Read only")).toBeInTheDocument()
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByText("No files uploaded")).toBeInTheDocument()
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute("aria-expanded", "false")
  })

  it("defaults to expanded without count or badge", () => {
    render(<ScopeSection icon={Folder} title="Files">report.txt</ScopeSection>)
    expect(screen.getByRole("button", { name: "Files" })).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByText("report.txt")).toBeInTheDocument()
  })

  it.each(["online", "busy", "blocked", "offline"] as PresenceStatus[])("provides a text label for %s presence", (status) => {
    const label = status.charAt(0).toUpperCase() + status.slice(1)
    const { rerender } = render(<PresenceDot status={status} />)
    expect(screen.getByLabelText(label)).toHaveAttribute("title", label)
    rerender(<PresenceDot status={status} pulse={false} className="static-presence" />)
    expect(screen.getByLabelText(label)).toHaveClass("static-presence")
  })
})
