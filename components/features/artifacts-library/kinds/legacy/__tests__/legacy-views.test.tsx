import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"

const load = vi.hoisted(() => ({ fn: vi.fn() }))
vi.mock("@/lib/slides/http-client", () => ({ createHttpSlidesClient: () => ({ load: load.fn }) }))
vi.mock("@/components/features/pages/page-slides", () => ({ PageSlides: ({ pageId }: { pageId: string }) => <div data-testid="slides-editor" data-page={pageId} /> }))
vi.mock("@/components/features/pages/workbook-editor-host", () => ({ PageWorkbookSurface: ({ pageId }: { pageId: string }) => <div data-testid="workbook" data-page={pageId} /> }))

import { LegacyDeckBody, LegacyWorkbookBody } from "../legacy-views"

afterEach(() => { cleanup(); vi.unstubAllEnvs(); load.fn.mockReset() })

it("shows a saved deck read-only while the Slides editor is not mounted: every slide, nothing editable", async () => {
  load.fn.mockResolvedValue({ deck: fixture.deck, rev: 1, orderVersion: 1, contentHash: "x" })
  render(<LegacyDeckBody workspaceId="ws1" pageId="page-deck" name="Q3 board deck" />)
  const deck = await screen.findByRole("region", { name: "Saved deck" })
  expect(load.fn).toHaveBeenCalledWith("page-deck")
  const count = fixture.deck.slide_order.length
  expect(deck.querySelectorAll("figure")).toHaveLength(count)
  expect(screen.getByText(`Slide 1 of ${count}`)).toBeTruthy()
  expect(screen.queryByRole("button")).toBeNull()
  expect(screen.queryByTestId("slides-editor")).toBeNull()
})

it.each<[string, unknown, RegExp]>([
  ["a server without Slides", { status: "unavailable", reason: "not_implemented" }, /not available on this server yet/],
  ["a refused read", { status: "unavailable", reason: "forbidden" }, /could not be loaded/],
])("says plainly what happened on %s, and changes nothing", async (_name, answer, message) => {
  load.fn.mockResolvedValue(answer)
  render(<LegacyDeckBody workspaceId="ws1" pageId="page-deck" name="Q3 board deck" />)
  expect(await screen.findByText(message)).toBeTruthy()
})

it("mounts the same Slides editor Pages mounted where its build switch is on", () => {
  vi.stubEnv("NEXT_PUBLIC_SLIDES_EDITOR", "true")
  render(<LegacyDeckBody workspaceId="ws1" pageId="page-deck" name="Q3 board deck" />)
  expect(screen.getByTestId("slides-editor").dataset.page).toBe("page-deck")
  expect(load.fn).not.toHaveBeenCalled()
})

it("hosts an older workbook's own surface", () => {
  render(<LegacyWorkbookBody workspaceId="ws1" pageId="page-model" name="Price model" />)
  expect(screen.getByTestId("workbook").dataset.page).toBe("page-model")
})
