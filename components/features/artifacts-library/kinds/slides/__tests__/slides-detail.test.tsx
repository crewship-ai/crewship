// A typed Slides deck inside the ArtifactFrame (Harbor 2 v87): slots, filmstrip, notes, grid, Present and the drawer.
vi.mock("@/lib/local-draft-seal", () => ({
  createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
  forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import fixture from "@/testdata/artifact-slides-projection.json"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import { slideLabel, SlidesBody } from "../slides-detail"

const state = vi.hoisted(() => ({
  projection: null as unknown,
  snapshot: null as ArtifactProjectSnapshot | null, comment: vi.fn(), restore: vi.fn(), share: vi.fn(), download: vi.fn(), viewing: [] as (number | undefined)[],
}))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "person" } } }) }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: true, dispatch: vi.fn(async () => true) }) }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({
  useArtifactProjectSnapshot: (_workspace: string, _slug: string, version?: number) => { state.viewing.push(version); return { data: state.snapshot, error: null, refetch: vi.fn() } },
  useArtifactProjectMutations: () => ({ restore: { mutate: state.restore, isPending: false }, share: { mutate: state.share, isPending: false }, comment: { mutate: state.comment, isPending: false } }),
}))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-slides", async () => {
  const { parseArtifactSlides } = await import("@/lib/pages/artifact-slides")
  return { useArtifactSlides: () => ({ scope: "s", enabled: true, projection: parseArtifactSlides(state.projection, "page"), error: false, downloadPDF: state.download }) }
})

function snapshot(over: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot {
  return {
    page_id: "page", artifact_kind: "slides", can_write: true, can_share: true, can_restore: true, can_comment: true, can_send_to_agent: false,
    latest_version: 2, shared_version: 1, origin: null,
    manifest: { version: 2, entrypoint: "slides/intro.html", files: [{ path: "deck.json", media_type: "application/json", content: "{}" }] },
    versions: [{ version: 1, message: "First draft" }, { version: 2, message: "Revenue slide" }], comments: [], ...over,
  }
}
const report = vi.fn()
async function mount(layout: FrameLayout = "desktop") {
  const view = render(<ArtifactFrame name="Q4 board update" kind={{ kind: "slides" }} caps={{ ...NO_FRAME_CAPABILITIES, agent: { ask: { chatHref: () => "/chat" } } }} onHome={vi.fn()} layout={layout}>
    <SlidesBody workspaceId="workspace" slug="q4" onFrame={report} />
  </ArtifactFrame>)
  await act(async () => {})
  return view
}
const slot = (name: string) => document.querySelector(`[data-artifact-slot="${name}"]`) as HTMLElement

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", "true")
  Object.defineProperty(document, "fonts", { configurable: true, value: { load: vi.fn(async () => [{ family: "Go Regular" }]), ready: Promise.resolve() } })
  state.snapshot = snapshot(); state.viewing = []; state.projection = fixture
  window.history.replaceState(null, "", "/artifacts/page?workspace_id=workspace")
  state.comment.mockReset(); state.restore.mockReset(); state.share.mockReset(); state.download.mockReset(); report.mockReset()
})
afterEach(() => { cleanup(); vi.unstubAllEnvs(); window.history.replaceState(null, "", "/") })

/** The fixture with a third slide, so "2 → 3" has somewhere to go. */
function threeSlides() {
  const deck = structuredClone(fixture.deck) as typeof fixture.deck & { slides: unknown[] }
  deck.slide_order = [...deck.slide_order, "closing"]
  deck.slides = [...deck.slides, { id: "closing", notes: "Thank the board", elements: [] }]
  return { ...fixture, deck }
}

describe("Slides in the artifact frame", () => {
  it("fills view, output, viewport and nav, and reports Saved with the server's version", async () => {
    await mount()
    expect(within(slot("view")).getByRole("button", { name: "Slide 1 of 2, choose a slide" })).toBeInTheDocument()
    expect(within(slot("output")).getByRole("button", { name: "Download" })).toBeInTheDocument()
    expect(within(slot("output")).getByRole("button", { name: "Present" })).toBeInTheDocument()
    expect(within(slot("viewport")).getAllByRole("button").map(button => button.getAttribute("aria-label"))).toEqual(["Zoom out", "Fit the slide", "Zoom in"])
    expect(within(slot("nav")).getAllByRole("button").map(button => button.getAttribute("aria-label"))).toEqual(["Speaker notes", "All slides", "Hide filmstrip"])
    expect(within(slot("nav")).getByText("1 / 2")).toBeInTheDocument()
    expect(report).toHaveBeenLastCalledWith({ canEdit: true, status: expect.objectContaining({ key: "saved", label: "Saved · v2" }) })
  })

  it("moves with the filmstrip, shows notes and the grid, and hides the filmstrip", async () => {
    await mount()
    const strip = screen.getByRole("navigation", { name: "Slide thumbnails" })
    fireEvent.click(within(strip).getByRole("button", { name: "Slide 2" }))
    expect(within(slot("nav")).getByText("2 / 2")).toBeInTheDocument()
    fireEvent.click(within(strip).getByRole("button", { name: "Slide 1" }))
    fireEvent.click(within(slot("nav")).getByRole("button", { name: "Speaker notes" }))
    expect(screen.getByRole("complementary", { name: "Speaker notes" })).toHaveTextContent("Opening note")
    fireEvent.click(within(slot("nav")).getByRole("button", { name: "Hide filmstrip" }))
    expect(screen.queryByRole("navigation", { name: "Slide thumbnails" })).toBeNull()
    fireEvent.click(within(slot("nav")).getByRole("button", { name: "All slides" }))
    const grid = screen.getByRole("list", { name: "All slides" })
    fireEvent.click(within(grid).getAllByRole("button")[1])
    expect(screen.queryByRole("list", { name: "All slides" })).toBeNull()
    expect(within(slot("nav")).getByText("2 / 2")).toBeInTheDocument()
  })

  it("presents full screen and returns", async () => {
    const request = vi.fn(async () => {})
    Object.defineProperty(document.documentElement, "requestFullscreen", { configurable: true, value: request })
    await mount()
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Present" }))
    expect(request).toHaveBeenCalledOnce()
    const dialog = screen.getByRole("dialog", { name: "Present slides" })
    fireEvent.keyDown(dialog, { key: "Escape" })
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("keeps the filmstrip above the phone's bottom controls and hides zoom", async () => {
    await mount("phone")
    expect(screen.getByRole("navigation", { name: "Slide thumbnails" }).className).toContain("mb-[7.5rem]")
    expect(slot("viewport").className).toContain("hidden")
    expect(slot("nav").className).toContain("bottom-[3.75rem]")
  })

  it("lets a reader read comments but not write, and offers no Restore or Share", async () => {
    state.snapshot = snapshot({ can_write: false, can_comment: false, can_restore: false, can_share: false, versions: [], comments: [{ id: "c1", version: 2, path: "slides/intro.html", selector: "#title", quote: "Revenue", text: "Bigger title" }] })
    await mount()
    expect(report).toHaveBeenLastCalledWith({ canEdit: false, status: expect.objectContaining({ key: "readonly" }) })
    fireEvent.click(screen.getByRole("button", { name: "Comments" }))
    expect(screen.getByText("Bigger title")).toBeInTheDocument()
    expect(screen.queryByRole("textbox", { name: "Comment on the slide" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(screen.queryByRole("button", { name: /Restore/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /Share/ })).toBeNull()
  })

  it("saves a comment on selected authored text through the existing route", async () => {
    await mount()
    fireEvent.click(screen.getByRole("button", { name: "Comments" }))
    fireEvent.click(screen.getByRole("button", { name: "Select on the slide" }))
    fireEvent.pointerDown(screen.getByRole("button", { name: "text 1" }))
    expect(screen.getByText("On “Revenue”")).toBeInTheDocument()
    const box = screen.getByRole("textbox", { name: "Comment on the slide" })
    await act(async () => {})
    fireEvent.change(box, { target: { value: "Make it bigger" } })
    fireEvent.click(screen.getByRole("button", { name: "Save comment" }))
    expect(state.comment).toHaveBeenCalledWith({ version: 2, path: "slides/intro.html", selector: "#title", quote: "Revenue", text: "Make it bigger", send_to_agent: false }, expect.anything())
  })

  it("registers Propose only when the deck may send to its agent chat", async () => {
    state.snapshot = snapshot({ can_send_to_agent: true, origin: { chat_id: "chat", agent_id: "agent" } })
    await mount()
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    expect(within(screen.getByRole("radiogroup", { name: "Agent mode" })).getAllByRole("radio").map(radio => radio.textContent)).toEqual(["Ask", "Propose"])
  })

  it("opens an older version from History and restores against the latest", async () => {
    await mount()
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    fireEvent.click(screen.getByRole("button", { name: "View" }))
    expect(state.viewing.at(-1)).toBe(1)
    fireEvent.click(screen.getByRole("button", { name: "Restore as new version" }))
    expect(state.restore).toHaveBeenCalledWith({ base_version: 2, version: 1 }, expect.anything())
  })

  it("says an unsupported deck is unsupported", async () => {
    state.snapshot = snapshot({ manifest: { version: 2, entrypoint: "x", files: [{ path: "deck.json", media_type: "application/json", content: "{}" }, { path: "dashboard.json", media_type: "application/json", content: "{}" }] } })
    await mount()
    expect(screen.getByRole("alert")).toHaveTextContent("This Slides format is unsupported.")
  })
})

describe("one selected slide for the viewer, its link and Present (#5150)", () => {
  beforeEach(() => { state.projection = threeSlides() })
  const shown = () => within(slot("nav")).getByText(/^\d+ \/ 3$/).textContent

  it("opens the slide the link names and keeps the link on the selection", async () => {
    window.history.replaceState(null, "", "/artifacts/page?workspace_id=workspace#slide-details")
    await mount()
    expect(shown()).toBe("2 / 3")
    fireEvent.click(within(screen.getByRole("navigation", { name: "Slide thumbnails" })).getByRole("button", { name: "Slide 3" }))
    expect(window.location.hash).toBe("#slide-closing")
    expect(window.location.search).toBe("?workspace_id=workspace")
    fireEvent.click(within(slot("nav")).getByRole("button", { name: "Speaker notes" }))
    expect(screen.getByRole("complementary", { name: "Speaker notes" })).toHaveTextContent("Speaker notes · slide 3Thank the board")
    fireEvent.click(within(slot("nav")).getByRole("button", { name: "All slides" }))
    expect(within(screen.getByRole("list", { name: "All slides" })).getAllByRole("button")[2]).toHaveAttribute("aria-current", "true")
    fireEvent.click(within(slot("nav")).getByRole("button", { name: "Single slide" }))
    expect(shown()).toBe("3 / 3")
  })

  it("ignores a link to a slide the deck does not have", async () => {
    window.history.replaceState(null, "", "/artifacts/page?workspace_id=workspace#slide-missing")
    await mount()
    expect(shown()).toBe("1 / 3")
  })

  it("follows back and forward between slide links", async () => {
    await mount()
    act(() => { window.history.replaceState(null, "", "#slide-closing"); window.dispatchEvent(new HashChangeEvent("hashchange")) })
    expect(shown()).toBe("3 / 3")
    act(() => { window.history.replaceState(null, "", "#slide-intro"); window.dispatchEvent(new PopStateEvent("popstate")) })
    expect(shown()).toBe("1 / 3")
  })

  it("moves with the arrow, page, Home and End keys in the viewer, not while typing", async () => {
    await mount()
    fireEvent.keyDown(document.body, { key: "ArrowRight" })
    expect(shown()).toBe("2 / 3")
    expect(window.location.hash).toBe("#slide-details")
    fireEvent.keyDown(document.body, { key: "PageDown" })
    fireEvent.keyDown(document.body, { key: "ArrowRight" })
    expect(shown()).toBe("3 / 3")
    fireEvent.keyDown(document.body, { key: "Home" })
    expect(shown()).toBe("1 / 3")
    fireEvent.keyDown(document.body, { key: "ArrowLeft" })
    expect(shown()).toBe("1 / 3")
    fireEvent.keyDown(document.body, { key: "End" })
    expect(shown()).toBe("3 / 3")
    fireEvent.keyDown(document.body, { key: "PageUp" })
    expect(shown()).toBe("2 / 3")
    fireEvent.keyDown(document.body, { key: "ArrowRight", metaKey: true })
    expect(shown()).toBe("2 / 3")
    fireEvent.click(screen.getByRole("button", { name: "Comments" }))
    const box = screen.getByRole("textbox", { name: "Comment on the slide" })
    fireEvent.keyDown(box, { key: "ArrowRight" })
    expect(shown()).toBe("2 / 3")
    // A button in the drawer keeps PageDown/End for scrolling its list.
    const select = screen.getByRole("button", { name: "Select on the slide" })
    expect(fireEvent.keyDown(select, { key: "End" })).toBe(true)
    expect(shown()).toBe("2 / 3")
    // The frame's slot buttons and the filmstrip are the deck's own controls.
    fireEvent.keyDown(within(slot("nav")).getByRole("button", { name: "All slides" }), { key: "ArrowRight" })
    expect(shown()).toBe("3 / 3")
  })

  it("leaves Present on the last slide shown: 2 → 3 → Escape stays on 3", async () => {
    await mount()
    fireEvent.click(within(screen.getByRole("navigation", { name: "Slide thumbnails" })).getByRole("button", { name: "Slide 2" }))
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Present" }))
    const dialog = screen.getByRole("dialog", { name: "Present slides" })
    expect(within(dialog).getByText("2 / 3")).toBeInTheDocument()
    fireEvent.keyDown(dialog, { key: "ArrowRight" })
    expect(within(dialog).getByText("3 / 3")).toBeInTheDocument()
    fireEvent.keyDown(dialog, { key: "Escape" })
    expect(screen.queryByRole("dialog")).toBeNull()
    expect(shown()).toBe("3 / 3")
    expect(window.location.hash).toBe("#slide-closing")
  })

  it("stops at the first and last slide in Present and while the viewer keys are idle", async () => {
    await mount()
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Present" }))
    const dialog = screen.getByRole("dialog", { name: "Present slides" })
    fireEvent.keyDown(dialog, { key: "ArrowLeft" })
    expect(within(dialog).getByText("1 / 3")).toBeInTheDocument()
    // One key, one step: the viewer's own keys do not move the slide under the presentation too.
    fireEvent.keyDown(dialog, { key: "ArrowRight" })
    expect(within(dialog).getByText("2 / 3")).toBeInTheDocument()
    expect(shown()).toBe("2 / 3")
    fireEvent.keyDown(dialog, { key: "End" })
    fireEvent.keyDown(dialog, { key: "ArrowRight" })
    expect(within(dialog).getByText("3 / 3")).toBeInTheDocument()
    expect(shown()).toBe("3 / 3")
    fireEvent.click(within(dialog).getByRole("button", { name: "Close presentation" }))
    expect(shown()).toBe("3 / 3")
  })

  it("ends Present on the shown slide when the browser leaves full screen", async () => {
    let element: Element | null = null
    Object.defineProperty(document, "fullscreenElement", { configurable: true, get: () => element })
    Object.defineProperty(document.documentElement, "requestFullscreen", { configurable: true, value: vi.fn(async () => { element = document.documentElement; document.dispatchEvent(new Event("fullscreenchange")) }) })
    try {
      await mount()
      await act(async () => { fireEvent.click(within(slot("output")).getByRole("button", { name: "Present" })) })
      const dialog = screen.getByRole("dialog", { name: "Present slides" })
      fireEvent.keyDown(dialog, { key: "ArrowRight" })
      act(() => { element = null; document.dispatchEvent(new Event("fullscreenchange")) })
      expect(screen.queryByRole("dialog")).toBeNull()
      expect(shown()).toBe("2 / 3")
    } finally {
      Reflect.deleteProperty(document, "fullscreenElement")
    }
  })
})

/**
 * Same-origin channels in one test. Like the browser's, delivery is asynchronous: messages wait
 * until `deliver()`, so several quick moves can be in flight at once.
 */
class FakeChannel {
  static open = new Set<FakeChannel>()
  static queue: { to: FakeChannel; data: unknown }[] = []
  static async deliver() {
    await act(async () => {
      while (FakeChannel.queue.length) {
        const { to, data } = FakeChannel.queue.shift()!
        if (FakeChannel.open.has(to)) { to.received.push(data); to.onmessage?.({ data } as MessageEvent) }
      }
    })
  }
  onmessage: ((event: MessageEvent) => void) | null = null
  received: unknown[] = []
  constructor(readonly name: string) { FakeChannel.open.add(this) }
  postMessage(data: unknown) {
    for (const peer of FakeChannel.open) if (peer !== this && peer.name === this.name) FakeChannel.queue.push({ to: peer, data: structuredClone(data) })
  }
  close() { FakeChannel.open.delete(this) }
}

describe("Presenter view in its own window (#5151)", () => {
  const SESSION = "0123456789abcdef01234567"
  let opened: ReturnType<typeof vi.fn>
  let popup: { opener: unknown }
  beforeEach(() => {
    state.projection = threeSlides()
    FakeChannel.open.clear(); FakeChannel.queue = []
    vi.stubGlobal("BroadcastChannel", FakeChannel)
    popup = { opener: window }
    opened = vi.fn(() => popup)
    vi.stubGlobal("open", opened)
  })
  afterEach(() => { vi.unstubAllGlobals() })
  const peer = (session = SESSION) => new FakeChannel(`crewship-slides-presenter:${session}`)
  const sessionOf = () => /presenter=([0-9a-f]{24})/.exec(opened.mock.calls[0][0] as string)![1]
  const slides = (channel: FakeChannel) => channel.received.filter(message => (message as { kind: string }).kind === "slide")

  it("opens the presenter window on the current slide and presents here without notes", async () => {
    await mount()
    fireEvent.click(within(screen.getByRole("navigation", { name: "Slide thumbnails" })).getByRole("button", { name: "Slide 2" }))
    // Notes were open in the viewer: the audience screen closes them.
    fireEvent.click(within(slot("nav")).getByRole("button", { name: "Speaker notes" }))
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Presenter view" }))
    expect(opened).toHaveBeenCalledOnce()
    const [url, target] = opened.mock.calls[0] as [string, string]
    expect(url).toMatch(/^\/artifacts\/page\?workspace_id=workspace&presenter=[0-9a-f]{24}#slide-details$/)
    expect(target).toMatch(/^crewship-presenter-[0-9a-f]{24}$/)
    expect(popup.opener).toBeNull()
    const dialog = screen.getByRole("dialog", { name: "Present slides" })
    expect(within(dialog).getByText("2 / 3")).toBeInTheDocument()
    expect(within(dialog).getByRole("button", { name: "Full screen" })).toBeInTheDocument()
    expect(screen.queryByRole("complementary", { name: "Speaker notes" })).toBeNull()
    expect(document.body).not.toHaveTextContent("Thank the board")
    expect(document.body).not.toHaveTextContent("Opening note")
  })

  it("says so when the browser blocks the presenter window, and does not present", async () => {
    opened.mockReturnValue(null)
    await mount()
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Presenter view" }))
    expect(screen.getByRole("alert")).toHaveTextContent("Your browser blocked the presenter window. Allow pop-ups for this site and try again.")
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("follows quick presenter moves without sending them back, and only its own session", async () => {
    await mount()
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Presenter view" }))
    const presenter = peer(sessionOf()), stranger = peer("ffffffffffffffffffffffff")
    await FakeChannel.deliver()
    presenter.received = []
    const dialog = screen.getByRole("dialog", { name: "Present slides" })
    // Two moves in flight before the audience hears either.
    presenter.postMessage({ kind: "slide", id: "details" })
    presenter.postMessage({ kind: "slide", id: "closing" })
    await FakeChannel.deliver()
    expect(within(dialog).getByText("3 / 3")).toBeInTheDocument()
    expect(slides(presenter)).toEqual([])
    presenter.postMessage({ kind: "slide", id: "missing" })
    stranger.postMessage({ kind: "slide", id: "intro" })
    await FakeChannel.deliver()
    expect(within(dialog).getByText("3 / 3")).toBeInTheDocument()
    // A move made here is sent once.
    fireEvent.keyDown(dialog, { key: "ArrowLeft" })
    await FakeChannel.deliver()
    expect(slides(presenter)).toEqual([{ kind: "slide", id: "details" }])
    fireEvent.keyDown(dialog, { key: "Escape" })
    await FakeChannel.deliver()
    expect(presenter.received.at(-1)).toEqual({ kind: "end" })
    expect(stranger.received).toEqual([])
    expect(shown3()).toBe("2 / 3")
  })

  it("ends here when the presenter window ends or closes, and a later Present starts where the viewer is", async () => {
    await mount()
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Presenter view" }))
    const presenter = peer(sessionOf())
    presenter.postMessage({ kind: "slide", id: "closing" })
    presenter.postMessage({ kind: "end" })
    await FakeChannel.deliver()
    expect(screen.queryByRole("dialog", { name: "Present slides" })).toBeNull()
    expect(shown3()).toBe("3 / 3")
    fireEvent.keyDown(document.body, { key: "Home" })
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Present" }))
    expect(within(screen.getByRole("dialog", { name: "Present slides" })).getByText("1 / 3")).toBeInTheDocument()
  })

  it("tells the presenter window when this window closes", async () => {
    await mount()
    fireEvent.click(within(slot("output")).getByRole("button", { name: "Presenter view" }))
    const presenter = peer(sessionOf())
    window.dispatchEvent(new Event("pagehide"))
    await FakeChannel.deliver()
    expect(presenter.received.at(-1)).toEqual({ kind: "end" })
  })

  it("is the presenter window: current and next slide, notes and timer, driving the audience", async () => {
    window.history.replaceState(null, "", `/artifacts/page?workspace_id=workspace&presenter=${SESSION}#slide-intro`)
    const audience = peer()
    await mount()
    const view = screen.getByRole("dialog", { name: "Presenter view" })
    expect(within(view).getByRole("region", { name: "Speaker notes" })).toHaveTextContent("Speaker notes · slide 1Opening note")
    expect(within(view).getByRole("region", { name: "Next slide" })).toBeInTheDocument()
    expect(within(view).getByLabelText("Elapsed time")).toHaveTextContent("00:00")
    fireEvent.click(within(view).getByRole("button", { name: "Next slide" }))
    await FakeChannel.deliver()
    expect(audience.received.at(-1)).toEqual({ kind: "slide", id: "details" })
    fireEvent.keyDown(view, { key: "End" })
    expect(within(view).getByRole("region", { name: "Speaker notes" })).toHaveTextContent("Thank the board")
    expect(within(view).getByRole("region", { name: "Next slide" })).toHaveTextContent("End of the deck.")
    audience.postMessage({ kind: "slide", id: "intro" })
    await FakeChannel.deliver()
    expect(within(view).getByText("1 / 3")).toBeInTheDocument()
    // Focus stays inside the view.
    const buttons = within(view).getAllByRole("button").filter(button => !(button as HTMLButtonElement).disabled)
    buttons.at(-1)!.focus()
    fireEvent.keyDown(buttons.at(-1)!, { key: "Tab" })
    expect(document.activeElement).toBe(buttons[0])
    audience.postMessage({ kind: "end" })
    await FakeChannel.deliver()
    expect(within(view).getByRole("status")).toHaveTextContent("The presentation ended. You can close this window.")
  })

  it("ignores a presenter address that is not a session", async () => {
    window.history.replaceState(null, "", "/artifacts/page?workspace_id=workspace&presenter=../x")
    await mount()
    expect(screen.queryByRole("dialog", { name: "Presenter view" })).toBeNull()
  })
  const shown3 = () => within(slot("nav")).getByText(/^\d+ \/ 3$/).textContent
})

it("names a slide by its first text", () => {
  expect(slideLabel({ id: "a", notes: "", elements: [{ id: "t", type: "text", x: 0, y: 0, width: 1, height: 1, text: { value: "Revenue\nmore", font_family: "Go Regular", font_size: 1, color: "#000", align: "left" } }] }, 0)).toBe("Revenue")
  expect(slideLabel({ id: "a", notes: "", elements: [] }, 2)).toBe("Slide 3")
})
