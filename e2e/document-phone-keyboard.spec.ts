import { test, expect, type Page } from "@playwright/test"
import { readFileSync } from "node:fs"
import { documentSchema } from "../lib/documents/schema"

// ART-06: a Document opened, edited and saved on a phone with the keyboard
// alone, including the server refusing the write with 409.
//
// Every /api request is answered here, so the spec needs only the static
// frontend: no Go server, seed or provider. The page is opened by address
// (/pages/_ is the placeholder the export serves for every slug; the client
// reads the slug from the URL) and from then on nothing is clicked or tapped:
// Tab, Shift+Tab, Enter and typing only.
//
// What the code does today, and what this pins (hooks/use-page-document.ts,
// components/features/pages/document-save-state.tsx, page-document.tsx):
//   - one role=status region carries the save state: "Saving…" while a local
//     edit is unsent (+ "Saving to this browser…" / "Saved locally in this
//     browser" · "not yet confirmed by server"), then "Saved · Confirmed by
//     server" once the patch receipt verified;
//   - a 409 from POST …/document/patch blocks the session: status "Document
//     changed. Recover your draft before reopening.", the server's message in
//     role=alert, the editor read-only, and a recovery alert offering
//     "Download unsaved draft" and "Reopen saved document". The typed text is
//     still in the editor, downloadable, and held in this browser's journal
//     across a reload ("Unsaved document draft found · recover before
//     reopening") until "Discard recovered drafts and reopen".

test.use({
  viewport: { width: 390, height: 844 },
  deviceScaleFactor: 3,
  hasTouch: true,
  isMobile: true,
  userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
  // The PR config signs in once for the live server; this spec answers every
  // /api request itself, so it must not carry (or need) that session.
  storageState: { cookies: [], origins: [] },
})

const WORKSPACE = "art06-workspace"
const PAGE_ID = "art06-document"
const EPOCH = "art06-epoch"
const TYPED = "Keyboard note from a phone"
const CONFLICT_MESSAGE = "Document changed on the server."

type PatchRequest = { id: string; update: string; schema: number; epoch: string }

async function overflowX(page: Page) {
  return page.evaluate(() => document.documentElement.scrollWidth)
}

/** The focused element, described the way a keyboard user meets it. */
async function focused(page: Page) {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    if (!el || el === document.body) return null
    const rect = el.getBoundingClientRect()
    return {
      label: el.getAttribute("aria-label") ?? el.textContent?.trim() ?? el.tagName,
      role: el.getAttribute("role") ?? el.tagName.toLowerCase(),
      // Reachable = on screen. A few px of a 44px target may sit under the edge.
      inViewport: rect.width > 0 && rect.height > 0 && rect.right > 0 && rect.left < window.innerWidth,
    }
  })
}

/** Tab until `done` is true for the focused element; returns every stop passed. */
async function tabUntil(page: Page, done: (stop: { label: string; role: string }) => boolean, limit = 80) {
  const stops: { label: string; role: string; inViewport: boolean }[] = []
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press("Tab")
    const stop = await focused(page)
    if (!stop) continue
    stops.push(stop)
    if (done(stop)) return stops
  }
  throw new Error(`keyboard focus never reached the target in ${limit} Tab presses; passed: ${stops.map(s => s.label).join(" > ")}`)
}

test("ART-06 Document on a phone, keyboard only: edit, save state, 409 conflict and recovery", async ({ page }) => {
  test.setTimeout(90_000)

  const documentURL = `/api/v1/pages/_/document`
  const head = documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "art06-paragraph" } }] }, 3)
  const baseSnapshot = documentSchema.snapshot(head)
  let rev = 1
  const applied: string[] = []
  const patches: PatchRequest[] = []
  let refuseWith409 = false
  let releasePatch: () => void = () => {}
  let holdPatch: Promise<void> | undefined
  const unexpected: string[] = []

  await page.route("**/api/**", async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const key = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (key === "GET /api/auth/session") return json({ user: { id: "art06-human", name: "Phone Writer", email: "writer@example.test" }, expires: "2099-01-01T00:00:00Z" })
    if (key === "GET /api/v1/workspaces") return json([{ id: WORKSPACE, name: "Fixture", slug: "fixture", currentUserRole: "OWNER", currentUserCapabilities: [] }])
    if (key === "GET /api/v1/documents/status") return json({ enabled: true })
    if (key === "GET /api/v1/pages") return json([])
    if (key === "GET /api/v1/pages/_") return json({ id: PAGE_ID, slug: "_", name: "Phone notes", has_document: true, panels: [] })
    if (key === `GET ${documentURL}`) {
      return json({ page_id: PAGE_ID, schema: 3, rev, epoch: EPOCH, can_write: true, snapshot: baseSnapshot, updates: applied })
    }
    if (key === `POST ${documentURL}/patch`) {
      const body = request.postDataJSON() as PatchRequest
      patches.push(body)
      if (holdPatch) await holdPatch
      if (refuseWith409) return json({ error: CONFLICT_MESSAGE, code: "conflict" }, 409)
      applied.push(body.update)
      rev += 1
      return json({ id: body.id, epoch: body.epoch, schema: body.schema, rev })
    }
    // Shell chatter this flow does not depend on (realtime token, lists, flags).
    if (request.method() === "GET" && /ws-token|settings|config|health|folders|access|agents|crews|inbox|notifications|presence/.test(path)) return json(path.includes("ws-token") ? {} : [])
    if (request.method() === "GET") return json([])
    unexpected.push(key)
    return json({}, 404)
  })

  const pageErrors: string[] = []
  page.on("pageerror", error => pageErrors.push(error.message))
  const dialogs: string[] = []
  page.on("dialog", dialog => { dialogs.push(dialog.type()); void dialog.accept() })

  // ── Open the Document (by address; nothing is clicked from here on) ──────
  await page.goto(`/pages/_?workspace_id=${WORKSPACE}`)
  const status = page.locator("[role=status][data-durability]")
  await expect(page.getByRole("heading", { name: "Phone notes" })).toBeVisible({ timeout: 20_000 })
  await expect(status).toHaveCount(1)
  await expect(status).toHaveText(/^Saved · Confirmed by server$/, { timeout: 15_000 })
  expect(await overflowX(page), "no horizontal scroll once opened").toBeLessThanOrEqual(390)

  // ── Tab into the editor; every stop on the way is a reachable control ────
  const stops = await tabUntil(page, stop => stop.label === "Document editor")
  expect(stops.length).toBeGreaterThan(1)
  for (const stop of stops) {
    expect(stop.inViewport, `focus stop "${stop.label}" is inside the 390px viewport`).toBe(true)
    expect(stop.label, "every focus stop has an accessible name").not.toBe("")
  }
  const labels = stops.map(stop => stop.label)
  expect(labels).toContain("History")
  expect(labels).toContain("Heading 1")
  const editor = page.getByLabel("Document editor")
  await expect(editor).toBeFocused()

  // ── Type: pending, then server-confirmed (patch held to observe both) ────
  holdPatch = new Promise<void>(resolve => { releasePatch = resolve })
  await page.keyboard.type(TYPED)
  await expect(editor).toContainText(TYPED)
  await expect(status).toHaveCount(1)
  await expect(status).toContainText("Saving…")
  await expect(status).toContainText("Saved locally in this browser · not yet confirmed by server")
  await expect.poll(() => patches.length, { message: "the edit is sent to the server" }).toBe(1)
  expect(await overflowX(page)).toBeLessThanOrEqual(390)
  releasePatch()
  await expect(status).toHaveText(/^Saved · Confirmed by server$/, { timeout: 15_000 })
  await expect(status).toHaveCount(1)
  expect(patches[0].epoch).toBe(EPOCH)
  expect(applied.length, "every typed update was stored").toBeGreaterThanOrEqual(1)

  // A keyboard-activated control: Shift+Tab back to "Heading 1", Enter applies it.
  await page.keyboard.press("Shift+Tab")
  await tabUntilBackwards(page, "Heading 1")
  await page.keyboard.press("Enter")
  await expect(editor.locator("h1")).toContainText(TYPED)
  await expect(editor).toBeFocused()
  await expect(status).toHaveText(/^Saved · Confirmed by server$/, { timeout: 15_000 })

  // ── Force a 409 on the next patch ────────────────────────────────────────
  holdPatch = undefined
  refuseWith409 = true
  const sentBefore = patches.length, storedBefore = applied.length
  const MORE = " and a second line"
  await page.keyboard.type(MORE)
  await expect.poll(() => patches.length, { message: "the next edit reaches the server" }).toBeGreaterThan(sentBefore)

  await expect(status).toContainText("Document changed. Recover your draft before reopening.", { timeout: 15_000 })
  await expect(status).toHaveCount(1)
  const alerts = page.getByRole("alert")
  await expect(alerts.filter({ hasText: CONFLICT_MESSAGE })).toHaveCount(1)
  const recovery = alerts.filter({ hasText: "Your unsaved draft is retained privately in this browser. Download it before discarding it." })
  await expect(recovery).toBeVisible()
  await expect(editor).toHaveAttribute("contenteditable", "false")
  await expect(editor).toContainText(TYPED + MORE)
  expect(applied, "the refused edit was not stored by the server").toHaveLength(storedBefore)
  expect(await overflowX(page), "no horizontal scroll with the conflict UI").toBeLessThanOrEqual(390)

  // ── The typed text is not silently lost: download it by keyboard ─────────
  await tabUntil(page, stop => stop.label === "Download unsaved draft")
  const download = page.waitForEvent("download")
  await page.keyboard.press("Enter")
  const saved = await download
  expect(saved.suggestedFilename()).toBe("_-recovery.json")
  const draftPath = await saved.path()
  expect(JSON.stringify(JSON.parse(readFileSync(draftPath, "utf8")))).toContain(TYPED + MORE)

  // ── Reopen by keyboard: the unsent draft is held, not dropped ────────────
  await tabUntil(page, stop => stop.label === "Reopen saved document")
  const reloaded = page.waitForEvent("load")
  await page.keyboard.press("Enter")
  await reloaded
  expect(dialogs, "leaving with unsent work asks first").toContain("beforeunload")
  refuseWith409 = false
  await expect(status).toContainText("Unsaved document draft found · recover before reopening", { timeout: 20_000 })
  await expect(status).toContainText("An earlier unsent draft is held in this browser and was not restored")
  await expect(status).toHaveCount(1)
  await expect(page.getByRole("alert").filter({ hasText: "Your unsaved draft is retained privately in this browser." })).toBeVisible()
  expect(await overflowX(page)).toBeLessThanOrEqual(390)

  // Only an explicit, keyboard-operated discard clears it and reopens the saved copy.
  await tabUntil(page, stop => stop.label === "Download unsaved draft")
  await tabUntil(page, stop => stop.label === "Discard recovered drafts and reopen")
  await page.keyboard.press("Enter")
  await expect(status).toHaveText(/^Saved · Confirmed by server$/, { timeout: 20_000 })
  await expect(page.getByLabel("Document editor")).toHaveAttribute("contenteditable", "true")
  expect(await overflowX(page)).toBeLessThanOrEqual(390)

  expect(pageErrors).toEqual([])
  expect(unexpected, "no request outside the fixture").toEqual([])
})

async function tabUntilBackwards(page: Page, label: string, limit = 40) {
  for (let i = 0; i < limit; i++) {
    const stop = await focused(page)
    if (stop?.label === label) return
    await page.keyboard.press("Shift+Tab")
  }
  throw new Error(`Shift+Tab never reached "${label}"`)
}
