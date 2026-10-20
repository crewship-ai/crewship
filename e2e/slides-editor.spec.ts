import { expect, test, type Page, type Route } from "@playwright/test"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import type { SlideDeck } from "../lib/slides/model"

// Backend contract (decision F5-7, public PR #3837): Slides has Read and Initialize only, behind
// CREWSHIP_SLIDES_ENABLED. There is no PATCH route, so these flows are about the read and about an
// honest local draft; nothing here claims that an editor save works against the real API. The read
// is served through page.route in the exact backend body (`{schema_version, revision, order_version,
// content_hash, deck}`, no page_id, no rev). The same bodies were checked against a real server built
// from that PR (see the PR description). The Pages detail is mocked because the server does not
// report `has_slides` yet (FE-REQ-F5-6). Everything else is the real app.
//
// NEXT_PUBLIC_* flags are inlined when the UI is built or started, so the browser cannot turn them
// on. The flag-on tests run only when the served UI was built with NEXT_PUBLIC_SLIDES_EDITOR=true
// and the runner has the same variable; the flag-off test runs against the default build.
const FLAGS_ON = process.env.NEXT_PUBLIC_SLIDES_EDITOR === "true"
const FLAG_OFF = process.env.NEXT_PUBLIC_SLIDES_EDITOR !== "true"
const PAGE_ID = "slides-e2e-page", SLUG = "slides-e2e"
const slidesPath = `/api/v1/pages/%40${PAGE_ID}/slides`

const fixture = JSON.parse(readFileSync(join(process.cwd(), "testdata/slides-schema-v1.json"), "utf8")) as { deck: SlideDeck }
// Identity is deck.deck_id, which must equal the Page ID; the fixture deck is re-keyed to the mocked Page.
const readBody = (deckId = PAGE_ID) => ({ schema_version: 1, revision: 3, order_version: 1, content_hash: "0".repeat(64), deck: { ...structuredClone(fixture.deck), deck_id: deckId } })
const text404 = (route: Route) => route.fulfill({ status: 404, contentType: "text/plain; charset=utf-8", body: "404 page not found\n" })

async function mockPage(page: Page, read: "missing" | "foreign" | "ok", seen: string[] = []) {
  await page.route(`**/api/v1/pages/${SLUG}?*`, route => route.fulfill({ json: { id: PAGE_ID, slug: SLUG, name: "Quarterly deck", has_slides: true, owner: "user/e2e", panels: [] } }))
  await page.route(`**${slidesPath}?*`, route => {
    const request = route.request()
    seen.push(`${request.method()} ${new URL(request.url()).pathname}`)
    if (read === "missing") return text404(route)
    return route.fulfill({ json: readBody(read === "foreign" ? "another_page" : PAGE_ID) })
  })
}
const editor = (page: Page) => page.getByRole("region", { name: "Slides editor" })
const position = async (page: Page, id: string) => {
  const transform = await page.locator(`g[data-element-id="${id}"]`).getAttribute("transform")
  const [x, y] = transform!.match(/-?\d+(\.\d+)?/g)!.map(Number)
  return { x, y }
}
const status = (page: Page) => editor(page).getByRole("status").first()

test.describe("Slides editor mount", () => {
  test.beforeEach(async ({ page }) => { await page.setViewportSize({ width: 1280, height: 900 }) })

  test("flag off: a has_slides Page is untouched and makes no Slides request", async ({ page }) => {
    test.skip(!FLAG_OFF, "the served UI was built with the mount flag on")
    const seen: string[] = []
    await mockPage(page, "ok", seen)
    await page.goto(`/pages/${SLUG}`)
    await expect(page.getByText("Quarterly deck").first()).toBeVisible({ timeout: 20_000 })
    await page.waitForTimeout(1000)
    await expect(editor(page)).toHaveCount(0)
    expect(seen).toEqual([])
  })

  test("select, move by keyboard, layer, undo: edits are a labelled local draft that reload discards", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with NEXT_PUBLIC_SLIDES_EDITOR set to true")
    const seen: string[] = []
    await mockPage(page, "ok", seen)
    await page.goto(`/pages/${SLUG}`)
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole("heading", { name: "Quarterly deck" })).toBeVisible()
    await expect(status(page)).toContainText("Revision 3. Saving is not available yet")
    await expect(editor(page).getByRole("button", { name: "Save changes" })).toHaveCount(0)

    const title = page.getByRole("button", { name: "text 1", exact: true })
    await title.click()
    await expect(title).toHaveAttribute("aria-pressed", "true")
    const before = await position(page, "title_b")
    await page.keyboard.press("Shift+ArrowRight")
    await page.keyboard.press("ArrowDown")
    await expect.poll(() => position(page, "title_b")).toEqual({ x: before.x + 10, y: before.y + 1 })
    await expect(status(page)).toContainText("Local draft, not saved")
    await expect(status(page)).toContainText("Revision 3 on the server is unchanged")

    await editor(page).getByRole("button", { name: "Bring forward" }).click()
    await editor(page).getByRole("button", { name: "Undo" }).click()
    await expect(status(page)).toContainText("Local draft, not saved")
    await expect.poll(() => position(page, "title_b")).toEqual({ x: before.x + 10, y: before.y + 1 })

    // No save route exists: the page only ever read, and a reload returns the confirmed revision.
    expect(seen.every(entry => entry.startsWith("GET "))).toBe(true)
    page.once("dialog", dialog => dialog.accept())
    await page.reload()
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await expect(status(page)).toContainText("Revision 3. Saving is not available yet")
    expect(await position(page, "title_b")).toEqual(before)
  })

  test("missing endpoints show an explicit state", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with NEXT_PUBLIC_SLIDES_EDITOR set to true")
    await mockPage(page, "missing")
    await page.goto(`/pages/${SLUG}`)
    await expect(page.getByText(/Slides editing is not available on this server yet/)).toBeVisible({ timeout: 30_000 })
    await expect(editor(page)).toHaveCount(0)
  })

  test("a deck that is not this Page's is an error state, never shown", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with NEXT_PUBLIC_SLIDES_EDITOR set to true")
    await mockPage(page, "foreign")
    await page.goto(`/pages/${SLUG}`)
    await expect(page.getByRole("alert").filter({ hasText: "does not belong to this Page" })).toBeVisible({ timeout: 30_000 })
    await expect(editor(page)).toHaveCount(0)
  })

  test("phone width is a read-only preview that does not overflow", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with NEXT_PUBLIC_SLIDES_EDITOR set to true")
    await page.setViewportSize({ width: 390, height: 844 })
    await mockPage(page, "ok")
    await page.goto(`/pages/${SLUG}`)
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await expect(status(page)).toContainText("read-only preview")
    for (const name of ["Undo", "Redo", "New slide", "Bring forward"]) await expect(editor(page).getByRole("button", { name }).first()).toBeDisabled()
    await expect(editor(page).getByRole("button", { name: "Save changes" })).toHaveCount(0)
    await expect(editor(page).getByRole("button", { name: "Present" })).toBeEnabled()
    await expect(editor(page).getByRole("group", { name: /Slide \d/ })).toHaveCount(0)
    await expect(editor(page).getByRole("img", { name: /Slide \d/ })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  })
})
