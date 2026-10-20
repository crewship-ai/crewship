import { expect, test, type Page, type Route } from "@playwright/test"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import type { SlideDeck } from "../lib/slides/model"
import { createFixtureSaveClient } from "../lib/slides/save-fixture"

// The Slides read/save endpoints do not exist on the server yet (coord
// FE-REQ-F5-1), so they are served here by the in-memory contract fixture
// through page.route. The Pages detail is mocked too, because the server does not
// report `has_slides` yet (FE-REQ-F5-6). Everything else is the real app.
//
// NEXT_PUBLIC_* flags are inlined when the UI is built or started, so the browser
// cannot turn them on. The flag-on tests run only when the served UI was built
// with NEXT_PUBLIC_SLIDES_EDITOR=true and NEXT_PUBLIC_SLIDES_EDITOR_SAVE=true and
// the runner has the same variables; the flag-off test runs against the default build.
const FLAGS_ON = process.env.NEXT_PUBLIC_SLIDES_EDITOR === "true" && process.env.NEXT_PUBLIC_SLIDES_EDITOR_SAVE === "true"
const FLAG_OFF = process.env.NEXT_PUBLIC_SLIDES_EDITOR !== "true"
const PAGE_ID = "slides-e2e-page", SLUG = "slides-e2e"
const slidesPath = `/api/v1/pages/%40${PAGE_ID}/slides`

const fixture = JSON.parse(readFileSync(join(process.cwd(), "testdata/slides-schema-v1.json"), "utf8")) as { deck: SlideDeck }
// The save contract binds a deck to its Page (page_id == deck_id); the fixture deck is re-keyed to the mocked Page.
const start = () => createFixtureSaveClient({ deck: { ...structuredClone(fixture.deck), deck_id: PAGE_ID }, rev: 3, orderVersion: 1 })
type Server = ReturnType<typeof start>
const text404 = (route: Route) => route.fulfill({ status: 404, contentType: "text/plain; charset=utf-8", body: "404 page not found\n" })

async function mockPage(page: Page, server: Server | "missing", seen: string[] = []) {
  await page.route(`**/api/v1/pages/${SLUG}?*`, route => route.fulfill({ json: { id: PAGE_ID, slug: SLUG, name: "Quarterly deck", has_slides: true, owner: "user/e2e", panels: [] } }))
  await page.route(`**${slidesPath}?*`, async route => {
    const request = route.request()
    seen.push(`${request.method()} ${new URL(request.url()).pathname}`)
    if (server === "missing") return text404(route)
    if (request.method() === "GET") { const head = server.head(); return route.fulfill({ json: { deck: head.deck, rev: head.rev, order_version: head.orderVersion } }) }
    const result = await server.patch(request.postDataJSON())
    if (result.status === "saved") return route.fulfill({ json: { deck: result.snapshot.deck, rev: result.snapshot.rev, order_version: result.snapshot.orderVersion, mutation_id: result.mutationId } })
    if (result.status === "conflict") return route.fulfill({ status: 409, json: { deck: result.current.deck, rev: result.current.rev, order_version: result.current.orderVersion } })
    return route.fulfill({ status: 400, json: { error: result.status === "error" ? result.message : "unavailable" } })
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
    await mockPage(page, start(), seen)
    await page.goto(`/pages/${SLUG}`)
    await expect(page.getByText("Quarterly deck").first()).toBeVisible({ timeout: 20_000 })
    await page.waitForTimeout(1000)
    await expect(editor(page)).toHaveCount(0)
    expect(seen).toEqual([])
  })

  test("select, move by keyboard, layer, undo, save, reload", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with NEXT_PUBLIC_SLIDES_EDITOR and NEXT_PUBLIC_SLIDES_EDITOR_SAVE set to true")
    const server = start()
    await mockPage(page, server)
    await page.goto(`/pages/${SLUG}`)
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole("heading", { name: "Quarterly deck" })).toBeVisible()
    await expect(status(page)).toContainText("Revision 3")
    await expect(editor(page).getByRole("button", { name: "Save changes" })).toBeDisabled()

    const title = page.getByRole("button", { name: "text 1", exact: true })
    await title.click()
    await expect(title).toHaveAttribute("aria-pressed", "true")
    const before = await position(page, "title_b")
    await page.keyboard.press("Shift+ArrowRight")
    await page.keyboard.press("ArrowDown")
    await expect.poll(() => position(page, "title_b")).toEqual({ x: before.x + 10, y: before.y + 1 })
    await expect(status(page)).toContainText("Unsaved changes")

    // Layering cannot be saved yet (FE-REQ-F5-2): the draft keeps it, Save is blocked and says why.
    await editor(page).getByRole("button", { name: "Bring forward" }).click()
    await expect(status(page)).toContainText("layer order cannot be saved yet")
    await expect(editor(page).getByRole("button", { name: "Save changes" })).toBeDisabled()
    await editor(page).getByRole("button", { name: "Undo" }).click()
    await expect(status(page)).toContainText("Unsaved changes")
    await expect.poll(() => position(page, "title_b")).toEqual({ x: before.x + 10, y: before.y + 1 })

    await editor(page).getByRole("button", { name: "Save changes" }).click()
    await expect(status(page)).toContainText("Saved as revision 4")
    expect(server.head().rev).toBe(4)
    expect(server.head().deck.slides.find(slide => slide.id === "slide_b")!.elements[0]).toMatchObject({ id: "title_b", x: before.x + 10, y: before.y + 1 })

    await page.reload()
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await expect(status(page)).toContainText("Revision 4")
    expect(await position(page, "title_b")).toEqual({ x: before.x + 10, y: before.y + 1 })
  })

  test("conflict keeps the draft and offers an explicit recovery", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with the Slides editor flags")
    const server = start()
    await mockPage(page, server)
    await page.goto(`/pages/${SLUG}`)
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await page.getByRole("button", { name: "text 1", exact: true }).click()
    await page.keyboard.press("Shift+ArrowRight")
    const moved = await position(page, "title_b")
    // Someone else saves first.
    const head = server.head(); head.deck.slides[0].notes = "Changed elsewhere"
    server.advance({ ...head, rev: head.rev + 1 })
    await editor(page).getByRole("button", { name: "Save changes" }).click()
    await expect(page.getByText(/Someone else changed this presentation \(now revision 4\)/)).toBeVisible()
    expect(await position(page, "title_b")).toEqual(moved)
    expect(server.head().rev).toBe(4)
    await page.getByRole("button", { name: "Keep editing locally" }).click()
    await expect(page.getByText(/Saving stays blocked until you load the latest version/)).toBeVisible()
    await expect(editor(page).getByRole("button", { name: "Save changes" })).toBeDisabled()
    await page.getByRole("button", { name: "Load latest and discard my draft" }).click()
    await expect(status(page)).toContainText("Revision 4")
    await expect(page.getByLabel("Speaker notes")).toHaveValue("Changed elsewhere")
    expect((await position(page, "title_b")).x).toBe(moved.x - 10)
  })

  test("missing endpoints show an explicit state and never claim a save", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with the Slides editor flags")
    await mockPage(page, "missing")
    await page.goto(`/pages/${SLUG}`)
    await expect(page.getByText(/Slides editing is not available on this server yet/)).toBeVisible({ timeout: 30_000 })
    await expect(editor(page)).toHaveCount(0)
  })

  test("a missing save route keeps the draft", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with the Slides editor flags")
    const server = start()
    await mockPage(page, server)
    await page.route(`**${slidesPath}?*`, route => route.request().method() === "PATCH" ? text404(route) : route.fallback())
    await page.goto(`/pages/${SLUG}`)
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await page.getByLabel("Speaker notes").fill("Do not lose me")
    await editor(page).getByRole("button", { name: "Save changes" }).click()
    await expect(page.getByText(/Saving is not available on this server yet/)).toBeVisible()
    await expect(page.getByLabel("Speaker notes")).toHaveValue("Do not lose me")
    await expect(status(page)).not.toContainText("Saved as revision")
    expect(server.head().rev).toBe(3)
  })

  test("phone width is a read-only preview that does not overflow", async ({ page }) => {
    test.skip(!FLAGS_ON, "needs a UI built with the Slides editor flags")
    await page.setViewportSize({ width: 390, height: 844 })
    const server = start()
    await mockPage(page, server)
    await page.goto(`/pages/${SLUG}`)
    await expect(editor(page)).toBeVisible({ timeout: 30_000 })
    await expect(status(page)).toContainText("read-only preview")
    for (const name of ["Undo", "Redo", "New slide", "Bring forward"]) await expect(editor(page).getByRole("button", { name }).first()).toBeDisabled()
    await expect(editor(page).getByRole("button", { name: "Save changes" })).toBeDisabled()
    await expect(editor(page).getByRole("button", { name: "Present" })).toBeEnabled()
    await expect(editor(page).getByRole("group", { name: /Slide \d/ })).toHaveCount(0)
    await expect(editor(page).getByRole("img", { name: /Slide \d/ })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    expect(server.head().rev).toBe(3)
  })
})
