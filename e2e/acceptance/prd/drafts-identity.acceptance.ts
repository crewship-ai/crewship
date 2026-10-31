// PRD §0.10 "drafts, another account in the same browser": person A leaves an
// unsaved Project source draft and an unsent comment, logs out, and person B
// signs in in the same browser profile. B must read nothing of A's draft: not
// in the page, not in the browser storage reachable from B's session
// (localStorage, IndexedDB), and not in B's network traffic.
//
// Two ways A leaves, because the product treats them differently:
//   explicit Log out: the product asks "Sign out and delete local recovery
//     copies?" and, on Sign out, deletes A's drafts on purpose. B reads
//     nothing, and A's drafts are gone when A comes back, as announced.
//   the session ends without a Log out (expiry, revocation: here the cookies
//     are dropped): the drafts stay sealed in the browser for A. B reads
//     nothing, and A gets both drafts back after signing in again.
import type { Page } from "@playwright/test"
import { loginStorageState } from "./auth"
import { CrashableProfile, createProject, ensureArtifactProjects, grantPage, openProject, openViewer, pause, prepareViewerComment, projectOffer, shareProject, typeSourceDraft, viewerCommentValue } from "./drafts-crash/crash"
import { expect, prdRequirement, test } from "./fixtures"

async function userIdOf(request: import("@playwright/test").APIRequestContext) {
  const res = await request.get("/api/auth/session")
  const id = ((await res.json()) as { user?: { id?: string } }).user?.id
  expect(id, "session has a user id").toBeTruthy()
  return id!
}

/** Everything the page's origin lets this session read from localStorage and IndexedDB, as text. */
async function readableStorage(page: Page): Promise<{ localStorageKeys: number; indexedDbRecords: number; text: string }> {
  return page.evaluate(async () => {
    const parts: string[] = []
    let keys = 0
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)!
      keys++
      parts.push(k, localStorage.getItem(k) ?? "")
    }
    let records = 0
    const dbs = typeof indexedDB.databases === "function" ? await indexedDB.databases() : []
    const enc = (v: unknown): string => {
      try {
        return JSON.stringify(v, (_k, x) => (x instanceof ArrayBuffer ? `ab:${new TextDecoder("latin1").decode(x)}` : ArrayBuffer.isView(x) ? `ab:${new TextDecoder("latin1").decode(x as ArrayBufferView)}` : x)) ?? ""
      } catch {
        return String(v)
      }
    }
    for (const info of dbs) {
      if (!info.name) continue
      const db = await new Promise<IDBDatabase | null>((resolve) => {
        const r = indexedDB.open(info.name!)
        r.onsuccess = () => resolve(r.result)
        r.onerror = () => resolve(null)
        r.onblocked = () => resolve(null)
      })
      if (!db) continue
      for (const name of Array.from(db.objectStoreNames)) {
        await new Promise<void>((resolve) => {
          const tx = db.transaction(name, "readonly")
          const all = tx.objectStore(name).openCursor()
          all.onsuccess = () => {
            const c = all.result
            if (!c) return resolve()
            records++
            parts.push(enc(c.key), enc(c.value))
            c.continue()
          }
          all.onerror = () => resolve()
        })
      }
      db.close()
    }
    return { localStorageKeys: keys, indexedDbRecords: records, text: parts.join("\n") }
  })
}

async function logOut(page: Page): Promise<{ confirmShown: boolean; confirmText: string | null }> {
  await page.getByRole("button", { name: "User menu" }).first().click()
  await page.getByRole("menuitem", { name: /Log out/ }).or(page.getByRole("button", { name: /Log out/ })).first().click()
  // With unsaved drafts the product asks before it deletes the local recovery copies.
  const confirm = page.getByRole("button", { name: "Sign out", exact: true })
  const shown = await confirm.waitFor({ timeout: 5_000 }).then(() => true, () => false)
  const confirmText = shown ? (await page.getByRole("heading", { name: /delete local recovery copies/i }).locator("xpath=..").innerText().catch(() => "")).replace(/\s+/g, " ").trim() : null
  if (shown) await confirm.click()
  await expect.poll(() => page.url(), { timeout: 30_000, message: "signed out: the login page" }).toMatch(/\/login/)
  return { confirmShown: shown, confirmText }
}

async function logIn(page: Page, email: string, password: string) {
  if (!/\/login/.test(page.url())) await page.goto("/login")
  await page.locator("#email").fill(email)
  await page.locator("#password").fill(password)
  await page.locator('button[type="submit"]').first().click()
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 60_000, message: "signed in: left the login page" }).not.toMatch(/^\/login/)
}

test.describe("drafts and identity", () => {
  test(
    "another account in the same browser reads nothing of a logged-out person's drafts",
    { annotation: prdRequirement("drafts.identity-switch", "A leaves unsaved drafts and signs out; B signs in in the same browser and reads nothing of them; after a session that ended without Log out, A gets the drafts back") },
    async ({ prd, request, playwright }) => {
      test.setTimeout(Math.max(test.info().timeout, 10 * 60_000))
      const ws = await prd.qaWorkspace()
      const availability = await ensureArtifactProjects(request, ws.id, !prd.env.workspaceId)
      const project = await createProject(request, ws.id, "identity")
      const other = await prd.provision("MEMBER", "PRD drafts identity person B")
      // B really has access, so "sees nothing" means private drafts, not a hidden page.
      expect(await shareProject(request, ws.id, project.pageId, 1), "share version 1").toBeLessThan(300)
      expect(await grantPage(request, ws.id, project.pageId, other.userId, "write"), "grant B write").toBeLessThan(300)

      const ownerAccount = prd.requireAccount("owner")
      const profile = new CrashableProfile(playwright.chromium, prd.origin, await loginStorageState(prd.origin, ownerAccount), {
        userId: await userIdOf(request),
        workspaceId: ws.id,
      })
      const ev: Record<string, unknown> = { availability, pageId: project.pageId }
      const traffic: string[] = []
      let watching = false
      const checks: Array<() => void> = []

      /** A types a source draft and an unsent viewer comment; returns the typed texts. */
      const draftAs = async (a: Page, round: string) => {
        const tag = `${round}-${Date.now().toString(36)}`
        const MARK = `IDENTITY-SOURCE-${tag}`
        const COMMENT = `Identity unsent comment ${tag}`
        await openProject(a, project.slug, ws.id)
        const typed = await typeSourceDraft(a, MARK)
        await openViewer(a, project.slug, ws.id)
        const fill = await prepareViewerComment(a)
        await fill(COMMENT)
        await pause(6_000)
        expect(await viewerCommentValue(a), `${round}: A's comment is typed`).toBe(COMMENT)
        await openProject(a, project.slug, ws.id)
        await a.waitForTimeout(2_000)
        const offer = await projectOffer(a)
        expect(offer.restoreOffered, `${round}: A's own source draft is kept while A is signed in`).toBe(1)
        return { MARK, COMMENT, typed }
      }

      /** B signs in in the same browser and looks for A's drafts everywhere B can. */
      const readAsB = async (a: Page, round: string, d: { MARK: string; COMMENT: string }) => {
        const leaked = (h: string) => h.includes(d.MARK) || h.includes(d.COMMENT)
        traffic.length = 0
        watching = true
        await logIn(a, other.account.email, other.account.password)
        await openProject(a, project.slug, ws.id)
        await a.waitForTimeout(3_000)
        const offer = await projectOffer(a)
        const body = await a.locator("body").innerText()
        await a.screenshot({ path: test.info().outputPath(`${round}-b-project.png`), fullPage: true })
        await a.getByRole("button", { name: "Edit source", exact: true }).click()
        const bSource = await a.getByRole("textbox", { name: "Source file", exact: true }).inputValue()
        await openViewer(a, project.slug, ws.id)
        await prepareViewerComment(a)
        const bComment = await viewerCommentValue(a)
        const bBody = await a.locator("body").innerText()
        const storage = await readableStorage(a)
        await a.screenshot({ path: test.info().outputPath(`${round}-b-viewer.png`), fullPage: true })
        watching = false
        const result = {
          offer,
          sourceBoxStartsWithDraft: bSource.includes(d.MARK),
          viewerCommentBox: bComment,
          pageTextHasDraft: leaked(body) || leaked(bBody),
          storage: { localStorageKeys: storage.localStorageKeys, indexedDbRecords: storage.indexedDbRecords, hasDraftText: leaked(storage.text) },
          networkHasDraft: traffic.some(leaked),
          networkItems: traffic.length,
        }
        checks.push(() => {
          expect.soft(result.offer.restoreOffered, `${round}: B is not offered A's source draft`).toBe(0)
          expect.soft(result.sourceBoxStartsWithDraft, `${round}: B's source editor does not start with A's text`).toBe(false)
          expect.soft(result.viewerCommentBox ?? "", `${round}: B's comment box is empty`).toBe("")
          expect.soft(result.pageTextHasDraft, `${round}: A's draft text appears nowhere in B's pages`).toBe(false)
          expect.soft(result.storage.hasDraftText, `${round}: no A draft text in the storage B's session can read`).toBe(false)
          expect.soft(result.networkHasDraft, `${round}: no A draft text in B's network traffic`).toBe(false)
        })
        return result
      }

      try {
        const a = await profile.page()
        a.on("dialog", (d) => void d.accept().catch(() => {}))
        a.context().on("request", (req) => {
          if (watching && req.url().startsWith(prd.origin)) traffic.push(req.url(), req.postData() ?? "")
        })
        a.context().on("response", async (res) => {
          if (!watching || !res.url().startsWith(prd.origin) || !/json|text|html/.test(res.headers()["content-type"] ?? "")) return
          traffic.push(await res.text().catch(() => ""))
        })

        // Round 1: A signs out explicitly.
        const d1 = await draftAs(a, "logout")
        const out1 = await logOut(a)
        ev.logout = { ...out1, b: await readAsB(a, "logout", d1) }
        await logOut(a)
        await logIn(a, ownerAccount.email, ownerAccount.password)
        await openProject(a, project.slug, ws.id)
        await a.waitForTimeout(3_000)
        const after1 = await projectOffer(a)
        ;(ev.logout as Record<string, unknown>).aAfterExplicitLogout = after1
        checks.push(() => {
          expect.soft(out1.confirmShown, "logout: the product asks before deleting A's local recovery copies").toBe(true)
          expect.soft(out1.confirmText ?? "", "logout: the question says the copies are deleted").toMatch(/delete local recovery copies/i)
          expect.soft(after1.restoreOffered, "logout: after the announced deletion A is not offered the drafts").toBe(0)
        })

        // Round 2: A's session ends without a Log out (the cookies go; the drafts stay).
        const d2 = await draftAs(a, "expiry")
        await a.context().clearCookies()
        await a.goto("/login")
        ev.expiry = { b: await readAsB(a, "expiry", d2) }
        await logOut(a)
        await logIn(a, ownerAccount.email, ownerAccount.password)
        await openProject(a, project.slug, ws.id)
        await a.waitForTimeout(3_000)
        const back = await projectOffer(a)
        let restoredIdentical: boolean | null = null
        if (back.restoreOffered === 1) {
          await a.getByRole("button", { name: "Restore source draft", exact: true }).click()
          restoredIdentical = (await a.getByRole("textbox", { name: "Source file", exact: true }).inputValue()) === d2.typed
        }
        await openViewer(a, project.slug, ws.id)
        await prepareViewerComment(a)
        const aComment = await viewerCommentValue(a)
        await a.screenshot({ path: test.info().outputPath("expiry-a-again.png"), fullPage: true })
        ;(ev.expiry as Record<string, unknown>).aAgain = { offer: back, restoredIdentical, viewerComment: aComment }
        checks.push(() => {
          expect.soft(back.restoreOffered, "expiry: A is offered the source draft again").toBe(1)
          expect.soft(restoredIdentical, "expiry: the restored source is what A typed").toBe(true)
          expect.soft(aComment, "expiry: A's unsent comment is back").toBe(d2.COMMENT)
        })
      } finally {
        await prd.evidence("result.json", ev)
        await profile.dispose()
      }
      for (const c of checks) c()
    },
  )
})
