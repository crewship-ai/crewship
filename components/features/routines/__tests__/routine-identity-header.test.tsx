// The state line: published or only saved, and what Run starts.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { RoutineIdentityHeader, draftAuthorLabel } from "../routine-identity-header"

vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "usr_me" } }, status: "authenticated" }) }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({ useWorkspaceAgentDirectory: () => ({ agents: [], error: false }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(), broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))

const routine = { slug: "invoice-intake", name: "Invoice intake", description: "Reads an invoice.", head_version: 3 }

describe("<RoutineIdentityHeader> state line", () => {
  it("says Published vN and what Run uses when there is no draft", () => {
    render(<RoutineIdentityHeader routine={routine} workspaceId="ws" runUses onEdit={() => {}} onPublish={() => {}} />)
    const line = screen.getByTestId("routine-state-line")
    expect(line).toHaveTextContent("Published v3")
    expect(line).toHaveTextContent("Run uses v3")
    expect(screen.queryByTestId("routine-draft-pill")).toBeNull()
    expect(screen.queryByTestId("routine-publish-button")).toBeNull()
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument()
    expect(screen.getByText("Reads an invoice.")).toBeInTheDocument()
  })

  it("adds the draft pill with author and age, and the Publish button", () => {
    const publish = vi.fn()
    const updated_at = new Date(Date.now() - 12 * 60_000).toISOString()
    render(
      <RoutineIdentityHeader
        routine={{ ...routine, draft: { id: "drf_1", revision: 2, updated_at, updated_by: "usr_me" } }}
        workspaceId="ws"
        runUses
        onEdit={() => {}}
        onPublish={publish}
      />,
    )
    expect(screen.getByTestId("routine-draft-pill")).toHaveTextContent("Draft r2 · you · 12m ago")
    expect(screen.getByTestId("routine-state-line")).toHaveTextContent("Run uses v3")
    fireEvent.click(screen.getByRole("button", { name: "Publish draft r2" }))
    expect(publish).toHaveBeenCalled()
  })

  it("says nothing is published yet when there is no version", () => {
    render(<RoutineIdentityHeader routine={{ ...routine, head_version: undefined, draft: { id: "d", revision: 1, updated_at: new Date().toISOString(), updated_by: "usr_other" } }} workspaceId="ws" runUses />)
    const line = screen.getByTestId("routine-state-line")
    expect(line).toHaveTextContent("Not published")
    expect(line).toHaveTextContent("Run uses nothing yet — publish first")
    expect(screen.getByTestId("routine-draft-pill")).toHaveTextContent("usr_other")
  })

  it("orders the actions Run · Publish · Edit · menu", () => {
    render(
      <RoutineIdentityHeader
        routine={{ ...routine, draft: { id: "d", revision: 1, updated_at: new Date().toISOString() } }}
        workspaceId="ws"
        onEdit={() => {}}
        onPublish={() => {}}
        primary={<button type="button">Run</button>}
        actions={<button type="button">More</button>}
      />,
    )
    const names = screen.getAllByRole("button").map((b) => b.textContent?.trim()).filter((n) => n && n !== "Choose crew icon")
    expect(names.slice(-4)).toEqual(["Run", "Publish draft r1", "Edit", "More"])
  })

  it("labels the draft author", () => {
    expect(draftAuthorLabel("usr_me", "usr_me")).toBe("you")
    expect(draftAuthorLabel("usr_x", "usr_me")).toBe("usr_x")
    expect(draftAuthorLabel(undefined, "usr_me")).toBe("unknown")
  })
})
