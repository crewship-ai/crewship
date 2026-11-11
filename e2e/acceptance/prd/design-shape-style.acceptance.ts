// Conformance V02: corner radius and stroke of Design rectangles and images
// (#5219), set in Properties, drawn, saved as a version and kept after reload,
// on the canvas and at the server. Fill of a rectangle is checked the same
// way. Runs on the scenario's own disposable QA workspace.
import { randomUUID } from "node:crypto"
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import { applyOperations, commitNumber, createDesign, enableDesign, getDesign, listVersions, openCanvas, selectWorkspace, sessionUserId, type DesignNode } from "./design/canvas"

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)

test.describe("design shape style", () => {
 test(
  "fill, corner radius and stroke of a rectangle and an image survive save and reload",
  { annotation: prdRequirement("V02.shape-style", "Design: fill, corner radius and stroke set in Properties for rectangles and images are drawn, saved as a version and kept after reload") },
  async ({ prd }) => {
   const owner = prd.env.accounts.owner
   if (!owner) throw new Error("owner account is not configured")
   const context = await prd.browserContext(await loginStorageState(prd.origin, owner))
   const ws = await prd.qaWorkspace()
   await enableDesign(context.request, ws.id)
   await selectWorkspace(context, await sessionUserId(context.request), ws.id)
   const slug = `prd-shape-${tag()}`
   await createDesign(context.request, ws.id, slug, `PRD shape style ${slug}`)
   const fixture: DesignNode[] = [
    { id: "screen", type: "frame", name: "Screen", x: 64, y: 64, width: 420, height: 420, fill: "#ffffff" },
    { id: "card", type: "rect", name: "Card", parentId: "screen", x: 20, y: 20, width: 200, height: 120, fill: "#2563eb" },
    { id: "photo", type: "image", name: "Photo", parentId: "screen", x: 20, y: 200, width: 200, height: 120, fill: "#ffffff" },
   ]
   await applyOperations(context.request, ws.id, slug, fixture.map(node => ({ op: "upsert", node })), "PRD shape style fixture")
   const page = await context.newPage()
   if (!(await openCanvas(page, slug, ws.id))) {
    test.skip(prd.mode === "quick", "this build ships without the Design UI (NEXT_PUBLIC_ARTIFACTS_DESIGN)")
    throw new Error("the build renders 'Design is not enabled.'")
   }
   const layers = page.getByLabel("Layers")
   await layers.getByRole("button", { name: "Card", exact: true }).click()
   const style = page.getByRole("group", { name: "Shape style" })
   if ((await style.count()) === 0) {
    test.skip(prd.mode === "quick", "this build ships without shape style controls (NEXT_PUBLIC_ARTIFACTS_DESIGN_SHAPE_STYLE)")
    throw new Error("the build has no Shape style controls")
   }
   await page.getByLabel("Fill value").fill("#16a34a")
   await commitNumber(style.getByLabel("Corner radius"), "24")
   await commitNumber(style.getByLabel("Stroke width"), "3")
   await style.getByLabel("Stroke color").fill("#dc2626")
   const card = { fill: "#16a34a", radius: 24, strokeWidth: 3, stroke: "#dc2626" }
   const server = async (id: string) => (await getDesign(context.request, ws.id, slug)).nodes.find(node => node.id === id)
   await expect.poll(() => server("card"), { timeout: 20_000 }).toMatchObject(card)
   await layers.getByRole("button", { name: "Photo", exact: true }).click()
   await commitNumber(page.getByRole("group", { name: "Shape style" }).getByLabel("Corner radius"), "60")
   await commitNumber(page.getByRole("group", { name: "Shape style" }).getByLabel("Stroke width"), "2")
   await expect.poll(() => server("photo"), { timeout: 20_000 }).toMatchObject({ radius: 60, strokeWidth: 2 })

   const drawnCard = page.locator('[data-design-node="card"]')
   await expect(drawnCard.locator("rect").first()).toHaveAttribute("rx", "24")
   await expect(drawnCard.locator("rect").first()).toHaveAttribute("fill", "#16a34a")
   await expect(drawnCard.locator("[data-shape-stroke]")).toHaveAttribute("stroke", "#dc2626")
   await prd.evidence("shape-style-before-reload.png", await page.screenshot())

   const { latest_version: beforeSave } = await listVersions(context.request, ws.id, slug)
   await page.getByRole("button", { name: "Save version" }).click()
   await expect.poll(async () => (await listVersions(context.request, ws.id, slug)).latest_version, { timeout: 20_000 }).toBeGreaterThan(beforeSave ?? 0)
   const { latest_version: saved } = await listVersions(context.request, ws.id, slug)
   const version = await getDesign(context.request, ws.id, slug, saved)
   expect(version.nodes.find(node => node.id === "card")).toMatchObject(card)
   expect(version.nodes.find(node => node.id === "photo")).toMatchObject({ radius: 60, strokeWidth: 2 })

   await page.reload()
   await expect(page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
   await page.getByLabel("Layers").getByRole("button", { name: "Card", exact: true }).click()
   const reloaded = page.getByRole("group", { name: "Shape style" })
   await expect(reloaded.getByLabel("Corner radius")).toHaveValue("24")
   await expect(reloaded.getByLabel("Stroke width")).toHaveValue("3")
   await expect(reloaded.getByLabel("Stroke color")).toHaveValue("#dc2626")
   await expect(page.getByLabel("Fill value")).toHaveValue("#16a34a")
   await expect(page.locator('[data-design-node="photo"] [data-shape-stroke]')).toHaveCount(1)
   await prd.evidence("shape-style-after-reload.png", await page.screenshot())
   await prd.evidence("v02-shape.json", { savedVersion: saved, card, photo: { radius: 60, strokeWidth: 2 } })
  },
 )
})
