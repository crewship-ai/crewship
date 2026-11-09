// Conformance V04/V05 (#5201): Design editing tools on a live canvas, checked
// at the server. Resize from a handle (Escape puts it back), keyboard nudge,
// layer order from Properties and keys, Delete, and a comment whose layer was
// deleted saying so. Runs in the scenario's own disposable QA workspace.
import { randomUUID } from "node:crypto"
import type { Page } from "@playwright/test"
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import { applyOperations, createDesign, enableDesign, getDesign, openCanvas, scoped, selectWorkspace, sessionUserId, type DesignNode } from "./design/canvas"

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)
const drawnOrder = (page: Page) => page.locator('svg[aria-label="Design canvas"] g[data-design-node]').evaluateAll(nodes => nodes.map(node => node.getAttribute("data-design-node")))

test.describe("design tools", () => {
 test(
  "resize, nudge, order, delete and a comment on a deleted layer work on the live canvas",
  {
   annotation: [
    prdRequirement("V04.resize", "Design: a selected layer resizes from its handles; Escape during a resize puts the box back"),
    prdRequirement("V04.keyboard", "Design: arrows nudge the selected layer (Shift: 10), Delete removes it"),
    prdRequirement("V05.order-editor", "Design: Properties and [ ] change a layer's stacking order, stored and drawn"),
    prdRequirement("V05.comment-deleted-layer", "Design: a comment whose layer was deleted stays and says the layer is gone"),
   ],
  },
  async ({ prd }) => {
   const owner = prd.env.accounts.owner
   if (!owner) throw new Error("owner account is not configured")
   const context = await prd.browserContext(await loginStorageState(prd.origin, owner))
   const ws = await prd.qaWorkspace()
   await enableDesign(context.request, ws.id)
   await selectWorkspace(context, await sessionUserId(context.request), ws.id)
   const slug = `prd-tools-${tag()}`
   await createDesign(context.request, ws.id, slug, `PRD tools ${slug}`)
   const fixture: DesignNode[] = [
    { id: "screen", type: "frame", name: "Screen", x: 64, y: 64, width: 500, height: 400, fill: "#ffffff" },
    { id: "under", type: "rect", name: "Under", parentId: "screen", x: 40, y: 40, width: 200, height: 120, fill: "#94a3b8" },
    { id: "over", type: "rect", name: "Over", parentId: "screen", x: 100, y: 80, width: 200, height: 120, fill: "#2563eb" },
   ]
   await applyOperations(context.request, ws.id, slug, fixture.map(node => ({ op: "upsert", node })), "PRD tools fixture")
   const page = await context.newPage()
   if (!(await openCanvas(page, slug, ws.id))) {
    test.skip(prd.mode === "quick", "this build ships without the Design UI (NEXT_PUBLIC_ARTIFACTS_DESIGN)")
    throw new Error("the build renders 'Design is not enabled.'")
   }
   const server = async (id: string) => (await getDesign(context.request, ws.id, slug)).nodes.find(node => node.id === id)
   const viewport = page.getByLabel("Design canvas viewport")
   const scale = Number(((await viewport.getAttribute("data-design-view")) ?? "1").split(" ")[0])
   const found: Record<string, unknown> = {}

   // Resize "over" from its bottom-right handle; the top-left corner stays.
   await page.getByLabel("Layers").getByRole("button", { name: "Over", exact: true }).click()
   const handle = page.locator('[data-design-handle="se"]')
   await expect(handle).toHaveCount(1)
   let grip = (await handle.boundingBox())!
   await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2)
   await page.mouse.down()
   await page.mouse.move(grip.x + grip.width / 2 + 60, grip.y + grip.height / 2 + 40, { steps: 6 })
   await page.mouse.up()
   const resized = { x: 100, y: 80, width: Math.round(200 + 60 / scale), height: Math.round(120 + 40 / scale) }
   await expect.poll(() => server("over"), { timeout: 20_000 }).toMatchObject(resized)
   // Escape during a resize puts the box back, at the server too.
   grip = (await handle.boundingBox())!
   await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2)
   await page.mouse.down()
   await page.mouse.move(grip.x + 150, grip.y + 100, { steps: 5 })
   await page.keyboard.press("Escape")
   await page.mouse.up()
   await page.waitForTimeout(1000)
   expect(await server("over")).toMatchObject(resized)
   found.resize = { resized, escapeRestored: true }

   // Keyboard nudge on the focused canvas.
   await viewport.focus()
   await page.keyboard.press("ArrowRight")
   await page.keyboard.press("Shift+ArrowDown")
   await expect.poll(() => server("over"), { timeout: 20_000 }).toMatchObject({ x: 101, y: 90 })

   // Order: "over" is drawn last; Send to back in Properties, then ] brings it forward again.
   expect(await drawnOrder(page)).toEqual(["screen", "under", "over"])
   await page.getByRole("group", { name: "Layer order" }).getByRole("button", { name: "Send to back" }).click()
   await expect.poll(async () => (await getDesign(context.request, ws.id, slug)).nodes.map(node => node.id), { timeout: 20_000 }).toEqual(["screen", "over", "under"])
   await expect.poll(() => drawnOrder(page)).toEqual(["screen", "over", "under"])
   await viewport.focus()
   await page.keyboard.press("]")
   await expect.poll(async () => (await getDesign(context.request, ws.id, slug)).nodes.map(node => node.id), { timeout: 20_000 }).toEqual(["screen", "under", "over"])
   found.order = { afterSendToBack: ["screen", "over", "under"], afterBracket: await drawnOrder(page) }

   // A comment on "under", then Delete removes the layer; the comment says so.
   await page.getByLabel("Layers").getByRole("button", { name: "Under", exact: true }).click()
   const commentText = `Lighter grey ${tag()}`
   await page.getByLabel("Comment for agent").fill(commentText)
   await page.getByRole("button", { name: /Save comment|Send to agent/ }).click()
   await expect.poll(async () => {
    const res = await context.request.get(scoped(`/api/v1/pages/${encodeURIComponent(slug)}/design/comments`, ws.id))
    return ((await res.json()) as { comments?: { selector?: string; text?: string }[] }).comments?.find(comment => comment.text === commentText)?.selector
   }, { timeout: 20_000 }).toBe("under")
   await viewport.focus()
   await page.keyboard.press("Delete")
   await expect.poll(async () => (await server("under")) === undefined, { timeout: 20_000 }).toBe(true)
   const comment = page.getByLabel("Design comments").locator("article").filter({ hasText: commentText })
   await expect(comment).toContainText("This layer is no longer on the canvas")
   found.commentOnDeletedLayer = await comment.innerText()

   await prd.evidence("tools.json", found)
   await prd.evidence("tools-final.png", await page.screenshot())
  },
 )
})
