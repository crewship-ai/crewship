import { useState } from "react"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { CalendarPage } from "../calendar-page"
import { ROUTINES, at } from "./fixtures"

const h = vi.hoisted(() => ({
  fetcher: vi.fn(),
  url: {} as Record<string, string | null>,
  pref: { scope: "workspace" } as { scope?: string },
  setPref: vi.fn(),
  mobile: false,
  role: "OWNER",
}))
vi.mock("@/lib/api-fetch", async (orig) => ({ ...(await orig<typeof import("@/lib/api-fetch")>()), apiFetch: h.fetcher }))
vi.mock("@/hooks/use-issue-detail", () => ({
  useUrlSelection: (key: string) => {
    const [v, setV] = useState<string | null>(h.url[key] ?? null)
    return [v, (next: string | null) => { h.url[key] = next; setV(next) }]
  },
}))
vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: (key: string, d: unknown) => {
    const [v, setV] = useState(key === "calendar.view" ? h.pref : d)
    return [v, (next: unknown) => { if (key === "calendar.view") h.setPref(next); setV(next) }, { ready: true }]
  },
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, capabilities: [] }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "me" } }, status: "authenticated" }) }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: ROUTINES, loading: false, error: null, refresh: vi.fn() }) }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("next/link", () => ({ default: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a> }))

type Reply = { ok: boolean; events?: unknown[]; truncated?: boolean }
function serve(calendar: (from: string, to: string) => Reply) {
  h.fetcher.mockImplementation(async (url: string) => {
    if (url.startsWith("/api/v1/crews")) return { ok: true, json: async () => [{ id: "ops", name: "Ops" }] }
    const q = new URL(url, "https://x.test").searchParams
    const r = calendar(q.get("from")!, q.get("to")!)
    return { ok: r.ok, status: r.ok ? 200 : 503, json: async () => ({ events: r.events ?? [], truncated: !!r.truncated }) }
  })
}
const calendarCalls = () => h.fetcher.mock.calls.filter(([u]) => String(u).includes("/pipelines/calendar"))

beforeEach(() => {
  h.fetcher.mockReset()
  h.url = { date: "2026-10-15" }
  h.pref = { scope: "workspace" }
  h.setPref.mockReset()
  h.mobile = false
  h.role = "OWNER"
})

describe("calendar-states", () => {
  it.each([
    ["empty", () => ({ ok: true }), "Nothing scheduled"],
    ["error", () => ({ ok: false }), "The calendar could not be loaded"],
    ["truncated", () => ({ ok: true, truncated: true, events: [{ id: "a", kind: "planned", at: at(2026, 10, 15, 9), slug: "scan" }] }), "Some dates in this range are left out"],
  ] as const)("%s", async (_name, reply, text) => {
    serve(reply)
    render(<CalendarPage workspaceId="ws" />)
    expect(await screen.findByText(new RegExp(text))).toBeInTheDocument()
  })

  it("loading shows the skeleton, not an empty calendar", () => {
    h.fetcher.mockImplementation(() => new Promise(() => {}))
    render(<CalendarPage workspaceId="ws" />)
    expect(document.querySelector('[data-state="loading"]')).not.toBeNull()
    expect(screen.queryByText("Nothing scheduled")).toBeNull()
  })

  it("error offers Retry, which asks again and keeps the view", async () => {
    let fail = true
    serve(() => (fail ? { ok: false } : { ok: true, events: [{ id: "a", kind: "planned", at: at(2026, 10, 15, 9), slug: "scan" }] }))
    h.url.view = "month"
    render(<CalendarPage workspaceId="ws" />)
    fail = false
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }))
    expect(await screen.findByRole("grid", { name: "Month" })).toBeInTheDocument()
  })

  it("nothing matches: names the filters and widens Mine to Workspace", async () => {
    h.pref = { scope: "mine" }
    serve(() => ({ ok: true, events: [{ id: "t", kind: "planned", at: at(2026, 10, 15, 9), slug: "triage" }] }))
    render(<CalendarPage workspaceId="ws" />)
    expect(await screen.findByText("Nothing matches these filters")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Show Workspace" }))
    expect(h.setPref).toHaveBeenCalledWith({ scope: "workspace" })
    expect(await screen.findByRole("grid", { name: "Week" })).toBeInTheDocument()
  })

  it("a six-week month loads in two bounded requests; a week in one", async () => {
    serve(() => ({ ok: true }))
    h.url = { view: "month", date: "2026-08-01" }
    const { unmount } = render(<CalendarPage workspaceId="ws" />)
    await waitFor(() => expect(calendarCalls()).toHaveLength(2))
    for (const [u] of calendarCalls()) {
      const q = new URL(u, "https://x.test").searchParams
      expect(Date.parse(q.get("to")!) - Date.parse(q.get("from")!)).toBeLessThanOrEqual(32 * 86_400_000)
    }
    unmount()
    h.fetcher.mockClear()
    h.url = { view: "week", date: "2026-10-15" }
    render(<CalendarPage workspaceId="ws" />)
    await waitFor(() => expect(calendarCalls()).toHaveLength(1))
  })

  it("heading, counts and need-you-soon come from the loaded range; switching view keeps the day", async () => {
    serve(() => ({
      ok: true,
      events: [
        { id: "w", kind: "run", status: "waiting", at: at(2026, 10, 14, 9), slug: "triage" },
        { id: "p", kind: "planned", at: at(2026, 10, 15, 8), slug: "scan" },
      ],
    }))
    render(<CalendarPage workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "Calendar" })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId("calendar-summary")).toHaveTextContent("Workspace · 2 dates you can open · 1 need you soon"))
    expect(screen.getByRole("list", { name: "Need you soon" })).toHaveTextContent("Nightly triage")
    expect(screen.getByTestId("calendar-range")).toHaveTextContent("12 – 18 October 2026")
    fireEvent.click(screen.getByRole("button", { name: "Month" }))
    expect(h.url).toMatchObject({ view: "month", date: "2026-10-15" })
    expect(screen.getByTestId("calendar-range")).toHaveTextContent("October 2026")
  })

  it("a phone opens on Agenda", async () => {
    h.mobile = true
    serve(() => ({ ok: true, events: [{ id: "p", kind: "planned", at: at(2026, 10, 15, 8), slug: "scan" }] }))
    render(<CalendarPage workspaceId="ws" />)
    expect(await screen.findByLabelText("Agenda")).toBeInTheDocument()
  })

  it("empty without the right to run offers no Schedule a start", async () => {
    h.role = "VIEWER"
    serve(() => ({ ok: true }))
    render(<CalendarPage workspaceId="ws" />)
    expect(await screen.findByText("Nothing scheduled")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Schedule a start/ })).toBeNull()
  })
})
