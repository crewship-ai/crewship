/**
 * History, restore and links on a real server (#5144): G04, G05 and BE02 for Docs.
 *
 * G05: a restore is a new revision with the older content; later work stays in
 * history; a commenter cannot restore. BE02: an editor who was offline across
 * the restore must not merge its old-epoch edits into the restored document
 * without an explicit recovery step. G04: every historical link form ends on
 * /artifacts/<immutable id> once, with the person's own rights.
 */
import { expect, test, type Page } from "@playwright/test"
import { COMMENTER, EDITOR, OWNER, cli, closeAll, createDoc, grant, readDoc, requireThrowaway, screenshot, signIn, statusKey, unique } from "./fixtures/collab"

test.beforeAll(() => requireThrowaway())

const editor = (page: Page) => page.locator("[data-docs-page] .ProseMirror")
const workspace = () => (JSON.parse(cli(["whoami", "-f", "json"])) as { workspace: { id: string } }).workspace.id

interface DocState { epoch: string; rev: number; schema: number; page_id?: string }
const state = (slug: string) => JSON.parse(readDoc(slug)) as DocState
const checkpoints = (slug: string) => (JSON.parse(cli(["doc", "checkpoints", slug, "-f", "json"])) as { checkpoints: { id: string; rev: number; actor: string; created_at: string }[] }).checkpoints

async function typeAtEnd(page: Page, text: string) {
  await editor(page).locator(":scope > p").first().click()
  await page.keyboard.press("End")
  await page.keyboard.type(text, { delay: 15 })
}

test("G05 + BE02 restore makes a new revision; an offline editor's old-epoch edit is not merged silently", async ({ browser }, info) => {
  const doc = createDoc(unique("g05"), ["Original text."])
  grant(doc.slug, EDITOR, "write"); grant(doc.slug, COMMENTER, "comment")
  const initial = checkpoints(doc.slug)[0]
  expect(initial.actor).toMatch(/^user:/)
  const b = await signIn(browser, EDITOR, info)
  const c = await signIn(browser, COMMENTER, info)
  try {
    await b.page.goto(`/artifacts/${doc.id}`)
    await expect(editor(b.page)).toBeVisible()
    await typeAtEnd(b.page, " [pre-restore]")
    await expect.poll(() => readDoc(doc.slug), { timeout: 20_000 }).toContain("[pre-restore]")
    await b.context.setOffline(true)
    await typeAtEnd(b.page, " [offline-old-epoch]")
    const before = state(doc.slug)
    // The commenter is refused; the owner restores the first checkpoint.
    const packet = { page_id: doc.id, id: unique("restore"), schema: before.schema, epoch: before.epoch, ifRev: before.rev, checkpoint: initial.id }
    const refused = await c.context.request.post(`/api/v1/pages/${doc.slug}/document/restore?workspace_id=${workspace()}`, { data: packet })
    expect([401, 403, 404, 405]).toContain(refused.status())
    cli(["doc", "restore", doc.slug, "--file", "-"], JSON.stringify(packet))
    const after = state(doc.slug)
    expect(after.rev).toBeGreaterThan(before.rev)
    expect(readDoc(doc.slug)).not.toContain("[pre-restore]")
    await b.context.setOffline(false)
    // Not merged without an explicit recovery step: the server never takes the
    // old-epoch edit, and the editor offers it as a draft instead of dropping it.
    await expect(b.page.locator("button[data-status]")).toHaveAttribute("data-status", "draft", { timeout: 30_000 })
    await screenshot(b.page, info, "g05-docs-editor-after-restore")
    expect(readDoc(doc.slug)).not.toContain("[offline-old-epoch]")
  } finally { await closeAll([b, c], info) }
})

test("G05 work replaced by a restore stays reachable in History", async () => {
  const doc = createDoc(unique("g05h"), ["Original text."])
  const initial = checkpoints(doc.slug)[0]
  const fresh = JSON.parse(readDoc(doc.slug)) as DocState & { blocks: { id: string; hash: string }[] }
  const block = fresh.blocks.find(item => item.id === "p1")!
  cli(["doc", "patch", doc.slug, "--file", "-"], JSON.stringify({ id: unique("patch"), schema: fresh.schema, page_id: doc.id, epoch: fresh.epoch, ifRev: fresh.rev, target: "p1", ifHash: block.hash, text: "Later text." }))
  expect(readDoc(doc.slug)).toContain("Later text.")
  const now = state(doc.slug)
  cli(["doc", "restore", doc.slug, "--file", "-"], JSON.stringify({ page_id: doc.id, id: unique("restore"), schema: now.schema, epoch: now.epoch, ifRev: now.rev, checkpoint: initial.id }))
  const listed = checkpoints(doc.slug)
  const contents = listed.map(pin => cli(["doc", "checkpoint", doc.slug, pin.id, "-f", "json"]))
  // Everything above is setup that must pass; only the next assertion is the known gap.
  test.fail(true, "Docs History lists only explicit checkpoints; a human cannot create one and a restore does not keep the replaced state, so later human work leaves History. R71 §0.14/4 gap recorded in the night matrix.")
  expect(contents.some(text => text.includes("Later text."))).toBe(true)
})

test("G04 a version link of a Doc or Design opens that version at /artifacts/<id>", async ({ browser }, info) => {
  const doc = createDoc(unique("g04v"), ["Versioned document."])
  const a = await signIn(browser, OWNER, info)
  try {
    await a.page.goto(`/pages/${doc.slug}?version=1&workspace_id=${workspace()}`)
    // Everything above is setup that must pass; only the next assertion is the known gap.
    test.fail(true, "/pages/<slug>?version=N moves to Artifacts only for Page projects; a Doc or Design stays on the old Pages surface with 'Can't show this artifact here'. Gap recorded in the night matrix.")
    await expect(a.page).toHaveURL(new RegExp(`/artifacts/${doc.id}\\?.*version=1`), { timeout: 10_000 })
  } finally { await closeAll([a], info) }
})

test("G04 historical links end once on /artifacts/<id> with the person's own rights", async ({ browser }, info) => {
  const doc = createDoc(unique("g04"), ["Linked document."])
  grant(doc.slug, EDITOR, "read")
  const ws = workspace()
  const a = await signIn(browser, OWNER, info)
  const b = await signIn(browser, EDITOR, info)
  const c = await signIn(browser, COMMENTER, info)
  try {
    const links = [
      `/pages/${doc.slug}?workspace_id=${ws}`,
      `/pages/${doc.slug}`,
      `/pages/%40${doc.id}?workspace_id=${ws}`,
      `/pages?ref=${doc.id}&workspace_id=${ws}`,
      `/artifacts/${doc.id}`,
    ]
    for (const link of links) {
      const page = await a.context.newPage()
      await page.goto(link)
      await expect(page).toHaveURL(new RegExp(`/artifacts/${doc.id}(\\?|$)`))
      await expect(editor(page)).toContainText("Linked document.")
      // One replacement at most, and the address stays put: no redirect loop.
      const landed = page.url()
      await page.waitForTimeout(1_500)
      expect(page.url(), link).toBe(landed)
      expect(await page.evaluate(() => history.length), `${link} history entries`).toBeLessThanOrEqual(2)
      await page.close()
    }
    // A reader lands on the same address, read-only.
    await b.page.goto(`/pages/${doc.slug}?workspace_id=${ws}`)
    await expect(b.page).toHaveURL(new RegExp(`/artifacts/${doc.id}`))
    await expect(b.page.locator("button[data-status]")).toHaveAttribute("data-status", "readonly")
    // Someone without any grant gets no content through any link form.
    for (const link of [`/pages/${doc.slug}?workspace_id=${ws}`, `/artifacts/${doc.id}`]) {
      await c.page.goto(link)
      await c.page.waitForLoadState("networkidle")
      await expect(c.page.getByText("Linked document.")).toHaveCount(0)
    }
    await screenshot(c.page, info, "g04-no-grant-link")
  } finally { await closeAll([a, b, c], info) }
})
