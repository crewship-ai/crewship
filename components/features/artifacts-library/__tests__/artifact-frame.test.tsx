import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { History } from "lucide-react"
import {
  ArtifactAgentMode, ArtifactDrawerSection, ArtifactFrame, ArtifactSlot, craftFrameStatus, NO_FRAME_CAPABILITIES,
  type CraftHostState, type FrameCapabilities, type FrameLayout,
} from "../frame"

afterEach(cleanup)

const caps = (over: Partial<FrameCapabilities> = {}): FrameCapabilities => ({ ...NO_FRAME_CAPABILITIES, ...over })
const ask = { ask: { chatHref: (prompt: string) => `/chat?page=brief&prompt=${encodeURIComponent(prompt)}&draft=1` } }

function frame(props: { caps?: FrameCapabilities; layout?: FrameLayout; children?: React.ReactNode; onShare?: () => void } = {}) {
  return render(<ArtifactFrame name="Partner brief" kind={{ kind: "word" }} folder="Demo Lab" caps={props.caps ?? caps()} onHome={vi.fn()}
    href="/artifacts/page-1" onShare={props.onShare} layout={props.layout ?? "desktop"} info={<p>info body</p>}>
    {props.children ?? <p>body</p>}
  </ArtifactFrame>)
}

describe("ArtifactFrame", () => {
  it("renders one header row: folder crumb, kind tile + name menu, status chip and Share", () => {
    frame({ caps: caps({ status: { key: "saving" } }), onShare: vi.fn() })
    expect(screen.getByText("Demo Lab")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Partner brief, artifact menu" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Status: Saving…" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Share" })).toBeTruthy()
    expect(screen.getByText("body")).toBeTruthy()
  })

  it("drops the folder crumb and shows only title and status on a phone", () => {
    frame({ caps: caps({ status: { key: "readonly" }, agent: ask }), layout: "phone", onShare: vi.fn() })
    expect(screen.queryByText("Demo Lab")).toBeNull()
    expect(screen.getByRole("button", { name: "Status: Read-only" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Share" })).toBeNull()
    expect(screen.getByRole("button", { name: "Agent" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Artifact menu" })).toBeTruthy()
  })

  it("hides an agent mode the backend does not support and disables one that needs edit access", () => {
    const { unmount } = frame({ caps: caps({ agent: ask }) })
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    // Ask only: no mode control at all, and Ask opens the existing chat with the artifact as context.
    expect(screen.queryByRole("radiogroup", { name: "Agent mode" })).toBeNull()
    fireEvent.change(screen.getByRole("textbox", { name: "What should the agent do?" }), { target: { value: "What changed?" } })
    expect(screen.getByRole("link", { name: "Ask in chat" }).getAttribute("href")).toBe("/chat?page=brief&prompt=What%20changed%3F&draft=1")
    unmount()

    frame({ caps: caps({ canEdit: false, agent: { ...ask, edit: { path: "test", send: vi.fn() } } }) })
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    const modes = screen.getByRole("radiogroup", { name: "Agent mode" })
    expect(within(modes).getAllByRole("radio").map(radio => radio.textContent)).toEqual(["Ask", "Edit"])
    expect(within(modes).getByRole("radio", { name: "Edit" })).toBeDisabled()
  })

  it.each([["word", "document"], ["docs", "document"], ["sheet", "sheet"], ["slides", "deck"], ["dashboard", "dashboard"], ["app", "app"], ["design", "design"]] as const)(
    "asks about a %s in plain English: this %s", (kind, noun) => {
      render(<ArtifactFrame name="Item" kind={{ kind }} caps={caps({ agent: ask })} onHome={vi.fn()} layout="desktop"><p>body</p></ArtifactFrame>)
      fireEvent.click(screen.getByRole("button", { name: "Agent" }))
      const prompt = screen.getByRole("textbox", { name: "What should the agent do?" })
      expect(prompt).toHaveAttribute("placeholder", `Ask about this ${noun}…`)
      expect(prompt.getAttribute("placeholder")).not.toMatch(/docs|word\b/)
    })

  it("hides the Agent tab when there is no agent path at all", () => {
    frame({ caps: caps({ agent: null }) })
    expect(screen.queryByRole("button", { name: "Agent" })).toBeNull()
  })

  it("shows a kind's agent form only while the body mounts it", () => {
    function Body({ admitted }: { admitted: boolean }) {
      return <>{admitted && <ArtifactAgentMode mode="edit"><button type="button">Start the craft task</button></ArtifactAgentMode>}</>
    }
    const view = (admitted: boolean) => <ArtifactFrame name="Brief" kind={{ kind: "word" }} caps={caps({ canEdit: true, agent: ask })} onHome={vi.fn()} layout="desktop"><Body admitted={admitted} /></ArtifactFrame>
    const { rerender } = render(view(true))
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Agent mode" })).getByRole("radio", { name: "Edit" }))
    expect(screen.getByRole("button", { name: "Start the craft task" })).toBeTruthy()
    rerender(view(false))
    expect(screen.queryByRole("radiogroup", { name: "Agent mode" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Start the craft task" })).toBeNull()
  })

  it("adds a History tab, header button and menu entry only when the body mounts history", () => {
    const { rerender } = frame()
    expect(screen.queryByRole("button", { name: "History" })).toBeNull()
    rerender(<ArtifactFrame name="Partner brief" kind={{ kind: "word" }} caps={caps()} onHome={vi.fn()} layout="desktop">
      <ArtifactDrawerSection tab="history" label="History" icon={History}><p>v3 · Latest</p></ArtifactDrawerSection>
    </ArtifactFrame>)
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    const drawer = screen.getByRole("complementary", { name: "History" })
    expect(within(drawer).getByText("v3 · Latest")).toBeTruthy()
    fireEvent.click(within(drawer).getByRole("button", { name: "Close panel" }))
    expect(screen.queryByRole("complementary", { name: "History" })).toBeNull()
  })

  it("places slots by purpose, and moves them for the phone", () => {
    const body = <>
      <ArtifactSlot name="view" label="View">v</ArtifactSlot>
      <ArtifactSlot name="tools" label="Tools">t</ArtifactSlot>
      <ArtifactSlot name="viewport" label="Zoom">z</ArtifactSlot>
      <ArtifactSlot name="panel" label="Layers">p</ArtifactSlot>
    </>
    const { unmount } = frame({ children: body })
    const layer = document.querySelector("[data-slot-layer]")!
    expect(layer.querySelector("[data-artifact-slot=view]")!.className).toContain("left-3 top-3")
    expect(layer.querySelector("[data-artifact-slot=tools]")!.className).toContain("left-1/2")
    expect(layer.querySelector("[data-artifact-slot=viewport]")!.className).toContain("right-3 bottom-3")
    unmount()
    frame({ children: body, layout: "phone" })
    const phone = document.querySelector("[data-slot-layer]")!
    expect(phone.querySelector("[data-slot-dock=top] [data-artifact-slot=view]")!.className).toContain("overflow-x-auto")
    expect(phone.querySelector("[data-artifact-slot=tools]")!.className).toContain("inset-x-2 bottom-2")
    expect(phone.querySelector("[data-artifact-slot=viewport]")!.className).toMatch(/(^| )hidden( |$)/)
    expect(phone.querySelector("[data-artifact-slot=panel]")!.className).toContain("bottom-0 h-[62%]")
  })

  it("keeps focus on what is being edited when a tool is pressed, so a phone's first tap is not lost (D01)", () => {
    frame({ layout: "phone", children: <>
      <textarea aria-label="Body" />
      <ArtifactSlot name="tools" label="Tools"><button type="button" onClick={vi.fn()}>Bold</button><input aria-label="Size" /></ArtifactSlot>
      <ArtifactSlot name="view" label="View"><button type="button">Mode</button></ArtifactSlot>
    </> })
    const tools = within(document.querySelector("[data-artifact-slot=tools]") as HTMLElement)
    // fireEvent returns false when the default (moving focus) was prevented.
    expect(fireEvent.mouseDown(tools.getByRole("button", { name: "Bold" }))).toBe(false)
    expect(fireEvent.mouseDown(tools.getByLabelText("Size"))).toBe(true)
    expect(fireEvent.mouseDown(screen.getByRole("button", { name: "Mode" }))).toBe(true)
  })

  it("docks output beside view at the top of a phone, so it never floats over the content", () => {
    frame({ layout: "phone", children: <>
      <ArtifactSlot name="output" label="Output">o</ArtifactSlot>
      <ArtifactSlot name="view" label="View">v</ArtifactSlot>
    </> })
    const dock = document.querySelector("[data-slot-layer] [data-slot-dock=top]")!
    expect(dock).toBeTruthy()
    const view = dock.querySelector("[data-artifact-slot=view]")!
    const output = dock.querySelector("[data-artifact-slot=output]")!
    // One row, view first: the output sits top right whatever order the body mounts them in.
    expect(view.compareDocumentPosition(output) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(output.className).not.toMatch(/(^| )bottom-/)
    expect(output.className).not.toMatch(/(^| )absolute( |$)/)
    expect(view.className).toContain("min-w-0 flex-1")
  })

  it.each([
    ["signedout", "Sign in to open this artifact"],
    ["denied", "You don’t have access"],
    ["missing", "This artifact doesn’t exist"],
  ] as const)("shows the %s gate instead of the content", (access, heading) => {
    frame({ caps: caps({ access, status: { key: "live" }, agent: ask }) })
    expect(screen.getByRole("heading", { name: heading })).toBeTruthy()
    expect(screen.queryByText("body")).toBeNull()
    expect(screen.queryByRole("button", { name: /^Status/ })).toBeNull()
    expect(screen.queryByRole("button", { name: "Agent" })).toBeNull()
  })

  it("keeps a disabled title-menu action visible with its reason", async () => {
    frame({ caps: caps({ actions: [{ id: "move", label: "Move to folder…", disabledReason: "Only the owner can move it" }] }) })
    await act(async () => {
      fireEvent.pointerDown(screen.getByRole("button", { name: "Partner brief, artifact menu" }), { button: 0, ctrlKey: false })
    })
    const item = (await screen.findByText("Move to folder…")).closest("[role=menuitem]")!
    expect(item.getAttribute("aria-disabled")).toBe("true")
    expect(within(item as HTMLElement).getByText("Only the owner can move it")).toBeTruthy()
  })
})

describe("craftFrameStatus", () => {
  const base: CraftHostState = { ready: true, dirty: false, canEdit: true, editing: true, holder: null, holderIsMe: true, openProposals: 0, recoveredDrafts: 0 }
  it.each<[string, Partial<CraftHostState>, string | null]>([
    ["nothing before the editor opens", { ready: false }, null],
    ["Saving… while the save is in flight", { phase: "saving", dirty: true }, "saving"],
    ["never Saved before the server acknowledged a version", { phase: "saved", savedVersion: undefined, dirty: true }, "unconfirmed"],
    ["Saved once acknowledged", { phase: "saved", savedVersion: "4" }, "saved"],
    ["held by another person", { editing: false, holderIsMe: false, holder: { kind: "human", name: "Jana" } }, "held"],
    ["an agent at work", { editing: false, holderIsMe: false, holder: { kind: "agent", name: "Scout" } }, "agent"],
    ["a proposal first", { openProposals: 1, phase: "saving" }, "proposal"],
    ["a recovered draft", { recoveredDrafts: 1 }, "draft"],
    ["read-only", { editing: false, canEdit: false }, "readonly"],
    ["free to take", { editing: false }, "free"],
  ])("%s", (_name, over, key) => {
    expect(craftFrameStatus({ ...base, ...over })?.key ?? null).toBe(key)
  })
  it("names who holds the lock", () => {
    expect(craftFrameStatus({ ...base, editing: false, holderIsMe: false, holder: { kind: "human", name: "Jana" } })?.label).toBe("Jana is editing")
  })
})
