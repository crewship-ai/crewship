// Conformance V02: text style of a Design text layer (#5161). Font family,
// weight, size, line height, alignment, wrap and clip are set in Properties,
// saved as a version, and come back after a reload, on the canvas and at the
// server. Runs on the scenario's own disposable QA workspace.
import { randomUUID } from "node:crypto"
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import { applyOperations, commitNumber, createDesign, enableDesign, getDesign, listVersions, openCanvas, selectWorkspace, sessionUserId, type DesignNode } from "./design/canvas"

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)

test.describe("design text style", () => {
 test(
  "font, weight, size, line height, alignment, wrap and clip survive save and reload",
  { annotation: prdRequirement("V02.text-style", "Design: text style set in Properties is drawn, saved as a version and kept after reload") },
  async ({ prd }) => {
   const owner = prd.env.accounts.owner
   if (!owner) throw new Error("owner account is not configured")
   const context = await prd.browserContext(await loginStorageState(prd.origin, owner))
   const ws = await prd.qaWorkspace()
   await enableDesign(context.request, ws.id)
   await selectWorkspace(context, await sessionUserId(context.request), ws.id)
   const slug = `prd-text-${tag()}`
   await createDesign(context.request, ws.id, slug, `PRD text style ${slug}`)
   const fixture: DesignNode[] = [
    { id: "screen", type: "frame", name: "Screen", x: 64, y: 64, width: 420, height: 400, fill: "#ffffff" },
    { id: "headline", type: "text", name: "Headline", parentId: "screen", x: 20, y: 20, width: 200, height: 60, fill: "#172554", text: "A headline that is longer than its box" },
   ]
   await applyOperations(context.request, ws.id, slug, fixture.map(node => ({ op: "upsert", node })), "PRD text style fixture")
   const page = await context.newPage()
   if (!(await openCanvas(page, slug, ws.id))) {
    test.skip(prd.mode === "quick", "this build ships without the Design UI (NEXT_PUBLIC_ARTIFACTS_DESIGN)")
    throw new Error("the build renders 'Design is not enabled.'")
   }
   await page.getByLabel("Layers").getByRole("button", { name: "Headline", exact: true }).click()
   const style = page.getByRole("group", { name: "Text style" })
   if ((await style.count()) === 0) {
    test.skip(prd.mode === "quick", "this build ships without text style controls (NEXT_PUBLIC_ARTIFACTS_DESIGN_TEXT_STYLE)")
    throw new Error("the build has no Text style controls")
   }
   await style.getByLabel("Font family").selectOption("serif")
   await style.getByLabel("Font weight").selectOption("700")
   await commitNumber(style.getByLabel("Font size"), "28")
   await commitNumber(style.getByLabel("Line height"), "1.2")
   await style.getByLabel("Text alignment").selectOption("center")
   await style.getByLabel("Wrap text to the box width").check()
   await style.getByLabel("Clip text to the box").check()
   const expected = { fontFamily: "serif", fontWeight: 700, fontSize: 28, lineHeight: 1.2, textAlign: "center", textWrap: "wrap", overflow: "clip" }
   await expect.poll(async () => (await getDesign(context.request, ws.id, slug)).nodes.find(node => node.id === "headline"), { timeout: 20_000 }).toMatchObject(expected)

   const drawn = page.locator('[data-design-node="headline"] foreignObject[data-text-layout="wrap"]')
   await expect(drawn).toHaveCount(1)
   await expect(drawn).toHaveAttribute("overflow", "hidden")
   const before = await page.locator('[data-design-node="headline"]').screenshot()
   await prd.evidence("text-style-before-reload.png", before)

   const { latest_version: beforeSave } = await listVersions(context.request, ws.id, slug)
   await page.getByRole("button", { name: "Save version" }).click()
   await expect.poll(async () => (await listVersions(context.request, ws.id, slug)).latest_version, { timeout: 20_000 }).toBeGreaterThan(beforeSave ?? 0)
   const { latest_version: saved } = await listVersions(context.request, ws.id, slug)
   expect((await getDesign(context.request, ws.id, slug, saved)).nodes.find(node => node.id === "headline")).toMatchObject(expected)

   await page.reload()
   await expect(page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
   await page.getByLabel("Layers").getByRole("button", { name: "Headline", exact: true }).click()
   const reloaded = page.getByRole("group", { name: "Text style" })
   await expect(reloaded.getByLabel("Font family")).toHaveValue("serif")
   await expect(reloaded.getByLabel("Font weight")).toHaveValue("700")
   await expect(reloaded.getByLabel("Font size")).toHaveValue("28")
   await expect(reloaded.getByLabel("Line height")).toHaveValue("1.2")
   await expect(reloaded.getByLabel("Text alignment")).toHaveValue("center")
   await expect(reloaded.getByLabel("Wrap text to the box width")).toBeChecked()
   await expect(reloaded.getByLabel("Clip text to the box")).toBeChecked()
   const box = page.locator('[data-design-node="headline"] foreignObject[data-text-layout="wrap"] > div')
   await expect(box).toHaveCSS("font-weight", "700")
   await expect(box).toHaveCSS("font-size", "28px")
   await expect(box).toHaveCSS("text-align", "center")
   await expect(box).toHaveCSS("overflow", "hidden")
   await prd.evidence("text-style-after-reload.png", await page.screenshot())
   await prd.evidence("v02.json", { savedVersion: saved, expected })
  },
 )
})
