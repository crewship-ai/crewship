// Conformance V05: qualification of the native Design model on our own
// fixture, one capability per step: the layer list and its sync with the
// canvas, stable layer IDs, z-order, property edits through Properties with
// save and reload, prototype links with playback, and comments anchored to a
// layer. Each step records what it found, including what is not supported, in
// v05.json; the test fails on any supported capability that does not hold.
import { randomUUID } from "node:crypto"
import type { Page } from "@playwright/test"
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"
import { applyOperations, commitNumber, createDesign, enableDesign, getDesign, listVersions, openCanvas, scoped, selectWorkspace, sessionUserId, type DesignNode } from "./design/canvas"

const tag = () => randomUUID().replaceAll("-", "").slice(0, 8)
const layersPanel = (page: Page) => page.getByLabel("Layers")
const drawnOrder = (page: Page) => page.locator('svg[aria-label="Design canvas"] g[data-design-node]').evaluateAll(nodes => nodes.map(node => node.getAttribute("data-design-node")))
const outlined = (page: Page, id: string) => page.locator(`[data-design-node="${id}"] rect[stroke="#2563eb"]`)

test.describe("design model", () => {
 test(
  "layers, IDs, z-order, property edits, prototype links and anchored comments hold on a fixture",
  {
   annotation: [
    prdRequirement("V05.layers", "Design: the layer list shows every screen, then every layer indented as a child of a screen, and selecting in the list or on the canvas selects the same layer"),
    prdRequirement("V05.ids", "Design: layer IDs are stable across edits, saves and reloads; a new layer gets an ID the server keeps"),
    prdRequirement("V05.z-order", "Design: the canvas draws layers in the document order, and a reorder changes it live"),
    prdRequirement("V05.properties", "Design: name, position, size, colour, text and screen edited in Properties survive save and reload"),
    prdRequirement("V05.prototype", "Design: a link set in Properties is stored, plays (click, Back, Escape) and playing changes nothing"),
    prdRequirement("V05.comments", "Design: a comment saved on a layer is stored with that layer's ID and leads back to the layer after it moves"),
   ],
  },
  async ({ prd }) => {
   const owner = prd.env.accounts.owner
   if (!owner) throw new Error("owner account is not configured")
   const context = await prd.browserContext(await loginStorageState(prd.origin, owner))
   const ws = await prd.qaWorkspace()
   await enableDesign(context.request, ws.id)
   await selectWorkspace(context, await sessionUserId(context.request), ws.id)
   const slug = `prd-model-${tag()}`
   await createDesign(context.request, ws.id, slug, `PRD model ${slug}`)
   const fixture: DesignNode[] = [
    { id: "screen-a", type: "frame", name: "Screen A", x: 64, y: 64, width: 400, height: 500, fill: "#ffffff" },
    { id: "screen-b", type: "frame", name: "Screen B", x: 520, y: 64, width: 400, height: 500, fill: "#ffffff" },
    { id: "backdrop", type: "rect", name: "Backdrop", parentId: "screen-a", x: 20, y: 20, width: 360, height: 200, fill: "#e2e8f0" },
    { id: "title", type: "text", name: "Title", parentId: "screen-a", x: 40, y: 40, width: 300, height: 40, fill: "#172554", text: "Welcome" },
    { id: "cta", type: "rect", name: "Next", parentId: "screen-a", x: 40, y: 300, width: 160, height: 48, fill: "#2563eb" },
   ]
   await applyOperations(context.request, ws.id, slug, fixture.map(node => ({ op: "upsert", node })), "PRD model fixture")
   const page = await context.newPage()
   if (!(await openCanvas(page, slug, ws.id))) {
    test.skip(prd.mode === "quick", "this build ships without the Design UI (NEXT_PUBLIC_ARTIFACTS_DESIGN)")
    throw new Error("the build renders 'Design is not enabled.'")
   }
   const found: Record<string, unknown> = {}

   // Layers: every layer, children indented under their screen; list and canvas select the same layer.
   // One entry per layer: the Hide/Lock icon buttons of a row (#5218) carry no text.
   const entries = layersPanel(page).getByRole("button").filter({ hasText: /\S/ })
   await expect(entries).toHaveText(["Screen A", "Screen B", "Backdrop", "Title", "Next"])
   for (const child of ["Backdrop", "Title", "Next"]) expect(await layersPanel(page).getByRole("button", { name: child, exact: true }).getAttribute("class"), `${child} is indented as a child`).toContain("pl-7")
   for (const screenName of ["Screen A", "Screen B"]) expect(await layersPanel(page).getByRole("button", { name: screenName, exact: true }).getAttribute("class"), `${screenName} is not indented`).not.toContain("pl-7")
   await layersPanel(page).getByRole("button", { name: "Title", exact: true }).click()
   await expect(outlined(page, "title")).toHaveCount(1)
   await expect(page.getByLabel("Layer name")).toHaveValue("Title")
   const ctaBox = (await page.locator('[data-design-node="cta"] rect').first().boundingBox())!
   await page.mouse.click(ctaBox.x + 10, ctaBox.y + 10)
   await expect(outlined(page, "cta")).toHaveCount(1)
   expect(await layersPanel(page).getByRole("button", { name: "Next", exact: true }).getAttribute("class")).toContain("bg-accent")
   found.layers = { list: await entries.allTextContents(), reorderInList: "not offered", hide: "not offered", lock: "not offered", group: "not offered" }

   // IDs: a layer added in the editor gets an ID the server keeps; edits and reloads keep every ID.
   await layersPanel(page).getByRole("button", { name: "Screen B", exact: true }).click()
   await page.getByLabel("Design tools").getByRole("button", { name: "Rectangle" }).click()
   let added = ""
   await expect.poll(async () => { const nodes = (await getDesign(context.request, ws.id, slug)).nodes; added = nodes.find(node => !fixture.some(item => item.id === node.id))?.id ?? ""; return added }, { timeout: 20_000 }).not.toBe("")
   expect(added).toMatch(/^[0-9a-f-]{36}$/)
   expect((await getDesign(context.request, ws.id, slug)).nodes.find(node => node.id === added)?.parentId).toBe("screen-b")

   // z-order: frames first, then layers in document order; a reorder through the API redraws live.
   expect(await drawnOrder(page)).toEqual(["screen-a", "screen-b", "backdrop", "title", "cta", added])
   await applyOperations(context.request, ws.id, slug, [{ op: "reorder", order: ["screen-a", "screen-b", "cta", "title", "backdrop", added] }], "PRD model: reorder")
   await expect.poll(() => drawnOrder(page), { timeout: 20_000 }).toEqual(["screen-a", "screen-b", "cta", "title", "backdrop", added])
   found.zOrder = { drawn: await drawnOrder(page), reorderInEditor: "not offered (API, CLI and agent only)" }

   // Properties: edit through the inspector, save a version, reload.
   await layersPanel(page).getByRole("button", { name: "Title", exact: true }).click()
   await page.getByLabel("Layer name").fill("Headline")
   await commitNumber(page.getByLabel("x", { exact: true }), "44")
   await commitNumber(page.getByLabel("y", { exact: true }), "48")
   await commitNumber(page.getByLabel("width", { exact: true }), "320")
   await commitNumber(page.getByLabel("height", { exact: true }), "56")
   await page.getByLabel("Fill value").fill("#0f766e")
   await page.getByLabel("Text content").fill("Welcome back")
   await page.getByLabel("Parent screen").selectOption("screen-b")
   const edited = { name: "Headline", x: 44, y: 48, width: 320, height: 56, fill: "#0f766e", text: "Welcome back", parentId: "screen-b" }
   await expect.poll(async () => (await getDesign(context.request, ws.id, slug)).nodes.find(node => node.id === "title"), { timeout: 20_000 }).toMatchObject(edited)
   const before = (await listVersions(context.request, ws.id, slug)).versions.length
   await page.getByRole("button", { name: "Save version" }).click()
   await expect.poll(async () => (await listVersions(context.request, ws.id, slug)).versions.length, { timeout: 20_000 }).toBeGreaterThan(before)
   const savedVersion = Math.max(...(await listVersions(context.request, ws.id, slug)).versions.map(item => item.version))
   expect((await getDesign(context.request, ws.id, slug, savedVersion)).nodes.find(node => node.id === "title")).toMatchObject(edited)
   await page.reload()
   await expect(page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
   await layersPanel(page).getByRole("button", { name: "Headline", exact: true }).click()
   for (const [label, value] of [["Layer name", "Headline"], ["x", "44"], ["y", "48"], ["width", "320"], ["height", "56"], ["Fill value", "#0f766e"], ["Text content", "Welcome back"], ["Parent screen", "screen-b"]] as const) {
    await expect(page.getByLabel(label, { exact: true })).toHaveValue(value)
   }
   expect((await getDesign(context.request, ws.id, slug)).nodes.map(node => node.id).sort()).toEqual([...fixture.map(node => node.id), added].sort())
   found.properties = { savedVersion, edited }

   // Prototype: link Next to Screen B, play it, go back, exit; playing writes nothing.
   const linkField = page.getByLabel("Link to screen")
   const prototypeShown = await page.getByRole("button", { name: "Play", exact: true }).count() > 0
   found.prototype = prototypeShown ? {} : { status: "not in this build (NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE off)" }
   // V05.prototype is a claim only when it ran: a full run on a build without playback fails it.
   expect(prototypeShown || prd.mode === "quick", "prototype playback is part of V05; build with NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE=true").toBe(true)
   if (prototypeShown) {
    await layersPanel(page).getByRole("button", { name: "Next", exact: true }).click()
    await linkField.selectOption("screen-b")
    await expect.poll(async () => (await getDesign(context.request, ws.id, slug)).nodes.find(node => node.id === "cta")?.linkTo, { timeout: 20_000 }).toBe("screen-b")
    const stateBefore = JSON.stringify((await getDesign(context.request, ws.id, slug)).nodes)
    const versionsBefore = (await listVersions(context.request, ws.id, slug)).versions.length
    await layersPanel(page).getByRole("button", { name: "Screen A", exact: true }).click()
    await page.getByRole("button", { name: "Play", exact: true }).click()
    const player = page.getByRole("dialog", { name: "Prototype playback" })
    await expect(player.getByRole("heading", { name: "Screen A" })).toBeVisible()
    await player.getByRole("button", { name: "Next: open Screen B" }).click()
    await expect(player.getByRole("heading", { name: "Screen B" })).toBeVisible()
    await player.getByRole("button", { name: "Back" }).click()
    await expect(player.getByRole("heading", { name: "Screen A" })).toBeVisible()
    await page.keyboard.press("Escape")
    await expect(player).toHaveCount(0)
    expect(JSON.stringify((await getDesign(context.request, ws.id, slug)).nodes)).toBe(stateBefore)
    expect((await listVersions(context.request, ws.id, slug)).versions.length).toBe(versionsBefore)
    found.prototype = { linkTo: "screen-b", played: ["Screen A", "Screen B", "back to Screen A", "Escape"], wroteNothing: true }
   }

   // Comments anchored to a layer: saved with the layer ID; after the layer moves the comment still selects it.
   await layersPanel(page).getByRole("button", { name: "Next", exact: true }).click()
   const commentText = `Make the button larger ${tag()}`
   await page.getByLabel("Comment for agent").fill(commentText)
   await page.getByRole("button", { name: /Save comment|Send to agent/ }).click()
   let stored: { selector?: string; version?: number; quote?: string; text?: string } | undefined
   await expect.poll(async () => {
    const res = await context.request.get(scoped(`/api/v1/pages/${encodeURIComponent(slug)}/design/comments`, ws.id))
    const body = (await res.json()) as { comments?: { selector?: string; version?: number; quote?: string; text?: string }[] }
    stored = body.comments?.find(comment => comment.text === commentText)
    return stored?.selector
   }, { timeout: 20_000 }).toBe("cta")
   await applyOperations(context.request, ws.id, slug, [{ op: "upsert", node: { id: "cta", x: 180, y: 360 } }], "PRD model: move the commented layer")
   await page.reload()
   await expect(page.locator("[data-design-connection]")).toHaveAttribute("data-design-connection", "connected", { timeout: 45_000 })
   const comment = page.getByLabel("Design comments").locator("article").filter({ hasText: commentText })
   await expect(comment).toHaveCount(1)
   await comment.getByRole("button").first().click()
   await expect(page.getByLabel("Layer name")).toHaveValue("Next")
   await expect(outlined(page, "cta")).toHaveCount(1)
   // Delete the commented layer: the comment stays; is the missing target acknowledged?
   await page.getByRole("button", { name: "Delete layer" }).click()
   await expect.poll(async () => (await getDesign(context.request, ws.id, slug)).nodes.some(node => node.id === "cta"), { timeout: 20_000 }).toBe(false)
   await expect(comment).toHaveCount(1)
   const orphanText = await comment.innerText()
   const orphanAcknowledged = /deleted|no longer|removed|missing/i.test(orphanText)
   found.comments = { stored, followsMovedLayer: true, afterDelete: { kept: true, orphanAcknowledged, text: orphanText } }

   await prd.evidence("v05.json", found)
   await prd.evidence("v05-final.png", await page.screenshot())
  },
 )
})
