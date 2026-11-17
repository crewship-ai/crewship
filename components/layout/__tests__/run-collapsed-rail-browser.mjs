// This helper runs in Node, outside happy-dom, so the browser and CSS compiler
// see their actual Node environment. Input is the real React-rendered aside.
import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import postcss from "postcss"
import tailwind from "@tailwindcss/postcss"
import { chromium } from "@playwright/test"

const html = readFileSync(0, "utf8")
const from = resolve("app/globals.css")
const css = (await postcss([tailwind()]).process(await readFile(from, "utf8"), { from })).css
const browser = await chromium.launch({ headless: true })
try {
  for (const [width, touch] of [[390, true], [820, true], [1440, true], [1440, false]]) {
    const context = await browser.newContext({ viewport: { width, height: 844 }, hasTouch: touch })
    const page = await context.newPage()
    await page.route("**/*", route => route.abort())
    await page.setContent(`<!doctype html><html data-text-size="small" class="${width === 820 ? "dark" : ""}"><head><style>${css}</style></head><body>${html}</body></html>`)
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
    await context.close()
  }
} finally { await browser.close() }
