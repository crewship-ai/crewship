/**
 * Docs collaboration with two genuinely different identities (#5144).
 * CO01–CO06, CO08, CO10 and G08 for Docs, on a throwaway server only.
 *
 * Docs merges concurrent edits through one Yjs document (Collaboration
 * extension); the transport is HTTP patch + a 500 ms poll, without awareness.
 * Presence and remote carets are therefore declared as known gaps with
 * test.fail(): the scenario states what must happen and fails today. When the
 * capability lands, Playwright reports the "expected failure" as passing and
 * the annotation has to go.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test"
import * as Y from "yjs"
import { COMMENTER, EDITOR, OWNER, cli, closeAll, createDoc, grant, readDoc, requireThrowaway, restart, revoke, screenshot, signIn, statusKey, unique, type Session } from "./fixtures/collab"

test.beforeAll(() => requireThrowaway())

const editor = (page: Page) => page.locator('[data-docs-page] .ProseMirror')
const paragraph = (page: Page, index: number) => editor(page).locator(":scope > p").nth(index)

async function openDoc(session: Session, id: string, page = session.page) {
  await page.goto(`/artifacts/${id}`)
  await expect(editor(page)).toBeVisible()
  await expect(page.locator("button[data-status]")).toHaveAttribute("data-status", /^(live|saved|readonly)$/)
}

async function typeAtEnd(page: Page, index: number, text: string) {
  await paragraph(page, index).click()
  await page.keyboard.press("End")
  await page.keyboard.type(text, { delay: 15 })
}

/** Saved means the server confirmed it: the chip says so AND the CLI reads it back. */
async function expectConfirmed(page: Page, slug: string, text: string) {
  await expect(page.locator("button[data-status]")).toHaveAttribute("data-status", "saved", { timeout: 20_000 })
  await expect.poll(() => readDoc(slug), { timeout: 20_000 }).toContain(text)
}

const b64 = (value: string) => Uint8Array.from(Buffer.from(value, "base64"))

/** A well-formed patch for the current document: insert `text` at the start of the first paragraph. */
async function forgePatch(request: APIRequestContext, docUrl: string, pageId: string, text: string) {
  const state = await (await request.get(docUrl)).json() as { snapshot: string; updates: string[]; epoch: string; schema: number }
  const doc = new Y.Doc()
  Y.applyUpdate(doc, b64(state.snapshot))
  for (const update of state.updates) Y.applyUpdate(doc, b64(update))
  const before = Y.encodeStateVector(doc)
  const first = doc.getXmlFragment("default").get(0) as Y.XmlElement
  ;(first.get(0) as Y.XmlText).insert(0, text)
  const update = Buffer.from(Y.encodeStateAsUpdate(doc, before)).toString("base64")
  return { id: unique("forged"), update, schema: state.schema, epoch: state.epoch, kind: "human", page_id: pageId }
}

function workspaceId(): string {
  return (JSON.parse(cli(["whoami", "-f", "json"])) as { workspace: { id: string } }).workspace.id
}

test.describe("Docs, two identities", () => {
  test("CO03 concurrent edits in the same paragraph and in different blocks converge after reload", async ({ browser }, info) => {
    const base = ["Alpha shared paragraph.", "Beta second block."]
    const doc = createDoc(unique("co03"), base)
    grant(doc.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDoc(a, doc.id); await openDoc(b, doc.id)
      // Same paragraph, both at once; then different blocks, both at once.
      await Promise.all([typeAtEnd(a.page, 0, " [owner-same]"), typeAtEnd(b.page, 0, " [editor-same]")])
      await Promise.all([typeAtEnd(a.page, 1, " [owner-other]"), typeAtEnd(b.page, 0, " [editor-other]")])
      const tokens = ["[owner-same]", "[editor-same]", "[owner-other]", "[editor-other]"]
      // Let both views settle on one text first; only then judge its content.
      await expect.poll(async () => (await editor(a.page).innerText()) === (await editor(b.page).innerText()), { timeout: 30_000 }).toBe(true)
      await screenshot(a.page, info, "co03-docs-owner-live"); await screenshot(b.page, info, "co03-docs-editor-live")
      const live = await editor(a.page).innerText()
      if (tokens.some((t) => !live.includes(t))) {
        // Known defect #5162 (owned elsewhere): a remote patch moves the local
        // caret, so concurrent typing in one paragraph interleaves characters.
        // It counts as #5162 only when every typed character is still there and
        // the server holds the same text; a dropped edit or a divergence fails hard.
        expect(interleaved(live, [...base, ...tokens]), `not an interleave but lost text: ${live}`).toBe(true)
        // Block by block, the server holds what both views show.
        const norm = (text: string) => text.replace(/\s+/g, " ").trim()
        const server = () => (JSON.parse(cli(["doc", "outline", doc.slug])) as { type: string; text: string }[]).filter(block => block.type === "paragraph").map(block => norm(block.text))
        const shown = await Promise.all(base.map(async (_, i) => norm(await paragraph(a.page, i).innerText())))
        await expect.poll(server, { timeout: 10_000 }).toEqual(shown)
        info.annotations.push({ type: "issue", description: `#5162 reproduced: ${live}` })
        test.fail()
      }
      for (const s of [a, b]) for (const text of tokens) await expect(editor(s.page)).toContainText(text, { timeout: 5_000 })
      await Promise.all([openDoc(a, doc.id), openDoc(b, doc.id)])
      await expect.poll(async () => (await editor(a.page).innerText()) === (await editor(b.page).innerText())).toBe(true)
      const server = readDoc(doc.slug)
      for (const text of tokens) expect(server).toContain(text)
    } finally { await closeAll([a, b], info) }
  })

  test("CO04 undo takes back only the local person's change", async ({ browser }, info) => {
    const doc = createDoc(unique("co04"), ["Owner paragraph.", "Editor paragraph."])
    grant(doc.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDoc(a, doc.id); await openDoc(b, doc.id)
      await typeAtEnd(a.page, 0, " [owner-undo-me]")
      await expect(editor(b.page)).toContainText("[owner-undo-me]")
      await typeAtEnd(b.page, 1, " [editor-keep]")
      await expect(editor(a.page)).toContainText("[editor-keep]")
      await paragraph(a.page, 0).click()
      await a.page.keyboard.press("ControlOrMeta+z")
      await expect(editor(a.page)).not.toContainText("[owner-undo-me]")
      await expect(editor(a.page)).toContainText("[editor-keep]")
      await expect(editor(b.page)).not.toContainText("[owner-undo-me]")
      await expect.poll(() => readDoc(doc.slug), { timeout: 20_000 }).not.toContain("[owner-undo-me]")
      expect(readDoc(doc.slug)).toContain("[editor-keep]")
      await a.page.keyboard.press("ControlOrMeta+Shift+z")
      await expect(editor(a.page)).toContainText("[owner-undo-me]")
      await expectConfirmed(a.page, doc.slug, "[owner-undo-me]")
    } finally { await closeAll([a, b], info) }
  })

  test("CO01 presence: the other editor is named on the document, never on another one", async ({ browser }, info) => {
    const doc = createDoc(unique("co01"), ["Presence paragraph."])
    grant(doc.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDoc(a, doc.id); await openDoc(b, doc.id)
      // Everything above is setup that must pass; only the next assertion is the known gap.
      test.fail(true, "Docs has no awareness channel (HTTP patch + 500 ms poll); no presence list exists. Gap recorded in the night matrix.")
      await expect(a.page.getByLabel(/people (on|in) this document/i)).toBeVisible({ timeout: 8_000 })
    } finally { await closeAll([a, b], info) }
  })

  test("CO02 remote caret and range: the editor sees where the owner is typing", async ({ browser }, info) => {
    const doc = createDoc(unique("co02"), ["Caret paragraph with enough words to select a range."])
    grant(doc.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDoc(a, doc.id); await openDoc(b, doc.id)
      await paragraph(a.page, 0).click()
      await a.page.keyboard.press("Home"); await a.page.keyboard.press("Shift+End")
      await screenshot(b.page, info, "co02-docs-editor-no-remote-caret")
      // Everything above is setup that must pass; only the next assertion is the known gap.
      test.fail(true, "Docs renders no remote carets or selections (no CollaborationCaret, no awareness). Gap recorded in the night matrix.")
      await expect(editor(b.page).locator('[class*="caret"], [class*="cursor"], [data-remote-caret]')).toHaveCount(1, { timeout: 8_000 })
    } finally { await closeAll([a, b], info) }
  })

  test("CO05 + G08 offline: never Saved while the server lacks the edit; reconnect delivers it", async ({ browser }, info) => {
    const doc = createDoc(unique("co05"), ["Offline paragraph.", "Online paragraph."])
    grant(doc.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDoc(a, doc.id); await openDoc(b, doc.id)
      await b.context.setOffline(true)
      await typeAtEnd(b.page, 0, " [while-offline]")
      await typeAtEnd(a.page, 1, " [owner-meanwhile]")
      await expectConfirmed(a.page, doc.slug, "[owner-meanwhile]")
      // Give the poll and the write queue time to (wrongly) claim success.
      await b.page.waitForTimeout(4_000)
      expect(await statusKey(b.page)).not.toMatch(/^(saved|live)$/)
      expect(readDoc(doc.slug)).not.toContain("[while-offline]")
      await screenshot(b.page, info, "co05-docs-editor-offline-status")
      await b.context.setOffline(false)
      await expectConfirmed(b.page, doc.slug, "[while-offline]")
      await expect(editor(a.page)).toContainText("[while-offline]")
      await expect(editor(b.page)).toContainText("[owner-meanwhile]")
    } finally { await closeAll([a, b], info) }
  })

  test("CO05 refresh while a save is refused: the draft is recovered or explicitly offered, never silently Saved", async ({ browser }, info) => {
    const doc = createDoc(unique("co05r"), ["Pending paragraph."])
    grant(doc.slug, EDITOR, "write")
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDoc(b, doc.id)
      b.page.on("dialog", dialog => void dialog.accept())
      await b.page.route("**/document/patch**", route => route.abort("internetdisconnected"))
      await typeAtEnd(b.page, 0, " [pending-at-refresh]")
      await b.page.waitForTimeout(2_000)
      expect(await statusKey(b.page)).not.toMatch(/^(saved|live)$/)
      await b.page.unroute("**/document/patch**")
      await b.page.reload()
      await expect(editor(b.page).or(b.page.getByRole("alert"))).toBeVisible()
      // Either the journal resent it and the server holds it, or the page says a draft is waiting.
      await expect.poll(async () => readDoc(doc.slug).includes("[pending-at-refresh]") ? "server" : (await statusKey(b.page)) === "draft" ? "draft" : "none", { timeout: 25_000 }).not.toBe("none")
      await screenshot(b.page, info, "co05-docs-after-refresh")
    } finally { await closeAll([b], info) }
  })

  test("CO06 revoke while open stops the editor; a grant change elsewhere does not drop the session", async ({ browser }, info) => {
    const docA = createDoc(unique("co06a"), ["Revoked document."])
    const docB = createDoc(unique("co06b"), ["Unrelated document."])
    const docC = createDoc(unique("co06c"), ["Third document."])
    for (const d of [docA, docB]) grant(d.slug, EDITOR, "write")
    const ws = workspaceId()
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    const bOther = await b.context.newPage()
    try {
      await openDoc(a, docA.id); await openDoc(b, docA.id); await openDoc(b, docB.id, bOther)
      // A grant change on an unrelated document (known cross-document risk) must not touch B's sessions.
      grant(docC.slug, COMMENTER, "comment"); revoke(docC.slug, COMMENTER); grant(docB.slug, COMMENTER, "comment")
      await typeAtEnd(b.page, 0, " [after-unrelated-grant]")
      await expectConfirmed(b.page, docA.slug, "[after-unrelated-grant]")
      revoke(docA.slug, EDITOR)
      // The body replaces the document with the denial; the frame chip is not a Saved claim any more.
      await expect(b.page.getByText(/document access denied/i)).toBeVisible({ timeout: 20_000 })
      await expect(b.page.getByText("Revoked document.")).toBeHidden()
      expect(await b.page.locator("button[data-status]").count() === 0 || (await statusKey(b.page)) === "error").toBeTruthy()
      await screenshot(b.page, info, "co06-docs-editor-after-revoke")
      const read = await b.context.request.get(`/api/v1/pages/${docA.slug}/document?workspace_id=${ws}`)
      expect([401, 403, 404]).toContain(read.status())
      // The other document of the same person keeps working.
      await typeAtEnd(bOther, 0, " [still-editing-b]")
      await expectConfirmed(bOther, docB.slug, "[still-editing-b]")
      await expect(editor(a.page)).toContainText("[after-unrelated-grant]")
    } finally { await closeAll([a, b], info) }
  })

  test("CO08 commenter reads and comments but cannot write content through UI or API", async ({ browser }, info) => {
    const doc = createDoc(unique("co08"), ["Commentable paragraph text."])
    grant(doc.slug, COMMENTER, "comment")
    const ws = workspaceId()
    const a = await signIn(browser, OWNER, info)
    const c = await signIn(browser, COMMENTER, info)
    try {
      await openDoc(c, doc.id)
      await expect(c.page.locator("button[data-status]")).toHaveAttribute("data-status", "readonly")
      await expect(c.page.locator('[data-docs-page] .ProseMirror[contenteditable="true"]')).toHaveCount(0)
      await editor(c.page).click()
      await c.page.keyboard.type(" [commenter-typed]")
      // A valid, current Yjs update sent straight to the API with the commenter's session.
      const docUrl = `/api/v1/pages/${doc.slug}/document?workspace_id=${ws}`
      const patchUrl = `/api/v1/pages/${doc.slug}/document/patch?workspace_id=${ws}`
      const forged = await forgePatch(c.context.request, docUrl, doc.id, "[commenter-api]")
      const refused = await c.context.request.post(patchUrl, { data: forged })
      expect([401, 403, 404], await refused.text()).toContain(refused.status())
      // Control: the owner's identical-shape request is accepted, so the refusal is about rights.
      await openDoc(a, doc.id)
      const control = await a.context.request.post(patchUrl, { data: await forgePatch(a.context.request, docUrl, doc.id, "[owner-api]") })
      expect(control.status(), await control.text()).toBeLessThan(300)
      await expect(editor(c.page)).toContainText("[owner-api]")
      const state = await (await c.context.request.get(docUrl)).json() as { epoch: string; rev: number }
      const comment = await c.context.request.post(`/api/v1/pages/${doc.slug}/document/comments?workspace_id=${ws}`, { data: { id: unique("c"), schema: 4, page_id: doc.id, epoch: state.epoch, ifRev: state.rev, block: "p1", from: 0, to: 11, text: "Commenter note" } })
      expect(comment.status(), await comment.text()).toBeLessThan(300)
      const server = readDoc(doc.slug)
      expect(server).not.toContain("[commenter-typed]")
      expect(server).not.toContain("[commenter-api]")
      expect(server).toContain("[owner-api]")
      expect(cli(["doc", "comments", doc.slug])).toContain("Commenter note")
    } finally { await closeAll([a, c], info) }
  })

  test("CO10 restart of the throwaway server: confirmed work survives; the edit made while it was down is never shown as Saved", async ({ browser }, info) => {
    test.setTimeout(300_000)
    const name = process.env.COLLAB_THROWAWAY_NAME
    test.skip(!name, "needs COLLAB_THROWAWAY_NAME (scripts/collab-throwaway.sh sets it)")
    const doc = createDoc(unique("co10"), ["Restart paragraph."])
    grant(doc.slug, EDITOR, "write")
    const a = await signIn(browser, OWNER, info)
    const b = await signIn(browser, EDITOR, info)
    try {
      await openDoc(a, doc.id); await openDoc(b, doc.id)
      await typeAtEnd(a.page, 0, " [before-restart]")
      await expectConfirmed(a.page, doc.slug, "[before-restart]")
      const restarted = restart(name!)
      await expect.poll(async () => (await a.context.request.get("/api/v1/documents/status").catch(() => undefined))?.ok() ?? false, { timeout: 60_000 }).toBe(false)
      await typeAtEnd(b.page, 0, " [during-restart]")
      await b.page.waitForTimeout(3_000)
      expect(await statusKey(b.page)).not.toMatch(/^(saved|live)$/)
      await screenshot(b.page, info, "co10-docs-editor-server-down")
      await restarted
      // Delivered, or held as an explicit draft: never "Saved" while the server lacks it.
      await expect.poll(async () => readDoc(doc.slug).includes("[during-restart]") ? "delivered" : (await statusKey(b.page)) === "draft" ? "draft" : "pending", { timeout: 30_000 }).not.toBe("pending")
      await screenshot(b.page, info, "co10-docs-editor-after-restart")
      if (!readDoc(doc.slug).includes("[during-restart]")) expect(await statusKey(b.page)).not.toMatch(/^(saved|live)$/)
      await a.page.reload()
      await expect(editor(a.page)).toContainText("[before-restart]")
    } finally { await closeAll([a, b], info) }
  })

  test("CO10 a plain restart keeps the document epoch, so a pending edit can be resent without manual recovery", async () => {
    test.setTimeout(300_000)
    const name = process.env.COLLAB_THROWAWAY_NAME
    test.skip(!name, "needs COLLAB_THROWAWAY_NAME (scripts/collab-throwaway.sh sets it)")
    const doc = createDoc(unique("co10e"), ["Epoch paragraph."])
    const before = (JSON.parse(readDoc(doc.slug)) as { epoch: string }).epoch
    await restart(name!)
    await expect.poll(() => { try { return (JSON.parse(readDoc(doc.slug)) as { epoch: string }).epoch } catch { return "unreadable" } }, { timeout: 60_000 }).not.toBe("unreadable")
    // Everything above is setup that must pass; only the next assertion is the known gap.
    test.fail(true, "A server restart changes the public Docs epoch (engine generation and incarnation), and the client parks every pending edit as an Unsaved draft. Gap recorded in the night matrix.")
    expect((JSON.parse(readDoc(doc.slug)) as { epoch: string }).epoch).toBe(before)
  })
})

/** True when the text still holds every non-space character of every part (as a multiset): reordered, not lost. */
function interleaved(text: string, parts: string[]): boolean {
  const have = new Map<string, number>()
  for (const ch of text) have.set(ch, (have.get(ch) ?? 0) + 1)
  for (const ch of parts.join("")) {
    if (/\s/.test(ch)) continue
    const left = (have.get(ch) ?? 0) - 1
    if (left < 0) return false
    have.set(ch, left)
  }
  return true
}
