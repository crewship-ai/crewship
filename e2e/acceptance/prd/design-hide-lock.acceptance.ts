// Conformance V05: hide and lock per layer (#5218). Hidden is not drawn and
// stays hidden after a save and reload; locked is drawn and listed but cannot
// be picked or dragged on the canvas. Checked at the server. Runs on the
// scenario's own disposable QA workspace.
import { randomUUID } from "node:crypto"
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import { applyOperations, createDesign, enableDesign, getDesign, listVersions, openCanvas, selectWorkspace, sessionUserId, type DesignNode } from "./design/canvas"

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)

test.describe("design hide and lock", () => {
 test(
  "a hidden layer is not drawn and stays hidden after reload; a locked layer cannot be picked or moved on the canvas",
  {
   annotation: [
    prdRequirement("V05.hide", "Design: a layer hidden in Layers is not drawn, is stored and stays hidden after save and reload"),
    prdRequirement("V05.lock", "Design: a locked layer stays drawn and listed, but a click or drag on the canvas neither selects nor moves it"),
   ],
  },
  async ({ prd }) => {
   const owner = prd.env.accounts.owner
   if (!owner) throw new Error("owner account is not configured")
   const context = await prd.browserContext(await loginStorageState(prd.origin, owner))
   const ws = await prd.qaWorkspace()
   await enableDesign(context.request, ws.id)
   await selectWorkspace(context, await sessionUserId(context.request), ws.id)
   const slug = `prd-hidelock-${tag()}`
   await createDesign(context.request, ws.id, slug, `PRD hide lock ${slug}`)
   const fixture: DesignNode[] = [
    { id: "screen", type: "frame", name: "Screen", x: 64, y: 64, width: 500, height: 400, fill: "#ffffff" },
    { id: "badge", type: "rect", name: "Badge", parentId: "screen", x: 300, y: 40, width: 120, height: 60, fill: "#dc2626" },
    { id: "backdrop", type: "rect", name: "Backdrop", parentId: "screen", x: 40, y: 160, width: 300, height: 160, fill: "#94a3b8" },
   ]
   await applyOperations(context.request, ws.id, slug, fixture.map(node => ({ op: "upsert", node })), "PRD hide lock fixture")
   const page = await context.newPage()
   if (!(await openCanvas(page, slug, ws.id))) {
    test.skip(prd.mode === "quick", "this build ships without the Design UI (NEXT_PUBLIC_ARTIFACTS_DESIGN)")
    throw new Error("the build renders 'Design is not enabled.'")
   }
   const row = (name: string) => page.getByRole("group", { name: `Layer ${name}` })
   if ((await row("Badge").getByRole("button", { name: "Hide layer" }).count()) === 0) {
    test.skip(prd.mode === "quick", "this build ships without hide and lock controls (NEXT_PUBLIC_ARTIFACTS_DESIGN_HIDE_LOCK)")
    throw new Error("the build has no hide and lock controls")
   }
   const server = async (id: string) => (await getDesign(context.request, ws.id, slug)).nodes.find(node => node.id === id)
   const found: Record<string, unknown> = {}

   // Hide: gone from the canvas, still in Layers, stored.
   await row("Badge").getByRole("button", { name: "Hide layer" }).click()
   await expect.poll(() => server("badge"), { timeout: 20_000 }).toMatchObject({ hidden: true })
   await expect(page.locator('[data-design-node="badge"]')).toHaveCount(0)
   await expect(row("Badge").getByRole("button", { name: "Show layer" })).toHaveAttribute("aria-pressed", "true")

   // Lock: a click selects nothing and a drag moves nothing.
   await row("Backdrop").getByRole("button", { name: "Lock layer" }).click()
   await expect.poll(() => server("backdrop"), { timeout: 20_000 }).toMatchObject({ locked: true })
   const box = (await page.locator('[data-design-node="backdrop"] rect').first().boundingBox())!
   const at = { x: box.x + box.width / 2, y: box.y + box.height / 2 }
   await page.mouse.click(at.x, at.y)
   await expect(page.getByLabel("Layer name")).not.toHaveValue("Backdrop")
   await page.mouse.move(at.x, at.y)
   await page.mouse.down()
   await page.mouse.move(at.x + 80, at.y + 40, { steps: 6 })
   await page.mouse.up()
   await page.waitForTimeout(1500)
   expect(await server("backdrop"), "a drag on a locked layer moves nothing").toMatchObject({ x: 40, y: 160, locked: true })
   found.lock = { clickSelected: false, afterDrag: await server("backdrop") }

   // Save and reload: both states stay.
   const { latest_version: beforeSave } = await listVersions(context.request, ws.id, slug)
   await page.getByRole("button", { name: "Save version" }).click()
   await expect.poll(async () => (await listVersions(context.request, ws.id, slug)).latest_version, { timeout: 20_000 }).toBeGreaterThan(beforeSave ?? 0)
   await page.reload()
   await expect(page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
   await expect(page.locator('[data-design-node="badge"]')).toHaveCount(0)
   await expect(page.locator('[data-design-node="backdrop"]')).toHaveAttribute("data-locked", "")
   await expect(row("Backdrop").getByRole("button", { name: "Unlock layer" })).toHaveAttribute("aria-pressed", "true")
   found.reload = { badge: await server("badge"), backdrop: await server("backdrop") }

   // Show again: drawn again.
   await row("Badge").getByRole("button", { name: "Show layer" }).click()
   await expect(page.locator('[data-design-node="badge"]')).toHaveCount(1)
   await prd.evidence("hide-lock.json", found)
   await prd.evidence("hide-lock-final.png", await page.screenshot())
  },
 )
})
