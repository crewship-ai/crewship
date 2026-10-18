// ODCH-ADM-09: People, Security and Backups open only for an instance
// administrator, like the console itself. A workspace OWNER/ADMIN is told so
// and the page behind the gate is never mounted (so nothing is fetched).
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

const h = vi.hoisted(() => ({ admin: false as boolean | null, mounted: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => h.admin }))
vi.mock("@/components/features/admin/people/people-page", () => ({ PeoplePage: () => { h.mounted("people"); return <div>people body</div> } }))
vi.mock("@/components/features/admin/security/security-page", () => ({ SecurityPage: () => { h.mounted("security"); return <div>security body</div> } }))
vi.mock("@/components/features/admin/backups/backups-page", () => ({ BackupsPage: () => { h.mounted("backups"); return <div>backups body</div> } }))

import PeopleRoute from "../people/page"
import SecurityRoute from "../security/page"
import BackupsRoute from "../backups/page"

const routes: [string, () => React.ReactElement, string][] = [
  ["people", () => <PeopleRoute />, "people body"],
  ["security", () => <SecurityRoute />, "security body"],
  ["backups", () => <BackupsRoute />, "backups body"],
]

beforeEach(() => { cleanup(); h.mounted.mockReset() })

describe.each(routes)("/admin/%s", (name, route, body) => {
  it("tells a non-instance-admin they have no access and mounts nothing", () => {
    h.admin = false
    render(route())
    expect(screen.getByText(/instance administrator/i)).toBeInTheDocument()
    expect(screen.queryByText(body)).toBeNull()
    expect(h.mounted).not.toHaveBeenCalled()
  })
  it("waits while the session loads", () => {
    h.admin = null
    render(route())
    expect(screen.queryByText(body)).toBeNull()
    expect(h.mounted).not.toHaveBeenCalled()
  })
  it("shows the page to an instance admin", () => {
    h.admin = true
    render(route())
    expect(screen.getByText(body)).toBeInTheDocument()
  })
})
