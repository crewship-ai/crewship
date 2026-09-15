// DEV1 acceptance against owned temporary routines. Account JSON stays private.
import fs from "node:fs"
import assert from "node:assert/strict"
import { chromium, expect } from "@playwright/test"
const base = "https://crewship-dev1.unifylab.cz"
const account = JSON.parse(fs.readFileSync(process.env.CREWSHIP_CLARITY_ACCOUNT, "utf8"))
const api = `/api/v1/workspaces/${account.workspace_id}`
const report = { checks: [], cleanup: [], errors: [] }
const owned = new Set()
const browser = await chromium.launch()
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: "reduce" })
context.on("page", page => page.on("pageerror", error => report.errors.push(error.message)))
const page = await context.newPage()
async function request(method, path, data, status = 200) {
  const response = await context.request.fetch(base + path, { method, data, headers: { Origin: base } })
  assert.equal(response.status(), status, `${method} ${path}: ${response.status()}`)
  return status === 204 ? null : response.json()
}
async function openEdit(target, slug) {
  await target.goto(`${base}/routines?slug=${slug}`)
  await target.getByRole("button", { name: "Edit", exact: true }).click()
  const dialog = target.getByRole("dialog")
  await expect(dialog.getByText(/Loading the current draft/)).toHaveCount(0)
  return dialog
}
async function changeCoverage(dialog, value) {
  await dialog.getByRole("tab", { name: /Inputs/ }).click()
  await dialog.locator("#routine-edit-input-coverage-default").fill(value)
}
try {
  await page.goto(base + "/login")
  await page.getByLabel(/email/i).fill(account.email)
  await page.getByLabel(/password/i).fill(account.password)
  await page.getByRole("button", { name: /sign in|log in/i }).click()
  await page.waitForURL(url => !url.pathname.includes("/login"))
  report.version = await request("GET", "/api/v1/system/version")
  const slug = `operator-proof-${Date.now()}`
  owned.add(slug)
  const definition = { dsl_version: "1.0", name: slug, agentless: true,
    inputs: [{ name: "coverage", label: "Coverage", type: "number", min: 0, max: 1, default: 0.7 }],
    steps: [{ id: "echo", type: "transform", name: "Return input", transform: { input: "{{ inputs.coverage }}", expression: "." } }] }
  const proof = await request("POST", api + "/pipelines/test_run", { definition, sample_inputs: { coverage: 0.7 } })
  await request("POST", api + "/pipelines/save", { slug, name: slug, definition, save_token: proof.save_token }, 201)
  const before = await request("GET", api + "/pipelines/" + slug)
  let dialog = await openEdit(page, slug)
  await dialog.getByLabel(/^Name/).fill("Draft title " + slug)
  await changeCoverage(dialog, "0.5")
  await dialog.getByRole("button", { name: "Save draft", exact: true }).click()
  await expect(dialog).toHaveCount(0)
  const published = await request("GET", api + "/pipelines/" + slug)
  const firstDraft = await request("GET", api + "/pipelines/" + slug + "/draft")
  assert.equal(published.name, before.name)
  assert.equal(published.head_version, before.head_version)
  assert.equal(firstDraft.document.name, "Draft title " + slug)
  assert.equal(firstDraft.document.definition.inputs[0].default, 0.5)
  report.checks.push("Combined name/input edit creates one draft and leaves publication unchanged")

  const second = await context.newPage()
  const a = await openEdit(page, slug)
  const b = await openEdit(second, slug)
  await changeCoverage(a, "0.4")
  await changeCoverage(b, "0.3")
  await a.getByRole("button", { name: "Save draft", exact: true }).click()
  await expect(a).toHaveCount(0)
  const stale = second.waitForResponse(r => r.request().method() === "POST" && r.url().endsWith("/pipelines/drafts"))
  await b.getByRole("button", { name: "Save draft", exact: true }).click()
  assert.equal((await stale).status(), 409)
  await expect(b.getByText(/Someone saved a newer draft or published recipe/)).toBeVisible()
  await expect(b.locator("#routine-edit-input-coverage-default")).toHaveValue("0.3")
  const after = await request("GET", api + "/pipelines/" + slug + "/draft")
  assert.equal(after.document.definition.inputs[0].default, 0.4)
  assert.equal(after.document.name, firstDraft.document.name)
  report.checks.push("Two real editors: stale save returns 409 and preserves both users' text")
  await second.close()

  await page.setViewportSize({ width: 390, height: 1000 })
  await page.goto(base + "/routines")
  await page.getByRole("button", { name: "New routine", exact: true }).click()
  await page.getByRole("button", { name: /Copy an existing routine/ }).click()
  const copied = page.waitForResponse(r => r.request().method() === "POST" && r.url().endsWith("/pipelines/drafts"))
  await page.getByTestId("routine-copy-" + slug).click()
  const copyResponse = await copied
  assert(copyResponse.ok())
  const copy = await copyResponse.json()
  owned.add(copy.slug)
  assert.equal(copy.revision, 1)
  assert.equal(copy.base_pipeline_id, "")
  assert.equal(copy.document.definition.inputs[0].default, 0.7)
  await request("GET", api + "/pipelines/" + copy.slug, undefined, 404)
  await expect(page.getByTestId("routine-publish-button")).toBeVisible()
  await expect(page.getByRole("button", { name: "Run", exact: true })).toBeDisabled()
  await page.getByTestId("routine-publish-button").click()
  const publishDialog = page.getByRole("dialog")
  await expect(publishDialog.getByRole("button", { name: "Publish v1", exact: true })).toBeEnabled()
  await publishDialog.getByRole("button", { name: "Publish v1", exact: true }).click()
  await expect(publishDialog).toHaveCount(0)
  const liveCopy = await request("GET", api + "/pipelines/" + copy.slug)
  assert.equal(liveCopy.head_version, 1)
  assert(!liveCopy.draft)
  report.checks.push("Phone Copy creates an unrunnable draft; explicit Publish creates version 1")
  assert.equal(report.errors.length, 0)
} catch (error) {
  report.error = String(error)
  process.exitCode = 1
  await page.screenshot({ path: "/tmp/routines-2562-recovery-failure.png", fullPage: true }).catch(() => {})
} finally {
  for (const slug of owned) {
    try {
      const draft = await request("GET", api + "/pipelines/" + slug + "/draft")
      if (draft.id) await request("DELETE", api + "/pipelines/" + slug + "/draft", { id: draft.id, revision: draft.revision }, 204)
      const response = await context.request.delete(base + api + "/pipelines/" + slug, { headers: { Origin: base } })
      assert([204, 404].includes(response.status()))
      report.cleanup.push({ slug, status: response.status() })
    } catch (error) { report.cleanup.push({ slug, error: String(error) }); process.exitCode = 1 }
  }
  fs.writeFileSync("/tmp/routines-2562-recovery-browser.json", JSON.stringify(report, null, 2), { mode: 0o600 })
  console.log(JSON.stringify(report))
  await browser.close()
}
