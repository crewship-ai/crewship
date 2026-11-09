/**
 * G09 on a real server (#5144): keyboard, focus, icon names, Escape and phone
 * layout for Docs and Design, for an editor and a read-only commenter.
 */
import { expect, test, type Page } from "@playwright/test"
import { COMMENTER, EDITOR, closeAll, createDesign, createDoc, grant, requireThrowaway, screenshot, signIn, unique } from "./fixtures/collab"

test.beforeAll(() => requireThrowaway())

const PHONE = { width: 390, height: 844 }
const LONG = "Quarterly operations review with a deliberately long artifact name that must not push the phone header off screen"

/** Icon-only buttons in the artifact frame that a screen reader could not name. */
async function unnamedButtons(page: Page) {
  return page.locator("main button:visible").evaluateAll(buttons => buttons
    .filter(button => !(button.getAttribute("aria-label") || button.getAttribute("aria-labelledby") || button.getAttribute("title") || (button.textContent ?? "").trim()))
    .map(button => button.outerHTML.slice(0, 120)))
}

test("G09 Docs on a phone: the active line stays above the bottom tools, names fit, icons are named", async ({ browser }, info) => {
  const doc = createDoc(unique("g09p"), Array.from({ length: 30 }, (_, i) => `Paragraph ${i + 1} of a long document that fills the phone screen.`), LONG)
  grant(doc.slug, EDITOR, "write"); grant(doc.slug, COMMENTER, "comment")
  const b = await signIn(browser, EDITOR, info, { viewport: PHONE })
  const c = await signIn(browser, COMMENTER, info, { viewport: PHONE })
  try {
    for (const s of [b, c]) {
      await s.page.goto(`/artifacts/${doc.id}`)
      await expect(s.page.locator("[data-docs-page] .ProseMirror")).toBeVisible()
      expect(await unnamedButtons(s.page), `${s.who.label} unnamed buttons`).toEqual([])
      // No horizontal page scroll with a long name.
      expect(await s.page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
    }
    const last = b.page.locator("[data-docs-page] .ProseMirror > p").last()
    await last.scrollIntoViewIfNeeded()
    await last.click()
    await b.page.keyboard.press("End")
    await b.page.keyboard.type(" typed on a phone")
    const line = (await last.boundingBox())!
    // An editor on a phone always has the formatting tools docked at the bottom.
    const tools = b.page.getByRole("toolbar").last()
    await expect(tools).toBeVisible()
    const bar = (await tools.boundingBox())!
    expect(line.y + line.height <= bar.y || line.y >= bar.y + bar.height, "active line hidden under the bottom tools").toBe(true)
    expect(line.y + line.height).toBeLessThanOrEqual(PHONE.height)
    await screenshot(b.page, info, "g09-docs-phone-editor"); await screenshot(c.page, info, "g09-docs-phone-commenter")
  } finally { await closeAll([b, c], info) }
})

test("G09 keyboard: the status chip and Share are reachable, Escape closes and returns focus", async ({ browser }, info) => {
  const doc = createDoc(unique("g09k"), ["Keyboard paragraph."])
  const design = createDesign(unique("g09d"), [{ id: "r1", type: "rect", name: "Box", x: 40, y: 40, width: 80, height: 80, fill: "#3366ff" }])
  for (const slug of [doc.slug, design.slug]) grant(slug, EDITOR, "write")
  const b = await signIn(browser, EDITOR, info)
  try {
    for (const id of [doc.id, design.id]) {
      await b.page.goto(`/artifacts/${id}`)
      const chip = b.page.locator("button[data-status]")
      await expect(chip).toBeVisible()
      expect(await unnamedButtons(b.page), `unnamed buttons on ${id}`).toEqual([])
      await chip.focus()
      await b.page.keyboard.press("Enter")
      await expect(b.page.getByRole("dialog", { name: "Status" })).toBeVisible()
      await b.page.keyboard.press("Escape")
      await expect(b.page.getByRole("dialog", { name: "Status" })).toHaveCount(0)
      await expect(chip).toBeFocused()
      const share = b.page.getByRole("button", { name: "Share" })
      await share.focus()
      await b.page.keyboard.press("Enter")
      await expect(b.page.getByRole("dialog")).toBeVisible()
      await b.page.keyboard.press("Escape")
      await expect(b.page.getByRole("dialog")).toHaveCount(0)
      await expect(share).toBeFocused()
    }
  } finally { await closeAll([b], info) }
})
