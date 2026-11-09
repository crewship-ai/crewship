// Conformance V06 / COLLAB-01..03: two genuinely different people (the owner
// and a provisioned member, separate browser contexts and sessions) on one
// live Design canvas. Awareness (names, colours, pointers, selection) after
// each side pans and zooms its own view, edits of different and of the same
// layer, delete versus edit, reconnect, and an Edit grant revoked while open.
//
// Everything runs in the scenario's own disposable QA workspace; nothing else
// is read or changed. Quick mode needs only the owner login.
import { randomUUID } from "node:crypto"
import type { BrowserContext, Locator, Page, WebSocketRoute } from "@playwright/test"
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test, type PrdScenario } from "./fixtures"
import { applyOperations, createDesign, enableDesign, getDesign, grantWrite, openCanvas, revokeWrite, selectWorkspace, sessionUserId, snapshot, type DesignNode } from "./design/canvas"

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)
const viewport = (page: Page) => page.getByLabel("Design canvas viewport")
const canvasSvg = (page: Page) => page.locator('svg[aria-label="Design canvas"]')
const layer = (page: Page, id: string) => page.locator(`[data-design-node="${id}"]`).first()

interface Person { context: BrowserContext; page: Page; name: string; userId: string }

/**
 * Passes the Design socket through and can cut it, so a person goes offline for
 * the live canvas while the page stays loaded. context.setOffline does not close
 * an open WebSocket in Chromium, so it cannot simulate a dropped connection.
 */
async function gateDesignSocket(context: BrowserContext) {
 let cut = false
 const live = new Set<WebSocketRoute>()
 await context.routeWebSocket(/\/design\/ws/, socket => {
  if (cut) { void socket.close({ code: 3000, reason: "PRD socket cut" }); return }
  const server = socket.connectToServer()
  socket.onMessage(message => server.send(message))
  server.onMessage(message => socket.send(message))
  socket.onClose(() => { live.delete(socket) })
  live.add(socket)
 })
 return {
  cut: async () => { cut = true; await Promise.all([...live].map(s => s.close({ code: 3000, reason: "PRD socket cut" }).catch(() => {}))); live.clear() },
  restore: () => { cut = false },
 }
}

/** The owner and a provisioned member with Edit on one fresh Design: screen, button and text. */
async function twoPeople(prd: PrdScenario, label: string) {
 const owner = prd.env.accounts.owner
 if (!owner) throw new Error("owner account is not configured")
 const ownerCtx = await prd.browserContext(await loginStorageState(prd.origin, owner))
 const ws = await prd.qaWorkspace()
 await enableDesign(ownerCtx.request, ws.id)
 const memberName = `PRD Peer ${tag()}`
 const member = await prd.provision("MEMBER", memberName)
 const memberCtx = await prd.browserContext(await loginStorageState(prd.origin, member.account))
 const bSocket = await gateDesignSocket(memberCtx)
 const ownerId = await sessionUserId(ownerCtx.request)
 const session = (await (await ownerCtx.request.get("/api/auth/session")).json()) as { user?: { name?: string; email?: string } }
 const ownerName = session.user?.name || session.user?.email || ownerId
 await selectWorkspace(ownerCtx, ownerId, ws.id)
 await selectWorkspace(memberCtx, member.userId, ws.id)
 const slug = `prd-collab-${tag()}`
 const design = await createDesign(ownerCtx.request, ws.id, slug, `PRD ${label} ${slug}`)
 await grantWrite(ownerCtx.request, ws.id, design.page_id, member.userId)
 const nodes: DesignNode[] = [
  { id: "screen", type: "frame", name: "Screen", x: 64, y: 64, width: 600, height: 420, fill: "#ffffff" },
  { id: "cta", type: "rect", name: "Call to action", parentId: "screen", x: 40, y: 40, width: 200, height: 100, fill: "#2563eb" },
  { id: "copy", type: "text", name: "Copy", parentId: "screen", x: 40, y: 200, width: 300, height: 60, fill: "#172554", text: "Original copy" },
 ]
 await applyOperations(ownerCtx.request, ws.id, slug, nodes.map(node => ({ op: "upsert", node })), "PRD collab fixture")
 const a: Person = { context: ownerCtx, page: await ownerCtx.newPage(), name: ownerName, userId: ownerId }
 const b: Person = { context: memberCtx, page: await memberCtx.newPage(), name: memberName, userId: member.userId }
 for (const person of [a, b]) {
  const opened = await openCanvas(person.page, slug, ws.id)
  if (!opened) {
   test.skip(prd.mode === "quick", "this build ships without the Design UI (NEXT_PUBLIC_ARTIFACTS_DESIGN)")
   throw new Error("the build renders 'Design is not enabled.'")
  }
  await expect(layer(person.page, "cta")).toBeVisible()
 }
 return { ws, slug, design, a, b, owner: ownerCtx, bSocket }
}

async function viewOf(page: Page) {
 const [scale, x, y] = ((await viewport(page).getAttribute("data-design-view")) ?? "").split(" ").map(Number)
 return { scale, x, y }
}

async function center(locator: Locator) {
 const box = (await locator.boundingBox())!
 return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}

/** Pans this person's own view with a right-button drag and zooms it around the pointer. */
async function panAndZoom(page: Page, by: { x: number; y: number }, wheel: number) {
 const box = (await viewport(page).boundingBox())!
 const from = { x: box.x + 30, y: box.y + box.height - 60 }
 await page.mouse.move(from.x, from.y)
 await page.mouse.down({ button: "right" })
 await page.mouse.move(from.x + by.x, from.y + by.y, { steps: 6 })
 await page.mouse.up({ button: "right" })
 await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
 await page.keyboard.down("Control")
 await page.mouse.wheel(0, wheel)
 await page.keyboard.up("Control")
}

/** The layer as the server holds it now. */
async function serverNode(person: Person, ws: string, slug: string, id: string) {
 return (await getDesign(person.context.request, ws, slug)).nodes.find(node => node.id === id)
}

async function properties(page: Page, layerName: string) {
 await page.getByLabel("Layers").getByRole("button", { name: layerName, exact: true }).click()
 await expect(page.getByLabel("Layer name")).toHaveValue(layerName)
}

test.describe("design collaboration", () => {
 test(
  "two people see each other's name, colour, pointer and selection in canvas coordinates after pan and zoom",
  {
   annotation: [
    prdRequirement("V06.awareness.names-colours", "Design: each of two different people sees the other's server-given name and one stable colour"),
    prdRequirement("V06.awareness.pointer-pan-zoom", "Design: a pointer over a layer is drawn on that layer for the other person, whatever either view's pan and zoom"),
    prdRequirement("V06.awareness.selection", "Design: the other person's selected layer is outlined with their name; clearing removes it"),
    prdRequirement("V06.awareness.own-viewport", "Design: one person's pan and zoom never move the other's view"),
    prdRequirement("V06.awareness.departure", "Design: a person who leaves disappears from presence and leaves no cursor"),
   ],
  },
  async ({ prd }) => {
   const { a, b } = await twoPeople(prd, "awareness")
   // Names come from the server, not the browser.
   await expect(a.page.getByLabel("People on this canvas").locator("span")).toHaveCount(1, { timeout: 20_000 })
   await expect(b.page.getByLabel("People on this canvas").locator("span")).toHaveCount(1, { timeout: 20_000 })
   await expect(a.page.getByLabel("People on this canvas").getByTitle(b.name)).toBeVisible()
   await expect(b.page.getByLabel("People on this canvas").getByTitle(a.name)).toBeVisible()

   // Each pans and zooms its own view differently; the other view does not move.
   const aBefore = await viewOf(a.page)
   await panAndZoom(b.page, { x: 140, y: -60 }, -200)
   const bView = await viewOf(b.page)
   expect(bView.scale).toBeGreaterThan(0)
   await a.page.waitForTimeout(500)
   expect(await viewOf(a.page), "B's pan and zoom must not move A's view").toEqual(aBefore)
   await panAndZoom(a.page, { x: -80, y: 50 }, 150)
   const aView = await viewOf(a.page)
   expect(aView.scale).not.toBeCloseTo(bView.scale, 2)
   expect(await viewOf(b.page), "A's pan and zoom must not move B's view").toEqual(bView)

   // B's pointer over the button's centre lands on the button on A's screen.
   const target = await center(b.page.locator('[data-design-node="cta"] rect').first())
   await b.page.mouse.move(target.x - 8, target.y - 8)
   await b.page.mouse.move(target.x, target.y, { steps: 4 })
   const cursor = a.page.locator(`g[aria-label="${b.name} cursor"]`)
   await expect(cursor).toHaveCount(1, { timeout: 15_000 })
   const ctaOnA = (await a.page.locator('[data-design-node="cta"] rect').first().boundingBox())!
   await expect.poll(async () => {
    const tip = (await cursor.locator("path").boundingBox())!
    return tip.x >= ctaOnA.x && tip.x <= ctaOnA.x + ctaOnA.width && tip.y >= ctaOnA.y && tip.y <= ctaOnA.y + ctaOnA.height
   }, { message: "B's pointer tip is inside the button on A's screen", timeout: 10_000 }).toBe(true)
   const transform = (await cursor.getAttribute("transform")) ?? ""
   const [, cx, cy] = /translate\(([-\d.]+),([-\d.]+)\)/.exec(transform) ?? []
   // The button's centre on the canvas is 64+40+100, 64+40+50.
   expect(Math.abs(Number(cx) - 204)).toBeLessThan(3)
   expect(Math.abs(Number(cy) - 154)).toBeLessThan(3)
   // One colour per person: the avatar, the pointer and (below) the selection outline agree.
   const avatarColor = await a.page.getByLabel("People on this canvas").getByTitle(b.name).evaluate(el => getComputedStyle(el).backgroundColor)
   const arrowFill = await cursor.locator("path").getAttribute("fill")
   expect(arrowFill).toMatch(/^#[0-9a-f]{6}$/i)

   // Selection: B selects the button; A sees it outlined with B's name, then cleared.
   await b.page.mouse.click(target.x, target.y)
   const outline = a.page.locator('[data-peer-selection="cta"]')
   await expect(outline).toHaveAttribute("aria-label", `${b.name} selected Call to action`, { timeout: 15_000 })
   expect(await outline.locator("rect").first().getAttribute("stroke")).toBe(arrowFill)
   const bg = (await viewport(b.page).boundingBox())!
   await b.page.mouse.click(bg.x + 12, bg.y + bg.height - 40)
   await expect(outline).toHaveCount(0, { timeout: 15_000 })
   // A's own selection is unaffected by B's.
   await a.page.mouse.click((await center(a.page.locator('[data-design-node="copy"] rect').first())).x, (await center(a.page.locator('[data-design-node="copy"] rect').first())).y)
   await expect(b.page.locator('[data-peer-selection="copy"]')).toHaveCount(1, { timeout: 15_000 })
   await expect(a.page.locator('[data-peer-selection]')).toHaveCount(0)

   await prd.evidence("awareness-a.png", await a.page.screenshot())
   await prd.evidence("awareness-b.png", await b.page.screenshot())

   // Departure: B closes the tab; A's presence and B's cursor go.
   await b.page.close()
   await expect(a.page.getByLabel("People on this canvas").locator("span")).toHaveCount(0, { timeout: 45_000 })
   await expect(a.page.locator(`g[aria-label="${b.name} cursor"]`)).toHaveCount(0)
   await prd.evidence("awareness.json", { a: { name: a.name, view: aView }, b: { name: b.name, view: bView }, cursorTransform: transform, avatarColor, arrowFill })
  },
 )

 test(
  "edits of different layers and of one layer converge for both after reload; delete versus edit keeps the lost text",
  {
   annotation: [
    prdRequirement("V06.edit.different-layers", "Design: two people changing different layers keep both changes, live and after reload"),
    prdRequirement("V06.edit.same-layer", "Design: two people changing different properties of one layer keep both; one property ends with one value on both sides"),
    prdRequirement("V06.edit.delete-vs-edit", "Design: an offline edit of a layer deleted meanwhile is not silently lost: the editor offers the text back"),
   ],
  },
  async ({ prd }) => {
   const { ws, slug, a, b, bSocket } = await twoPeople(prd, "edits")
   // Different layers at the same time.
   await properties(a.page, "Call to action")
   await properties(b.page, "Copy")
   await Promise.all([
    a.page.getByLabel("x", { exact: true }).fill("120"),
    b.page.getByLabel("Text content").fill("Copy from B"),
   ])
   for (const person of [a, b]) {
    await expect.poll(async () => (await serverNode(person, ws.id, slug, "cta"))?.x, { timeout: 20_000 }).toBe(120)
    await expect.poll(async () => (await serverNode(person, ws.id, slug, "copy"))?.text, { timeout: 20_000 }).toBe("Copy from B")
   }
   // One layer: different properties.
   await properties(b.page, "Call to action")
   await Promise.all([
    a.page.getByLabel("Layer name").fill("Buy now"),
    b.page.getByLabel("width", { exact: true }).fill("260"),
   ])
   await expect.poll(async () => { const node = await serverNode(a, ws.id, slug, "cta"); return [node?.name, node?.width] }, { timeout: 20_000 }).toEqual(["Buy now", 260])
   // One layer, one property: whatever wins, both sides and the server agree.
   await a.page.getByLabel("y", { exact: true }).fill("70")
   await b.page.getByLabel("y", { exact: true }).fill("90")
   let settled = 0
   await expect.poll(async () => {
    const values = await Promise.all([a.page.getByLabel("y", { exact: true }).inputValue(), b.page.getByLabel("y", { exact: true }).inputValue(), serverNode(a, ws.id, slug, "cta").then(node => String(node?.y))])
    settled = Number(values[2])
    return new Set(values).size === 1 && [70, 90].includes(settled)
   }, { timeout: 20_000 }).toBe(true)
   // Reload both: the persisted canvas.
   for (const person of [a, b]) {
    await person.page.reload()
    await expect(person.page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
   }
   const after = await getDesign(a.context.request, ws.id, slug)
   const cta = after.nodes.find(node => node.id === "cta")!
   expect([cta.x, cta.name, cta.width, cta.y]).toEqual([120, "Buy now", 260, settled])
   expect(after.nodes.find(node => node.id === "copy")?.text).toBe("Copy from B")

   // Delete versus edit: B offline edits the text, A deletes the layer, B reconnects.
   await properties(b.page, "Copy")
   await bSocket.cut()
   await expect(b.page.getByText("Offline · changes kept in this browser")).toBeVisible({ timeout: 30_000 })
   await b.page.getByLabel("Text content").fill("Offline text of B")
   await properties(a.page, "Copy")
   await a.page.getByRole("button", { name: "Delete layer" }).click()
   await expect.poll(async () => (await serverNode(a, ws.id, slug, "copy")) === undefined, { timeout: 20_000 }).toBe(true)
   bSocket.restore()
   await expect(b.page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 60_000 })
   const notice = b.page.getByRole("alert").filter({ hasText: "Edit conflict" })
   const final = await (async () => { await b.page.waitForTimeout(3000); return getDesign(a.context.request, ws.id, slug) })()
   const copy = final.nodes.find(node => node.id === "copy")
   const noticeShown = await notice.count() > 0
   await prd.evidence("delete-vs-edit.json", { serverHasLayer: Boolean(copy), serverText: copy?.text ?? null, conflictNoticeShownToB: noticeShown, noticeText: noticeShown ? await notice.first().innerText() : null })
   await prd.evidence("delete-vs-edit-b.png", await b.page.screenshot())
   // Not silently lost: either the layer came back with B's text, or B is shown the text with Restore.
   expect(copy?.text === "Offline text of B" || noticeShown, "B's offline text must survive on the canvas or in a conflict notice").toBe(true)
  },
 )

 test(
  "a reconnecting person's offline edits sync, and an Edit grant revoked while open stops writes",
  {
   annotation: [
    prdRequirement("V06.reconnect", "Design: offline edits are kept with an Offline status and reach the other person after reconnect"),
    prdRequirement("V06.revoke-write", "Design: revoking a person's Edit while the canvas is open stops their writes in the UI and at the server"),
   ],
  },
  async ({ prd }) => {
   const { ws, slug, design, a, b, owner, bSocket } = await twoPeople(prd, "reconnect")
   await properties(b.page, "Call to action")
   await bSocket.cut()
   await expect(b.page.getByText("Offline · changes kept in this browser")).toBeVisible({ timeout: 30_000 })
   await b.page.getByLabel("x", { exact: true }).fill("333")
   await properties(a.page, "Copy")
   await a.page.getByLabel("Text content").fill("A while B was offline")
   bSocket.restore()
   await expect(b.page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 60_000 })
   await expect.poll(async () => { const d = await getDesign(a.context.request, ws.id, slug); return [d.nodes.find(n => n.id === "cta")?.x, d.nodes.find(n => n.id === "copy")?.text] }, { timeout: 30_000 }).toEqual([333, "A while B was offline"])
   await expect.poll(() => b.page.locator('svg[aria-label="Design canvas"] text').allTextContents(), { timeout: 20_000 }).toContain("A while B was offline")
   await expect(layer(a.page, "cta")).toHaveAttribute("transform", /translate\(397,/, { timeout: 20_000 })
   const saved = await snapshot(owner.request, ws.id, slug, "PRD collab: after reconnect")

   // A reader sees only a shared version, so share the saved one first.
   const shared = await owner.request.post(`/api/v1/pages/${encodeURIComponent(slug)}/design/share?workspace_id=${encodeURIComponent(ws.id)}`, { data: { version: saved.version } })
   expect(shared.status(), "share the saved version").toBeLessThan(300)
   // Revoke B's Edit while B has the canvas open; B keeps Read.
   const read = await owner.request.put(`/api/v1/pages/@${encodeURIComponent(design.page_id)}/grants?workspace_id=${encodeURIComponent(ws.id)}`, { data: { subject_type: "user", subject: b.userId, level: "read" } })
   expect(read.status(), "grant B Read").toBeLessThan(300)
   expect(await revokeWrite(owner.request, ws.id, design.page_id, b.userId)).toBeLessThan(300)
   // The server refuses B's writes.
   const refused = await b.context.request.post(`/api/v1/pages/${encodeURIComponent(slug)}/design/operations?workspace_id=${encodeURIComponent(ws.id)}`, { data: { operations: [{ op: "upsert", node: { id: "cta", x: 1 } }], message: "after revoke" } })
   expect(refused.status(), "B's REST write after revoke").toBeGreaterThanOrEqual(400)
   // B's canvas stops offering edits (after the page notices the change or B reopens it).
   await expect.poll(async () => {
    const status = await b.page.getByRole("status").first().innerText().catch(() => "")
    const field = b.page.getByLabel("Layer name")
    const disabled = (await field.count()) === 0 || await field.isDisabled({ timeout: 1000 }).catch(() => true)
    return /read only|Access changed|write access/i.test(status) || disabled
   }, { timeout: 45_000 }).toBe(true)
   await b.page.reload()
   await expect(b.page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
   await expect(b.page.getByText("Read only", { exact: true })).toBeVisible()
   const beforeTry = await getDesign(owner.request, ws.id, slug)
   const box = await center(b.page.locator('[data-design-node="cta"] rect').first())
   await b.page.mouse.move(box.x, box.y); await b.page.mouse.down(); await b.page.mouse.move(box.x + 120, box.y + 80, { steps: 5 }); await b.page.mouse.up()
   await b.page.waitForTimeout(1500)
   expect((await getDesign(owner.request, ws.id, slug)).nodes.find(n => n.id === "cta")?.x).toBe(beforeTry.nodes.find(n => n.id === "cta")?.x)
   await prd.evidence("reconnect-revoke.json", { savedVersion: saved.version, revokedRestStatus: refused.status() })
   await prd.evidence("revoked-b.png", await b.page.screenshot())
  },
 )
})
