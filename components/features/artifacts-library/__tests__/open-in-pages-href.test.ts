import { expect, it } from "vitest"
import { openInPagesHref, type LibraryItem } from "../library-items"

const project = { slug: "deck", pageId: "page-deck", projectBacked: true } as LibraryItem

// A shared-version link can fall back to Pages; the pin must survive, never Latest.
it("keeps the pinned version when a project falls back to Pages", () => {
  expect(openInPagesHref("ws1", project, 2)).toBe("/pages/%40page-deck?workspace_id=ws1&artifact=1&version=2")
})

it("opens Latest without a pin and ignores a pin on a plain page", () => {
  expect(openInPagesHref("ws1", project)).toBe("/pages/%40page-deck?workspace_id=ws1&artifact=1")
  expect(openInPagesHref("ws1", { slug: "note", projectBacked: false } as LibraryItem, 2)).toBe("/pages/note?workspace_id=ws1")
})
