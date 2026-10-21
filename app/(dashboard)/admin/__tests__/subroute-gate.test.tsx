// ODCH-ADM-09: /admin/people, /admin/security and /admin/backups gate on the
// instance-admin check exactly like /admin does: a non-admin gets PageNoAccess
// and the page body is never mounted (so none of its APIs is called).
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

const h = vi.hoisted(() => ({ admin: null as boolean | null, wsLoading: false }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => h.admin }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws1", loading: h.wsLoading }) }))
vi.mock("@/components/features/admin/people/people-page", () => ({ PeoplePage: () => <div data-testid="body">people body</div> }))
vi.mock("@/components/features/admin/security/security-page", () => ({ SecurityPage: () => <div data-testid="body">security body</div> }))
vi.mock("@/components/features/admin/backups/backups-page", () => ({ BackupsPage: () => <div data-testid="body">backups body</div> }))

import PeoplePage from "../people/page"
import SecurityPage from "../security/page"
import BackupsPage from "../backups/page"

const routes = [
  ["people", PeoplePage],
  ["security", SecurityPage],
  ["backups", BackupsPage],
] as const

beforeEach(() => {
  cleanup()
  h.admin = false
  h.wsLoading = false
})

describe.each(routes)("/admin/%s", (_name, Page) => {
  it("shows the no-access state to a non-admin and mounts nothing of the page", () => {
    render(<Page />)
    expect(screen.getByText(/can.t open the Admin console/i)).toBeInTheDocument()
    expect(screen.queryByTestId("body")).toBeNull()
  })

  it("draws the page for an instance administrator", () => {
    h.admin = true
    render(<Page />)
    expect(screen.getByTestId("body")).toBeInTheDocument()
    expect(screen.queryByText(/can.t open/i)).toBeNull()
  })

  it("mounts nothing while the session is still loading", () => {
    h.admin = null
    render(<Page />)
    expect(screen.queryByTestId("body")).toBeNull()
    expect(screen.queryByText(/can.t open/i)).toBeNull()
  })
})
