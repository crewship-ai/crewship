// Document history: compare, revert a change and proposals (stream F3, PR #3825).
//
// Needs a real backend with the Documents engine, so it only runs through
//   scripts/throwaway-server.sh run NAME --documents --binary PATH -- \
//     pnpm exec playwright test --config=playwright.documents.config.ts
// and skips itself anywhere else.
//
// Real: bootstrap, Document creation and editing through the browser, the
// Node Documents service, the Go authority, checkpoint list and checkpoint
// read. Mocked with page.route (no endpoint exists yet, FE-REQ-F3-2/-4):
// revert and proposals. Two honest limits:
//  - no public route creates a checkpoint for a human (pins come from agent
//    drafts), so the pin is requested from the Node engine's own checkpoint
//    operation (private service socket, the person's bearer) after the first
//    edit; the engine snapshots and the Go authority stores it;
//  - the reader is the real document response with can_write set to false.
import { test, expect, type Browser, type BrowserContext, type Page, type Route } from "@playwright/test"
import { spawn, type ChildProcess } from "node:child_process"
import { request as httpRequest } from "node:http"
import { once } from "node:events"
import { readFileSync, mkdtempSync, openSync } from "node:fs"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"

const manifestPath = process.env.CREWSHIP_THROWAWAY_MANIFEST
const server = process.env.CREWSHIP_SERVER
test.skip(!manifestPath || !server, "needs scripts/throwaway-server.sh run --documents (see playwright.documents.config.ts)")
test.describe.configure({ mode: "serial" })

const FIRST = ["Alpha plan", "Beta plan", "Delta plan"]
let engine: ChildProcess | undefined
let context: BrowserContext
let docUrl = ""
let workspace = ""
let pageId = ""
let pinRev = 0

/** Private engine call, as the Go service would make it: service token, workspace and the person's bearer. */
function engineCall(manifest: Record<string, string>, bearer: string, operation: string, body: unknown) {
  const token = readFileSync(manifest.documents_service_token_file, "utf8").trim()
  return new Promise<{ status: number; body: Record<string, unknown> }>((resolve, reject) => {
    const req = httpRequest({ socketPath: manifest.documents_service_socket, path: `/v1/documents/${pageId}/${operation}`, method: "POST",
      headers: { "content-type": "application/json", "x-crewship-docs-service": token, "x-workspace-id": workspace, authorization: `Bearer ${bearer}` } }, res => {
      let text = ""
      res.on("data", chunk => { text += chunk })
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body: text ? JSON.parse(text) : {} }))
    })
    req.on("error", reject)
    req.end(JSON.stringify(body))
  })
}
const editorOf = (page: Page) => page.getByLabel("Document editor", { exact: true })
const saved = (page: Page) => page.getByRole("status").filter({ hasText: /^Saved$/ })
const para = (id: string, text: string) => ({ type: "paragraph", attrs: { id }, content: [{ type: "text", text }] })

async function openDocument(page: Page) {
  await page.goto(docUrl)
  await editorOf(page).waitFor()
  await saved(page).waitFor()
}
async function openHistory(page: Page) {
  await page.getByRole("button", { name: "History", exact: true }).click()
  await page.getByRole("list", { name: "Confirmed checkpoints" }).waitFor()
}
const confirmed = async (page: Page) => (await (await page.request.get(`/api/v1/pages/%40${pageId}/document?workspace_id=${workspace}`)).json()) as { rev: number; content: unknown; epoch: string; schema: number }
const headRev = async (page: Page) => (await confirmed(page)).rev

test.beforeAll(async ({ browser }: { browser: Browser }) => {
  if (!manifestPath || !server) return
  test.setTimeout(240_000)
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"))
  const out = mkdtempSync(join(tmpdir(), "document-history-e2e-"))
  const log = openSync(join(out, "engine.log"), "w", 0o600)
  engine = spawn(process.execPath, [resolve("tools/docs-service/service.mjs")], {
    env: { NODE_ENV: "production", CREWSHIP_DOCS_SOCKET: manifest.documents_service_socket, CREWSHIP_DOCS_AUTHORITY_SOCKET: manifest.documents_authority_socket, CREWSHIP_DOCS_SERVICE_TOKEN_FILE: manifest.documents_service_token_file },
    stdio: ["ignore", log, log],
  })
  const base = test.info().project.use.baseURL!
  context = await browser.newContext({ baseURL: base })
  const page = await context.newPage()
  const bootstrap = await context.request.post(`${base}/api/v1/bootstrap`, { data: { full_name: "History Owner", email: "history-owner@example.invalid", password: "disposable-history-password" } })
  expect(bootstrap.status(), "fresh disposable bootstrap").toBe(201)
  workspace = (await bootstrap.json()).workspace_id
  expect((await context.request.post(`${base}/api/v1/onboarding/complete`, { data: { skipped: true } })).status()).toBe(200)
  await expect.poll(async () => {
    const status = await context.request.get(`${base}/api/v1/documents/status?workspace_id=${encodeURIComponent(workspace)}`)
    return status.ok() && (await status.json()).enabled === true
  }, { timeout: 30_000 }).toBe(true)

  await page.goto("/pages")
  await page.getByRole("button", { name: "New Doc", exact: true }).click()
  await page.getByLabel("Name", { exact: true }).fill("History compare")
  const initialize = page.waitForRequest(request => request.url().includes("/document/initialize") && request.method() === "POST")
  await page.getByRole("button", { name: "Create Doc", exact: true }).click()
  pageId = (await initialize).postDataJSON().page_id
  await editorOf(page).waitFor()
  docUrl = page.url()

  // First version: three paragraphs, typed by the person.
  await editorOf(page).click()
  await page.keyboard.type(FIRST.join("\n"), { delay: 40 })
  await expect(editorOf(page)).toContainText("Delta plan")
  // "Saved" can still be showing from before the edit: wait for the server to confirm the text.
  await expect.poll(async () => JSON.stringify((await confirmed(page)).content), { timeout: 20_000 }).toContain("Delta plan")
  const head = await confirmed(page)
  pinRev = head.rev
  const cookies = await context.cookies()
  const bearer = cookies.find(cookie => cookie.name === "authjs.session-token")?.value
  expect(bearer, `session cookie among ${cookies.map(cookie => cookie.name).join(",")}`).toBeTruthy()
  const pin = await engineCall(manifest, bearer!, "checkpoint", { id: "e2e-pin-first", schema: head.schema, page_id: pageId, epoch: head.epoch, ifRev: head.rev })
  expect(pin.status, JSON.stringify(pin.body)).toBe(200)

  // Second version: Alpha changed, Beta removed, Gamma added. One edit at a
  // time, each confirmed by the server before the next, so the polling sync
  // never rewrites a paragraph under the cursor.
  const serverText = async () => JSON.stringify((await confirmed(page)).content)
  const paragraphs = editorOf(page).locator("p")
  await paragraphs.nth(0).click()
  await page.keyboard.press("End")
  await page.keyboard.type(" revised", { delay: 40 })
  await expect.poll(serverText, { timeout: 20_000 }).toContain("Alpha plan revised")
  await paragraphs.nth(1).selectText()
  await expect.poll(() => page.evaluate(() => getSelection()?.toString())).toBe("Beta plan")
  await page.keyboard.press("Backspace")
  await expect.poll(serverText, { timeout: 20_000 }).not.toContain("Beta plan")
  await page.keyboard.press("Backspace")
  await expect(paragraphs).toHaveCount(2)
  await expect(paragraphs.nth(1)).toHaveText("Delta plan")
  await paragraphs.nth(1).click()
  await page.keyboard.press("End")
  await page.keyboard.press("Enter")
  await page.keyboard.type("Gamma plan", { delay: 40 })
  await expect.poll(serverText, { timeout: 20_000 }).toContain("Gamma plan")
  await expect.poll(() => headRev(page)).toBeGreaterThan(pinRev)
  await page.close()
})

test.afterAll(async () => {
  await context?.close()
  if (engine && engine.exitCode === null && engine.signalCode === null) {
    const exited = once(engine, "exit")
    engine.kill("SIGTERM")
    await Promise.race([exited, new Promise(resolve => setTimeout(resolve, 5000))])
    if (engine.exitCode === null && engine.signalCode === null) engine.kill("SIGKILL")
  }
})

test.describe("compare on the real backend", () => {
  test("a retained revision is compared with the current document, and Restore and Revert are separate buttons", async () => {
    const page = await context.newPage()
    await openDocument(page)
    await openHistory(page)
    await page.getByRole("button", { name: new RegExp(`^Revision ${pinRev}\\b`) }).click()
    await expect(page.getByLabel(`Confirmed revision ${pinRev}`)).toContainText("Beta plan")

    // Two different actions with different names and different confirmations.
    const restore = page.getByRole("button", { name: "Restore selected checkpoint" })
    const revert = page.getByRole("button", { name: "Revert this change", exact: true })
    await expect(restore).toBeVisible()
    await expect(revert).toBeVisible()
    await restore.click()
    await expect(page.getByRole("alertdialog", { name: "Confirm checkpoint restore" })).toContainText(`Restore revision ${pinRev} as a new confirmed revision`)
    await page.getByRole("button", { name: "Cancel restore" }).click()
    await revert.click()
    await expect(page.getByRole("group", { name: "Confirm revert" })).toContainText("Undo only the change recorded at this checkpoint")
    await page.getByRole("button", { name: "Cancel", exact: true }).click()

    await page.getByRole("button", { name: "Compare with current" }).click()
    const compare = page.getByRole("region", { name: new RegExp(`^Compare revision ${pinRev} with the current document`) })
    await expect(compare.getByRole("status")).toContainText("1 added, 1 removed, 1 changed, 0 moved")
    // The kind is written out, not only coloured.
    const added = compare.locator('li[data-kind="added"]'), removed = compare.locator('li[data-kind="removed"]'), changed = compare.locator('li[data-kind="changed"]')
    await expect(added).toContainText("Added · paragraph")
    await expect(added).toContainText("Gamma plan")
    await expect(removed).toContainText("Removed · paragraph")
    await expect(removed).toContainText("Beta plan")
    await expect(changed).toContainText("Changed · paragraph")
    await expect(changed).toContainText("Alpha plan revised")
    // Comparing never edits: the live document is the second version and no write was sent.
    await expect(editorOf(page)).toContainText("Alpha plan revised")
    await expect(editorOf(page)).not.toContainText("Beta plan")

    // Reload: the revision is still retained and compares the same.
    const head = await headRev(page)
    await page.reload()
    await editorOf(page).waitFor()
    expect(await headRev(page)).toBe(head)
    await openHistory(page)
    await page.getByRole("button", { name: new RegExp(`^Revision ${pinRev}\\b`) }).click()
    await page.getByRole("button", { name: "Compare with current" }).click()
    await expect(page.getByRole("region", { name: /^Compare revision/ }).getByRole("status")).toContainText("1 added, 1 removed, 1 changed, 0 moved")
    await page.close()
  })

  test("compare fits a phone and keeps its labels", async () => {
    const page = await context.newPage()
    await page.setViewportSize({ width: 390, height: 844 })
    await openDocument(page)
    await openHistory(page)
    await page.getByRole("button", { name: new RegExp(`^Revision ${pinRev}\\b`) }).click()
    await page.getByRole("button", { name: "Compare with current" }).click()
    const compare = page.getByRole("region", { name: /^Compare revision/ })
    await expect(compare.locator('li[data-kind="added"]')).toContainText("Added · paragraph")
    await expect(compare.locator('li[data-kind="removed"]')).toContainText("Removed · paragraph")
    await expect(compare.locator('li[data-kind="changed"]')).toContainText("Changed · paragraph")
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "no horizontal page scroll").toBe(true)
    const box = await compare.boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(391)
    await page.close()
  })
})

interface Seen { url: string; body: Record<string, unknown>; key: string | undefined }
function revertRoute(page: Page, respond: (seen: Seen) => { status: number; body: unknown }) {
  const calls: Seen[] = []
  void page.route("**/document/revert*", async (route: Route) => {
    const request = route.request()
    const seen = { url: request.url(), body: request.postDataJSON(), key: request.headers()["idempotency-key"] }
    calls.push(seen)
    const answer = respond(seen)
    await route.fulfill({ status: answer.status, contentType: "application/json", body: JSON.stringify(answer.body) })
  })
  return calls
}
async function selectPin(page: Page) {
  await openHistory(page)
  await page.getByRole("button", { name: new RegExp(`^Revision ${pinRev}\\b`) }).click()
  await expect(page.getByLabel(`Confirmed revision ${pinRev}`)).toBeVisible()
}

test.describe("revert a change (mocked endpoint, FE-REQ-F3-2)", () => {
  test("is disabled while unsent edits exist, then sends the reviewed head with one request ID", async () => {
    const page = await context.newPage()
    await openDocument(page)
    // Hold the next save so the edit stays unsent.
    let release: () => void = () => {}
    const held = new Promise<void>(resolve => { release = resolve })
    await page.route("**/document/patch*", async route => { await held; await route.continue() })
    const calls = revertRoute(page, () => ({ status: 200, body: { rev: 99, reverted_change: "e2e-pin-first", new_checkpoint_id: "n1" } }))
    await editorOf(page).click()
    await page.keyboard.press("Control+End")
    await page.keyboard.type(" unsent")
    await expect(page.getByRole("status").filter({ hasText: /Saving/ })).toBeVisible()
    await selectPin(page)
    await page.getByRole("button", { name: "Revert this change", exact: true }).click()
    const confirm = page.getByRole("group", { name: "Confirm revert" })
    await expect(confirm).toContainText("You have unsent edits")
    await expect(confirm.getByRole("button", { name: "Download unsent edits" })).toBeVisible()
    await expect(confirm.getByRole("button", { name: "Revert this change", exact: true })).toBeDisabled()
    expect(calls).toHaveLength(0)

    release()
    await saved(page).waitFor()
    const head = await headRev(page)
    await expect(confirm.getByRole("button", { name: "Revert this change", exact: true })).toBeEnabled()
    const reload = page.waitForEvent("framenavigated")
    await confirm.getByRole("button", { name: "Revert this change", exact: true }).click()
    await reload
    expect(calls).toHaveLength(1)
    expect(calls[0].body).toMatchObject({ workspace_id: workspace, change_id: "e2e-pin-first", expected_rev: expect.any(Number) })
    expect(calls[0].body.expected_rev).toBeGreaterThanOrEqual(head - 1)
    expect(calls[0].key).toBeTruthy()
    expect(calls[0].body.request_id).toBe(calls[0].key)
    await page.close()
  })

  test("a 409 conflict changes nothing and says so", async () => {
    const page = await context.newPage()
    await openDocument(page)
    const before = await headRev(page)
    const text = await editorOf(page).innerText()
    const calls = revertRoute(page, () => ({ status: 409, body: { conflicts: ["block-1", "block-2"] } }))
    await selectPin(page)
    await page.getByRole("button", { name: "Revert this change", exact: true }).click()
    await page.getByRole("group", { name: "Confirm revert" }).getByRole("button", { name: "Revert this change", exact: true }).click()
    await expect(page.getByRole("alert").filter({ hasText: "Nothing was changed" })).toContainText("(2 blocks)")
    expect(calls).toHaveLength(1)
    expect(await headRev(page)).toBe(before)
    expect(await editorOf(page).innerText()).toBe(text)
    await page.close()
  })
})

const proposal = (over: Record<string, unknown> = {}) => ({
  id: "prop-1", author: "Research agent", run_id: "run-1", target: { block_ids: ["a"] }, base_rev: 1, status: "open", created_at: "2026-10-19T10:00:00Z", ...over,
})
function proposalRoutes(page: Page, accept: (body: Record<string, unknown>, key: string | undefined) => { status: number; body: unknown }) {
  const accepts: Seen[] = []
  void page.route("**/document/proposals**", async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    const json = (status: number, body: unknown) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    if (request.method() === "POST" && path.endsWith("/accept")) {
      const seen = { url: request.url(), body: request.postDataJSON(), key: request.headers()["idempotency-key"] }
      accepts.push(seen)
      const answer = accept(seen.body, seen.key)
      return json(answer.status, answer.body)
    }
    if (path.endsWith("/prop-1")) return json(200, { ...proposal(), base_content: { type: "doc", content: [para("a", "Alpha plan revised")] }, proposed_content: { type: "doc", content: [para("a", "Alpha plan, agent edit")] } })
    return json(200, { proposals: [proposal()] })
  })
  return accepts
}
async function openProposal(page: Page) {
  await page.getByRole("button", { name: /Research agent · open/ }).click()
  await page.getByRole("region", { name: "Proposal from Research agent" }).waitFor()
}

test.describe("proposals (mocked endpoints, FE-REQ-F3-4)", () => {
  test("accept is sent against the head the reviewer saw, and the proposal is not in the document before", async () => {
    const page = await context.newPage()
    const accepts = proposalRoutes(page, () => ({ status: 200, body: { rev: 120 } }))
    await openDocument(page)
    await openProposal(page)
    const review = page.getByRole("region", { name: "Proposal from Research agent" })
    await expect(review.locator('li[data-kind="changed"]')).toContainText("Alpha plan, agent edit")
    await expect(editorOf(page)).not.toContainText("agent edit")
    const head = await headRev(page)
    const reload = page.waitForEvent("framenavigated")
    await review.getByRole("button", { name: "Accept proposal" }).click()
    await reload
    expect(accepts).toHaveLength(1)
    expect(accepts[0].body.expected_rev).toBe(head)
    expect(accepts[0].body.request_id).toBe(accepts[0].key)
    await page.close()
  })

  test("a 409 conflict refuses the whole proposal and the document stays as it was", async () => {
    const page = await context.newPage()
    const accepts = proposalRoutes(page, () => ({ status: 409, body: { status: "conflict" } }))
    await openDocument(page)
    const text = await editorOf(page).innerText(), head = await headRev(page)
    await openProposal(page)
    const review = page.getByRole("region", { name: "Proposal from Research agent" })
    await review.getByRole("button", { name: "Accept proposal" }).click()
    await expect(review.getByRole("alert")).toContainText("Nothing was applied")
    expect(accepts).toHaveLength(1)
    expect(await headRev(page)).toBe(head)
    expect(await editorOf(page).innerText()).toBe(text)
    await expect(review.getByRole("button", { name: "Accept proposal" })).toBeEnabled()
    await page.close()
  })

  test("a reader can compare a proposal and a revision but cannot decide or revert", async () => {
    const page = await context.newPage()
    const accepts = proposalRoutes(page, () => ({ status: 200, body: { rev: 1 } }))
    // The real response, with write permission withdrawn.
    await page.route(url => url.pathname.endsWith("/document") && url.pathname.includes("/pages/"), async route => {
      if (route.request().method() !== "GET") return route.continue()
      const response = await route.fetch()
      await route.fulfill({ response, json: { ...(await response.json()), can_write: false } })
    })
    await page.goto(docUrl)
    await editorOf(page).waitFor()
    await expect(page.getByRole("status").filter({ hasText: /^Read only$/ })).toBeVisible()
    await openProposal(page)
    const review = page.getByRole("region", { name: "Proposal from Research agent" })
    await expect(review.locator('li[data-kind="changed"]')).toBeVisible()
    await expect(review).toContainText("You can compare this proposal but not decide on it.")
    await expect(review.getByRole("button", { name: "Accept proposal" })).toHaveCount(0)
    await expect(review.getByRole("button", { name: "Reject proposal" })).toHaveCount(0)
    await selectPin(page)
    await page.getByRole("button", { name: "Compare with current" }).click()
    await expect(page.getByRole("region", { name: /^Compare revision/ }).locator('li[data-kind="added"]')).toContainText("Added · paragraph")
    await expect(page.getByRole("button", { name: "Revert this change", exact: true })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Restore selected checkpoint" })).toHaveCount(0)
    expect(accepts).toHaveLength(0)
    await page.close()
  })

  test("proposals fit a phone", async () => {
    const page = await context.newPage()
    await page.setViewportSize({ width: 390, height: 844 })
    proposalRoutes(page, () => ({ status: 200, body: { rev: 1 } }))
    await openDocument(page)
    await openProposal(page)
    await expect(page.getByRole("button", { name: "Accept proposal" })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
    await page.close()
  })
})
