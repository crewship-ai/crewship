import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "@/lib/slides/model"
import { createFixtureSaveClient } from "@/lib/slides/save-fixture"
import type { SlidesSaveClient } from "@/lib/slides/save-client"
import { SlidesWorkspace } from "../slides-workspace"

afterEach(cleanup)
const PAGE = "workspace_page"
const head = (deckId = PAGE) => ({ deck: { ...structuredClone(fixture.deck), deck_id: deckId } as SlideDeck, rev: 3, orderVersion: 1 })
const typeNotes = (value: string) => fireEvent.change(screen.getByLabelText("Speaker notes"), { target: { value } })

describe("SlidesWorkspace without a save route", () => {
  it("labels edits as a local draft and offers no Save button", async () => {
    const client = createFixtureSaveClient(head())
    render(<SlidesWorkspace pageId={PAGE} client={client} />)
    await screen.findByLabelText("Speaker notes")
    expect(screen.getByRole("status").textContent).toContain("Revision 3.")
    // The unchanging warning is visible but not a live region; only the changing part is.
    const warning = screen.getByText(/Saving is not available yet.*lost on reload/)
    expect(warning.closest("[role=status]")).toBeNull()
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
    typeNotes("only here")
    expect(screen.getByRole("status").textContent).toContain("Local draft, not saved. Revision 3 is the version you opened.")
    expect(screen.getByRole("status").textContent).not.toContain("lost on reload")
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
    expect(client.head().rev).toBe(3)
  })
  it("asks the browser to confirm leaving only while a local draft exists", async () => {
    render(<SlidesWorkspace pageId={PAGE} client={createFixtureSaveClient(head())} />)
    await screen.findByLabelText("Speaker notes")
    const leave = () => { const event = new Event("beforeunload", { cancelable: true }) as BeforeUnloadEvent; window.dispatchEvent(event); return event.defaultPrevented && event.returnValue === "" }
    expect(leave()).toBe(false)
    typeNotes("draft")
    expect(leave()).toBe(true)
  })
  it("reports a dirty draft to the host and clears it on unmount", async () => {
    const dirty = vi.fn()
    const view = render(<SlidesWorkspace pageId={PAGE} client={createFixtureSaveClient(head())} onDirtyChange={dirty} />)
    const original = (await screen.findByLabelText("Speaker notes") as HTMLTextAreaElement).value
    expect(dirty).not.toHaveBeenLastCalledWith(true)
    typeNotes("draft")
    expect(dirty).toHaveBeenLastCalledWith(true)
    typeNotes(original)
    expect(dirty).toHaveBeenLastCalledWith(false)
    typeNotes("again")
    view.unmount()
    expect(dirty).toHaveBeenLastCalledWith(false)
  })
  it("words a local draft with the Document durability states, never as saved (SX-02)", async () => {
    render(<SlidesWorkspace pageId={PAGE} client={createFixtureSaveClient(head())} />)
    await screen.findByLabelText("Speaker notes")
    expect(screen.getByRole("status").textContent).not.toContain("·")
    typeNotes("only here")
    expect(screen.getByRole("status").textContent).toContain("Not stored in this browser or on the server · keep this tab open")
    expect(screen.getByRole("status").textContent).not.toMatch(/saved as|confirmed by server/i)
  })
  it("shows an error state, not a deck, when the deck belongs to another Page", async () => {
    const client: SlidesSaveClient = { load: async () => ({ status: "error", retryable: false, message: "The presentation does not belong to this Page." }), patch: async () => ({ status: "unavailable", reason: "not_implemented" }) }
    render(<SlidesWorkspace pageId={PAGE} client={client} />)
    expect((await screen.findByRole("alert")).textContent).toContain("does not belong to this Page")
    expect(screen.queryByLabelText("Speaker notes")).toBeNull()
  })
})

describe("SlidesWorkspace save against the in-memory contract fixture", () => {
  it("saves only when the caller enables it and shows the acknowledged revision", async () => {
    const client = createFixtureSaveClient(head())
    render(<SlidesWorkspace pageId={PAGE} client={client} saveEnabled />)
    await screen.findByLabelText("Speaker notes")
    typeNotes("saved notes")
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    expect(await screen.findByText(/Saved as revision 4/)).toBeTruthy()
    expect(screen.getByRole("status").textContent).toContain("Confirmed by server")
    expect(client.head().rev).toBe(4)
  })
  it("never shows a save without a receipt for this mutation as saved", async () => {
    const server = createFixtureSaveClient(head())
    const client: SlidesSaveClient = { load: server.load, patch: async request => ({ ...(await server.patch(request)), mutationId: "m_other" }) as Awaited<ReturnType<SlidesSaveClient["patch"]>> }
    render(<SlidesWorkspace pageId={PAGE} client={client} saveEnabled />)
    await screen.findByLabelText("Speaker notes")
    typeNotes("unconfirmed")
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Save changes" })) })
    expect((await screen.findByRole("alert")).textContent).toContain("could not be confirmed")
    expect(screen.getByRole("status").textContent).not.toMatch(/Saved as revision/)
    expect(screen.getByRole("status").textContent).toContain("Not stored in this browser or on the server")
    expect((screen.getByLabelText("Speaker notes") as HTMLTextAreaElement).value).toBe("unconfirmed")
  })
})
