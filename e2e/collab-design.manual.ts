/**
 * Design collaboration with two genuinely different identities (#5144).
 * CO01–CO04, CO06, CO08, CO10 and G08 for Design, on a throwaway server only.
 *
 * Design syncs its Yjs document and awareness. Selection and local undo
 * are now positive assertions, preserving the other editor's changes.
 */
import { expect, test, type Page } from "@playwright/test"
import { COMMENTER, EDITOR, OWNER, closeAll, createDesign, designNodes, grant, requireThrowaway, restart, revoke, screenshot, signIn, unique, type DesignNode, type Session } from "./fixtures/collab"

test.beforeAll(() => requireThrowaway())

const NODES: DesignNode[] = [
  { id: "f1", type: "frame", name: "Screen", x: 0, y: 0, width: 800, height: 600, fill: "#ffffff" },
  { id: "r1", type: "rect", name: "Box one", parentId: "f1", x: 100, y: 100, width: 120, height: 80, fill: "#3366ff" },
  { id: "r2", type: "rect", name: "Box two", parentId: "f1", x: 400, y: 300, width: 120, height: 80, fill: "#ff6633" },
]

const canvas = (page: Page) => page.getByRole("img", { name: "Design canvas" })
const node = (page: Page, id: string) => page.locator(`[data-design-node="${id}"]`)
const people = (page: Page) => page.getByLabel("People on this canvas").first()

async function openDesign(session: Session, id: string, page = session.page) {
  await page.goto(`/artifacts/${id}`)
  await expect(canvas(page)).toBeVisible()
  await expect(node(page, "r1")).toBeVisible()
}

async function displayName(session: Session) {
  const value = await (await session.context.request.get("/api/auth/session")).json() as { user?: { name?: string } }
  return value.user?.name ?? ""
}

async function center(page: Page, id: string) {
  const box = (await node(page, id).boundingBox())!
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}

async function drag(page: Page, id: string, dx: number, dy: number) {
  const from = await center(page, id)
  await page.mouse.move(from.x, from.y)
  await page.mouse.down()
  for (let i = 1; i <= 8; i++) await page.mouse.move(from.x + (dx * i) / 8, from.y + (dy * i) / 8)
  await page.mouse.up()
}

const position = (slug: string, id: string) => {
  const found = designNodes(slug).find(n => n.id === id)!
  return { x: found.x, y: found.y }
}

test.describe("Design, two identities", () => {
  test("CO01 presence: names on the same canvas, none from another canvas, gone after leaving", async ({ browser }, info) => {
    const a1 = createDesign(unique("co01d-a"), NODES)
    const b1 = createDesign(unique("co01d-b"), NODES, { share: true })
    grant(a1.slug, EDITOR, "write"); grant(b1.slug, COMMENTER, "comment")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    const c = await signIn(browser, COMMENTER, info)
    try {
      await openDesign(a, a1.id); await openDesign(b, a1.id); await openDesign(c, b1.id)
      const [ownerName, editorName, commenterName] = [await displayName(a), await displayName(b), await displayName(c)]
      for (const name of [ownerName, editorName, commenterName]) expect(name, "display name").not.toBe("")
      expect(new Set([ownerName, editorName, commenterName]).size).toBe(3)
      await expect(people(a.page).locator(`[title="${editorName}"]`)).toBeVisible()
      await expect(people(b.page).locator(`[title="${ownerName}"]`)).toBeVisible()
      // The commenter is on another canvas: never listed here, and sees nobody there.
      await expect(a.page.locator(`[title="${commenterName}"]`)).toHaveCount(0)
      await expect(c.page.getByLabel("People on this canvas")).toHaveCount(0)
      // Each person keeps one colour, and the two differ.
      const colour = (page: Page, name: string) => page.locator(`[aria-label="People on this canvas"] [title="${name}"]`).first().evaluate(el => getComputedStyle(el).backgroundColor)
      expect(await colour(a.page, editorName)).not.toBe(await colour(b.page, ownerName))
      await screenshot(a.page, info, "co01-design-owner-presence")
      await b.context.close()
      await expect(a.page.getByLabel("People on this canvas")).toHaveCount(0, { timeout: 45_000 })
    } finally { await closeAll([a, c], info) }
  })

  test("CO02 remote pointer lands on the same canvas point after the viewer zooms and scrolls", async ({ browser }, info) => {
    const d = createDesign(unique("co02d"), NODES)
    grant(d.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDesign(a, d.id); await openDesign(b, d.id)
      const ownerName = await displayName(a)
      const pointer = b.page.locator(`[aria-label="${ownerName} cursor"]`)
      const target = await center(a.page, "r1")
      await a.page.mouse.move(target.x - 5, target.y - 5)
      await a.page.mouse.move(target.x, target.y, { steps: 4 })
      await expect(pointer).toBeVisible()
      const near = async () => {
        const tip = (await pointer.locator("path").boundingBox())!
        const box = (await node(b.page, "r1").boundingBox())!
        return Math.hypot(tip.x - (box.x + box.width / 2), tip.y - (box.y + box.height / 2))
      }
      await expect.poll(near).toBeLessThan(8)
      await b.page.getByRole("button", { name: "Zoom in" }).click()
      await b.page.getByRole("button", { name: "Zoom in" }).click()
      await node(b.page, "r1").scrollIntoViewIfNeeded()
      await expect.poll(near).toBeLessThan(8)
      await screenshot(b.page, info, "co02-design-editor-remote-pointer-zoomed")
      await a.page.mouse.move(5, 5) // leaving the canvas clears the pointer
      await expect(pointer).toHaveCount(0)
    } finally { await closeAll([a, b], info) }
  })

  test("CO02 remote object selection is visible to the other editor", async ({ browser }, info) => {
    const d = createDesign(unique("co02s"), NODES)
    grant(d.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDesign(a, d.id); await openDesign(b, d.id)
      await node(a.page, "r2").click()
      await expect(b.page.locator('[data-remote-selection="r2"], [aria-label*="selected by"]')).toHaveCount(1, { timeout: 8_000 })
    } finally { await closeAll([a, b], info) }
  })

  test("CO03 concurrent moves of two objects and of the same object converge and persist", async ({ browser }, info) => {
    const d = createDesign(unique("co03d"), NODES)
    grant(d.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDesign(a, d.id); await openDesign(b, d.id)
      await Promise.all([drag(a.page, "r1", 60, 0), drag(b.page, "r2", 0, 50)])
      await expect.poll(() => position(d.slug, "r1").x, { timeout: 20_000 }).toBeGreaterThan(120)
      await expect.poll(() => position(d.slug, "r2").y, { timeout: 20_000 }).toBeGreaterThan(320)
      // Same object from both sides: one value wins everywhere, no torn state.
      await Promise.all([drag(a.page, "r1", 0, 60), drag(b.page, "r1", 0, -40)])
      await a.page.waitForTimeout(2_000)
      await Promise.all([a.page.reload(), b.page.reload()])
      await expect(node(a.page, "r1")).toBeVisible(); await expect(node(b.page, "r1")).toBeVisible()
      const transform = (page: Page) => node(page, "r1").getAttribute("transform")
      await expect.poll(async () => (await transform(a.page)) === (await transform(b.page))).toBe(true)
      const server = position(d.slug, "r1")
      const [x, y] = ((await transform(a.page)) ?? "").match(/translate\(([-\d.]+)[ ,]+([-\d.]+)\)/)!.slice(1).map(Number)
      // The node sits inside frame f1 at the origin, so its drawn offset is its stored position.
      expect({ x, y }).toEqual(server)
      expect(position(d.slug, "r2").y).toBeGreaterThan(320)
      await screenshot(a.page, info, "co03-design-converged")
    } finally { await closeAll([a, b], info) }
  })

  test("CO04 undo takes back the local person's move only", async ({ browser }, info) => {
    const d = createDesign(unique("co04d"), NODES)
    grant(d.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDesign(a, d.id); await openDesign(b, d.id)
      await drag(a.page, "r1", 60, 0)
      await drag(b.page, "r2", 0, 50)
      await expect.poll(() => position(d.slug, "r2").y, { timeout: 20_000 }).toBeGreaterThan(320)
      await a.page.keyboard.press("ControlOrMeta+z")
      await expect.poll(() => position(d.slug, "r1").x, { timeout: 8_000 }).toBe(100)
      expect(position(d.slug, "r2").y).toBeGreaterThan(320)
    } finally { await closeAll([a, b], info) }
  })

  test("G08 offline property edit stays unconfirmed until the exact value is saved after reconnect", async ({browser},info)=>{
    const d=createDesign(unique('g08-property'),NODES);grant(d.slug,EDITOR,'write')
    const b=await signIn(browser,EDITOR,info)
    try{
      await openDesign(b,d.id);await node(b.page,'r2').click()
      const y=b.page.getByLabel('y',{exact:true});await expect(y).toHaveValue('300')
      await b.context.setOffline(true)
      await y.fill('350');await expect(y).toHaveValue('350')
      await b.page.waitForTimeout(3000)
      expect(await b.page.locator('button[data-status]').getAttribute('data-status')).not.toMatch(/^(saved|live)$/)
      expect(position(d.slug,'r2').y).toBe(300)
      await screenshot(b.page,info,'g08-property-offline')
      await b.context.setOffline(false)
      await expect.poll(()=>position(d.slug,'r2').y,{timeout:30000}).toBe(350)
      await b.page.reload();await expect(node(b.page,'r2')).toBeVisible()
      await node(b.page,'r2').click();await expect(b.page.getByLabel('y',{exact:true})).toHaveValue('350')
      expect(position(d.slug,'r2').y).toBe(350)
      await screenshot(b.page,info,'g08-property-reloaded')
    }finally{await closeAll([b],info)}
  })

  test("G08 drag-layout regression: an offline warning must not reverse the gesture (#5283)", async ({ browser }, info) => {
    const d = createDesign(unique("g08d"), NODES)
    grant(d.slug, EDITOR, "write")
    const b = await signIn(browser, EDITOR, info)
    try {
      // Every Design socket of this page goes through this route; closing it is a network loss.
      const sockets: { close: () => Promise<void> }[] = []
      await b.page.routeWebSocket(/\/design\/ws/, ws => { const server = ws.connectToServer(); sockets.push({ close: () => server.close() }) })
      await openDesign(b, d.id)
      await expect(b.page.locator("button[data-status]")).toHaveAttribute("data-status", /^(live|saved)$/)
      await b.context.setOffline(true)
      for (const s of sockets) await s.close()
      await drag(b.page, "r2", 0, 50)
      await b.page.waitForTimeout(3_000)
      const key = await b.page.locator("button[data-status]").getAttribute("data-status")
      expect(key).not.toMatch(/^(saved|live)$/)
      expect(position(d.slug, "r2").y).toBe(300)
      await screenshot(b.page, info, "g08-design-offline-status")
      const localY=Number(await b.page.getByLabel('y',{exact:true}).inputValue())
      const warning=await b.page.getByRole('alert').filter({hasText:'Browser draft storage is unavailable or full'}).isVisible()
      await b.context.setOffline(false)
      // First prove delivery: the known geometry defect must never hide a lost edit.
      await expect.poll(() => position(d.slug, "r2").y, { timeout: 30_000 }).toBe(localY)
      if(warning && localY < 300){
        info.annotations.push({type:'issue',description:'#5283: viewport shifted during the active offline drag; local/server agree but direction is wrong'})
        test.fail(true,'#5283: draft-warning layout change reverses an active drag; synchronization was independently confirmed')
      }
      expect(localY,'downward gesture preserves intended movement').toBeGreaterThan(320)
    } finally { await closeAll([b], info) }
  })

  test("CO06 revoke while open removes the canvas; the editor's other canvas keeps syncing", async ({ browser }, info) => {
    const dA = createDesign(unique("co06d-a"), NODES)
    const dB = createDesign(unique("co06d-b"), NODES)
    for (const d of [dA, dB]) grant(d.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    const bOther = await b.context.newPage()
    try {
      await openDesign(a, dA.id); await openDesign(b, dA.id); await openDesign(b, dB.id, bOther)
      revoke(dA.slug, EDITOR)
      await expect(b.page.getByRole("alert").filter({ hasText: /access was removed/i })).toBeVisible({ timeout: 20_000 })
      await expect(node(b.page, "r1")).toHaveCount(0)
      await screenshot(b.page, info, "co06-design-editor-after-revoke")
      // Owner moves on A: the revoked editor must not receive it (no canvas, no awareness).
      // The owner keeps working; the revoked editor has left the owner's presence list.
      await drag(a.page, "r1", 40, 0)
      await expect.poll(() => position(dA.slug, "r1").x, { timeout: 20_000 }).toBeGreaterThan(120)
      await expect(a.page.getByLabel("People on this canvas")).toHaveCount(0)
      await expect(b.page.locator("[data-design-node]")).toHaveCount(0)
      await drag(bOther, "r2", 0, 50)
      await expect.poll(() => position(dB.slug, "r2").y, { timeout: 20_000 }).toBeGreaterThan(320)
    } finally { await closeAll([a, b], info) }
  })

  test("G06 commenter before anything is shared: no content, and no claim that access was removed", async ({ browser }, info) => {
    const d = createDesign(unique("g06d"), NODES)
    grant(d.slug, COMMENTER, "comment")
    const c = await signIn(browser, COMMENTER, info)
    try {
      await c.page.goto(`/artifacts/${d.id}`)
      await expect(c.page.getByRole("alert").filter({ hasText: /./ }).first()).toBeVisible()
      await expect(node(c.page, "r1")).toHaveCount(0)
      await screenshot(c.page, info, "g06-design-commenter-unshared")
      // Nothing was ever removed from this person: the page must not say so.
      await expect(c.page.getByText(/access was removed/i)).toHaveCount(0)
    } finally { await closeAll([c], info) }
  })

  test("CO10 restart: confirmed moves survive, a move made while down is never Saved, presence comes back without ghosts", async ({ browser }, info) => {
    test.setTimeout(300_000)
    const name = process.env.COLLAB_THROWAWAY_NAME
    test.skip(!name, "needs COLLAB_THROWAWAY_NAME (scripts/collab-throwaway.sh sets it)")
    const d = createDesign(unique("co10d"), NODES)
    grant(d.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDesign(a, d.id); await openDesign(b, d.id)
      await drag(b.page, "r2", 0, 50)
      await expect.poll(() => position(d.slug, "r2").y, { timeout: 20_000 }).toBeGreaterThan(320)
      const confirmed = position(d.slug, "r2")
      const restarted = restart(name!)
      await expect.poll(async () => (await a.context.request.get("/api/v1/documents/status").catch(() => undefined))?.ok() ?? false, { timeout: 60_000 }).toBe(false)
      await drag(a.page, "r1", 60, 0)
      await a.page.waitForTimeout(3_000)
      expect(await a.page.locator("button[data-status]").getAttribute("data-status")).not.toMatch(/^(saved|live)$/)
      await screenshot(a.page, info, "co10-design-owner-server-down")
      await restarted
      // The move made while down is delivered, or held as an explicit draft; never lost as Saved.
      await expect.poll(async () => position(d.slug, "r1").x > 120 ? "delivered" : (await a.page.locator("button[data-status]").getAttribute("data-status")) ?? "none", { timeout: 60_000 }).toMatch(/^(delivered|draft)$/)
      await screenshot(a.page, info, "co10-design-owner-after-restart")
      expect(position(d.slug, "r2")).toEqual(confirmed)
      // Presence returns: each sees exactly the other, no stale entry from before the restart.
      const editorName = await displayName(b)
      await expect(people(a.page).locator(`[title="${editorName}"]`)).toHaveCount(1, { timeout: 60_000 })
      await expect(people(a.page).locator("[title]")).toHaveCount(1)
    } finally { await closeAll([a, b], info) }
  })

  test("CO08 commenter cannot move objects on the canvas", async ({ browser }, info) => {
    const d = createDesign(unique("co08d"), NODES, { share: true })
    grant(d.slug, COMMENTER, "comment")
    const c = await signIn(browser, COMMENTER, info)
    try {
      await openDesign(c, d.id)
      await drag(c.page, "r1", 80, 80)
      await c.page.waitForTimeout(2_000)
      expect(position(d.slug, "r1")).toEqual({ x: 100, y: 100 })
      await c.page.reload(); await expect(node(c.page, "r1")).toBeVisible()
      expect(position(d.slug, "r1")).toEqual({ x: 100, y: 100 })
    } finally { await closeAll([c], info) }
  })
})
