import { afterEach, describe, expect, it } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
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
    expect(await screen.findByText(/Revision 3\. Saving is not available yet/)).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
    typeNotes("only here")
    expect(screen.getByText(/Local draft, not saved\..*Revision 3 on the server is unchanged/)).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
    expect(client.head().rev).toBe(3)
  })
  it("asks the browser to confirm leaving only while a local draft exists", async () => {
    render(<SlidesWorkspace pageId={PAGE} client={createFixtureSaveClient(head())} />)
    await screen.findByLabelText("Speaker notes")
    const leave = () => { const event = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(event); return event.defaultPrevented }
    expect(leave()).toBe(false)
    typeNotes("draft")
    expect(leave()).toBe(true)
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
    expect(client.head().rev).toBe(4)
  })
})
