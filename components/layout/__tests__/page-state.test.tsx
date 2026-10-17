import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, act, cleanup } from "@testing-library/react"
import { Inbox } from "lucide-react"

import {
  PageEmpty, PageError, PageLoading, PageNoAccess, PageNoResults, PageNotFound,
  OfflineBanner, StaleData, SaveConflict,
} from "../page-state"

// Harbor 2 kit (#3366): the eight page states, one shape each.
afterEach(() => { cleanup(); vi.useRealTimers() })

describe("page states", () => {
  it("loading is a busy status; after 10 s it says so and offers Retry", () => {
    vi.useFakeTimers()
    const retry = vi.fn()
    render(<PageLoading onRetry={retry} />)
    const s = screen.getByRole("status")
    expect(s).toHaveAttribute("aria-busy", "true")
    act(() => { vi.advanceTimersByTime(9000) })
    expect(screen.queryByText("Still loading…")).toBeNull()
    act(() => { vi.advanceTimersByTime(1000) })
    expect(screen.getByText("Still loading…")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it("a slow load without a retry handler only says it is still loading", () => {
    vi.useFakeTimers()
    render(<PageLoading />)
    act(() => { vi.advanceTimersByTime(10000) })
    expect(screen.getByText("Still loading…")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  })

  it("empty names what belongs here, offers one primary action and the CLI", () => {
    const create = vi.fn()
    render(<PageEmpty icon={Inbox} title="Nothing waits for you" description="Decisions land here." primary={{ label: "Open Activity", onClick: create }} cli="crewship inbox" />)
    expect(screen.getByRole("heading", { name: "Nothing waits for you" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Open Activity" }))
    expect(create).toHaveBeenCalledOnce()
    expect(screen.getByText("crewship inbox")).toBeInTheDocument()
  })

  it("no results lists the filters and clears them", () => {
    const clear = vi.fn()
    render(<PageNoResults filters="Status: Failed · Last 7 days" onClear={clear} />)
    expect(screen.getByText("Status: Failed · Last 7 days")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
    expect(clear).toHaveBeenCalledOnce()
  })

  it("error is an alert with Retry and the request id", () => {
    const retry = vi.fn()
    render(<PageError title="Issues could not load" description="Your filters are kept." onRetry={retry} requestId="req_8f2c1a" />)
    expect(screen.getByRole("alert")).toHaveTextContent("Issues could not load")
    expect(screen.getByText("req_8f2c1a")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it("no access says which role is needed and who can grant it", () => {
    render(<PageNoAccess area="Journal" role="Viewer" needed="Admin" admins={["Pavel Srba"]} />)
    expect(screen.getByRole("heading", { name: "You can’t open Journal" })).toBeInTheDocument()
    expect(screen.getByText(/Journal needs Admin or above/)).toBeInTheDocument()
    expect(screen.getByText("Admins: Pavel Srba")).toBeInTheDocument()
  })

  it("not found offers a way back", () => {
    render(<PageNotFound what="issue" back={{ label: "Back to Issues", href: "/issues" }} />)
    expect(screen.getByRole("heading", { name: "This issue doesn’t exist" })).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Back to Issues" })).toHaveAttribute("href", "/issues")
  })

  it("offline is one banner and the data under it is dimmed and inert", () => {
    render(<><OfflineBanner since="14:02" /><StaleData stale><button type="button">Open</button></StaleData></>)
    expect(screen.getByRole("status")).toHaveTextContent("You see data from 14:02.")
    const wrapper = screen.getByText("Open").parentElement!
    expect(wrapper).toHaveClass("page-stale")
    expect(wrapper).toHaveAttribute("aria-disabled", "true")
  })

  it("save conflict shows both versions and resolves either way", () => {
    const mine = vi.fn(), theirs = vi.fn()
    render(<SaveConflict theirs="Thursdays 08:00" yours="Thursdays 07:30" onKeepMine={mine} onTakeTheirs={theirs} />)
    expect(screen.getByText("Thursdays 08:00")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Keep mine" }))
    fireEvent.click(screen.getByRole("button", { name: "Take theirs" }))
    expect(mine).toHaveBeenCalledOnce()
    expect(theirs).toHaveBeenCalledOnce()
  })
})
