import { chromium, devices, expect } from "@playwright/test"
import { writeFile } from "node:fs/promises"
import { homedir } from "node:os"
import { join } from "node:path"
const baseURL = process.env.CREWSHIP_SERVER
const storageState = join(homedir(), ".crewship", "e2e-auth", "crewship-e2e-auth-" + process.env.CREWSHIP_INSTANCE_ID + ".json")
const browser = await chromium.launch()

try {
  for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({ ...devices["Desktop Chrome"], storageState, baseURL, colorScheme: theme })
    try {
      const page = await context.newPage()
      await page.goto("/graph-check")
      await page.locator(".react-flow__node").first().waitFor({ state: "visible" })
      await page.evaluate(dark => document.documentElement.classList.toggle("dark", dark), theme === "dark")
      for (const [index, name] of ["Activity chain", "Activity overview", "Routine step trace", "Workflow graph"].entries()) {
        const graph = page.getByRole("region", { name })
        await graph.scrollIntoViewIfNeeded()
        const pane = graph.locator(".react-flow")
        const first = graph.locator(".react-flow__node").first()
        const edge = graph.locator(".react-flow__edge").first()
        await expect(first).toBeVisible()
        await expect(edge).toBeVisible()
        await first.focus()
        await expect(first).toBeFocused()
        const edgeFocusable = (await edge.getAttribute("tabindex")) !== null
        let keyboardSelected = false
        if (edgeFocusable) {
          await edge.focus()
          await expect(edge).toBeFocused()
          await page.keyboard.press("Enter")
          keyboardSelected = /(^| )selected( |$)/.test(await edge.getAttribute("class") ?? "")
        }
        const path = edge.locator(".react-flow__edge-path, .edge-dash-idle, .edge-dash-active").first()
        await path.scrollIntoViewIfNeeded()
        await pane.locator(".react-flow__pane").first().click({ position: { x: 8, y: 8 } })
        const clickPoint = await path.evaluate(el => {
          const edge = el.closest(".react-flow__edge")
          for (const fraction of [0.5, 0.48, 0.52, 0.4, 0.6]) {
            const p = el.getPointAtLength(el.getTotalLength() * fraction)
            const pos = new DOMPoint(p.x, p.y).matrixTransform(el.getScreenCTM())
            if (edge?.contains(document.elementFromPoint(pos.x, pos.y))) return { x: pos.x, y: pos.y }
          }
          return null
        })
        if (clickPoint) await page.mouse.click(clickPoint.x, clickPoint.y)
        const mouseSelected = /(^| )selected( |$)/.test(await edge.getAttribute("class") ?? "")
        const fit = graph.locator(".react-flow__controls-fitview")
        await expect(fit).toBeVisible()
        const vp = graph.locator(".react-flow__viewport")
        const before = await vp.evaluate(el => getComputedStyle(el).transform)
        await graph.locator(".react-flow__controls-zoomin").click()
        await expect.poll(() => vp.evaluate(el => getComputedStyle(el).transform)).not.toBe(before)
        await fit.click()
        await expect.poll(async () => {
          const bounds = await pane.boundingBox()
          const node = await first.boundingBox()
          return !!bounds && !!node && node.x >= bounds.x - 2 && node.y >= bounds.y - 2 &&
            node.x + node.width <= bounds.x + bounds.width + 2 && node.y + node.height <= bounds.y + bounds.height + 2
        }).toBe(true)
        console.log("GRAPH " + JSON.stringify({ name, theme, edgeFocusable, keyboardSelected, mouseHit: !!clickPoint, mouseSelected, fit: true }))
        const screenshot = await graph.screenshot({ animations: "disabled", type: "jpeg", quality: 65 })
        await writeFile(join(process.env.BROWSER_EVIDENCE, theme + "-" + index + ".jpg"), screenshot)
      }
    } finally {
      await context.close()
    }
  }
} finally {
  await browser.close()
}
