// This helper runs in Node, outside happy-dom, so the browser and CSS compiler
// see their actual Node environment. Input is the real React-rendered rail or shared view control.
import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import postcss from "postcss"
import tailwind from "@tailwindcss/postcss"
import { chromium } from "@playwright/test"

const html = readFileSync(0, "utf8")
const views = process.argv.includes("--views")
const from = resolve("app/globals.css")
const css = (await postcss([tailwind()]).process(await readFile(from, "utf8"), { from })).css
const browser = await chromium.launch({ headless: true })
try {
  for (const [width, touch] of [[390, true], [820, true], [1440, true], [1440, false]]) {
    const context = await browser.newContext({ viewport: { width, height: 844 }, hasTouch: touch })
    const page = await context.newPage()
    await page.route("**/*", route => route.abort())
    await page.setContent(`<!doctype html><html data-text-size="small" class="${width === 820 ? "dark" : ""}"><head><style>${css}</style></head><body>${html}</body></html>`)
    if (views) {
      const group = page.getByRole("radiogroup", { name: "Chat panel" })
      const choices = group.getByRole("radio")
      assert.equal(await choices.count(), 4, "All four phone views must remain in the single group")
      const bounds = await group.boundingBox()
      assert.ok(bounds, "Phone views must be visible")
      let top
      for (const choice of await choices.all()) {
        const box = await choice.boundingBox()
        assert.ok(box, "Every view must be visible")
        if (touch) assert.ok(box.width >= 44 && box.height >= 44, "Every touch view must fit a 44px target")
        top ??= box.y
        assert.equal(box.y, top, "Phone views must share one row")
        assert.ok(box.x >= bounds.x && box.x + box.width <= bounds.x + bounds.width, "Every view must fit horizontally")
        assert.ok(box.y >= bounds.y && box.y + box.height <= bounds.y + bounds.height, "The row must fit the kit's height and padding")
        assert.ok(box.x >= 0 && box.x + box.width <= width, "Every view must fit the viewport")
      }
      await page.keyboard.press("Tab")
      assert.equal(await group.getByRole("radio", { checked: true }).evaluate(el => el === document.activeElement), true, "The selected view must receive keyboard focus")
    } else {
      const button = page.getByRole("button", { name: "Expand sidebar" })
      await page.keyboard.press("Tab")
      assert.equal(await button.evaluate(el => el === document.activeElement), true, "Expand must receive keyboard focus")
      const box = await button.boundingBox()
      const rail = await page.locator("aside").boundingBox()
      assert.ok(box && rail, "Expand and its rail must be visible")
      if (touch) {
        assert.ok(box.width >= 44 && box.height >= 44, "Touch target must be at least 44px")
      } else {
        assert.ok(rail.width < 44, "Fine-pointer rail must retain its compact size")
      }
      assert.ok(box.x >= rail.x, `Expand starts outside its rail: ${box.x} < ${rail.x}`)
      assert.ok(box.x + box.width <= rail.x + rail.width, "Expand must fit inside the rail")
      assert.ok(box.x >= 0 && box.x + box.width <= width, "Expand must remain within the viewport")
    }
    await context.close()
  }
} finally { await browser.close() }
