import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"
import type { Notification } from "@/lib/types/mission"

import { NotificationBell } from "../notification-bell"

// The FYI surface of the top bar. It had been a 360px Radix dropdown with its
// own text-[9px]/[10px]/[11px] ladder, a badge that capped at 9+, a flat
// undivided list and no footer — beside a 380px Inbox that had a sectioned
// list, a 99+ badge and a two-action footer. These assertions pin the shared
// chrome (components/layout/bar-menu.tsx) as much as the data.

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-1" }) }))

vi.mock("next/link", () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: ({ href, children, ...rest }: any) => <a href={href} {...rest}>{children}</a>,
}))

const now = Date.now()

function notif(over: Partial<Notification> & Pick<Notification, "id">): Notification {
  return {
    actor_type: "agent",
    actor_id: "a1",
    actor_name: "casey",
    action: "commented",
    entity_type: "issue",
    entity_id: "i1",
    entity_title: "Fix the login redirect",
    read_at: null,
    created_at: new Date(now - 5 * 60_000).toISOString(),
    ...over,
  }
}

let LIST: Notification[] = []
let COUNT = 0

beforeEach(() => {
  LIST = []
  COUNT = 0
  vi.mocked(apiFetch).mockImplementation(async (url: string) => {
    if (url.includes("/notifications/count")) {
      return { ok: true, json: async () => ({ unread: COUNT }) } as unknown as Response
    }
    if (url.includes("read-all") || url.includes("/read")) {
      return { ok: true, json: async () => ({}) } as unknown as Response
    }
    return { ok: true, json: async () => LIST } as unknown as Response
  })
})

afterEach(cleanup)

async function open() {
  render(<NotificationBell />)
  fireEvent.click(screen.getByTestId("notifications-trigger"))
  await waitFor(() => expect(screen.getByTestId("notifications-popover")).toBeInTheDocument())
}

describe("<NotificationBell> badge", () => {
  it("hides at zero", async () => {
    render(<NotificationBell />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(screen.queryByTestId("notifications-badge")).not.toBeInTheDocument()
  })

  it("counts past nine instead of stopping at 9+", async () => {
    // The old bell capped at 9+, so an eleventh unread read "9+" beside an
    // Inbox that said "10" for the same kind of pile.
    COUNT = 11
    render(<NotificationBell />)
    await waitFor(() => expect(screen.getByTestId("notifications-badge")).toHaveTextContent("11"))
  })
})

describe("<NotificationBell> panel", () => {
  it("separates what is unread from what has been seen", async () => {
    COUNT = 1
    LIST = [
      notif({ id: "n1" }),
      notif({
        id: "n2",
        action: "completed",
        entity_title: "Nightly digest",
        read_at: new Date(now - 60_000).toISOString(),
      }),
    ]
    await open()

    const unread = await screen.findByTestId("bar-menu-section-unread")
    expect(within(unread).getByText(/Fix the login redirect/)).toBeInTheDocument()

    const earlier = screen.getByTestId("bar-menu-section-earlier")
    expect(within(earlier).getByText(/Nightly digest/)).toBeInTheDocument()
  })

  it("says who acted and what it was about on the row's meta line", async () => {
    COUNT = 1
    LIST = [notif({ id: "n1" })]
    await open()

    const row = await screen.findByTestId("notification-row-n1")
    expect(within(row).getByText(/casey/)).toBeInTheDocument()
    expect(within(row).getByText("issue")).toBeInTheDocument()
  })

  it("marks a row read when the row is clicked", async () => {
    COUNT = 1
    LIST = [notif({ id: "n1" })]
    await open()

    fireEvent.click(await screen.findByTestId("notification-row-n1"))

    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/notifications/n1/read"),
        expect.objectContaining({ method: "POST" }),
      ),
    )
  })

  it("offers Mark N read on the left of the footer and a way out on the right", async () => {
    COUNT = 2
    LIST = [notif({ id: "n1" }), notif({ id: "n2" })]
    await open()

    const footer = await screen.findByTestId("bar-menu-footer")
    fireEvent.click(within(footer).getByRole("button", { name: /Mark 2 read/ }))
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/notifications/read-all"),
        expect.objectContaining({ method: "POST" }),
      ),
    )

    expect(within(footer).getByRole("link", { name: /notification settings/i })).toHaveAttribute(
      "href",
      "/integrations?tab=notifications",
    )
  })

  it("states the empty case rather than rendering a blank panel", async () => {
    await open()
    expect(await screen.findByText("No notifications yet")).toBeInTheDocument()
  })
})
