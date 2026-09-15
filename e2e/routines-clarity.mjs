// Standalone dev1 acceptance. CREWSHIP_CLARITY_ACCOUNT points to a private
// JSON file {email,password,workspace_id}; no credentials or session are logged.
import fs from "node:fs"
import assert from "node:assert/strict"
import { chromium } from "@playwright/test"
const base = "https://crewship-dev1.unifylab.cz"
const account = JSON.parse(fs.readFileSync(process.env.CREWSHIP_CLARITY_ACCOUNT, "utf8"))
const output = process.env.CREWSHIP_CLARITY_REPORT || "/tmp/crewship-1-clarity-browser.json"
const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
  reducedMotion: "reduce",
})
const page = await context.newPage()
const api = `/api/v1/workspaces/${account.workspace_id}`
const slug = `clarity-proof-${Date.now()}`
const report = { checks: [], runs: [], pageErrors: [] }
const owned = []
page.on("pageerror", (error) => report.pageErrors.push(error.message))
async function request(method, path, data, status = 200) {
  const response = await context.request.fetch(base + path, {
    method,
    data,
    headers: { Origin: base },
  })
  assert.equal(response.status(), status, `${method} ${path}: HTTP ${response.status()}`)
  return response.status() === 204 ? null : response.json()
}
async function publish(definition) {
  const proof = await request("POST", api + "/pipelines/test_run", {
    definition,
    sample_inputs: {
      coverage: 0.7,
      dir: "/crew/shared/project",
      enabled: false,
    },
  })
  const result = await request(
    "POST",
    api + "/pipelines/save",
    {
      slug: definition.name,
      name: definition.name,
      definition,
      save_token: proof.save_token,
    },
    201,
  )
  owned.push(definition.name)
  return result
}
try {
  await page.goto(base + "/login")
  await page.getByLabel(/email/i).fill(account.email)
  await page.getByLabel(/password/i).fill(account.password)
  await page.getByRole("button", { name: /sign in|log in/i }).click()
  await page.waitForURL((url) => !url.pathname.includes("/login"))
  report.version = await request("GET", "/api/v1/system/version")
  const definition = {
    dsl_version: "1.0",
    name: slug,
    agentless: true,
    inputs: [
      {
        name: "coverage",
        label: "Coverage",
        type: "number",
        min: 0,
        max: 1,
        default: 0.7,
      },
      {
        name: "dir",
        label: "Directory",
        type: "string",
        format: "absolute_path",
        default: "/crew/shared/project",
      },
      {
        name: "enabled",
        label: "Enabled",
        type: "boolean",
        widget: "boolean",
        default: true,
      },
    ],
    steps: [
      {
        id: "echo",
        name: "Return provided information",
        type: "transform",
        transform: {
          input: '{"coverage":{{ inputs.coverage }},"enabled":{{ inputs.enabled }}}',
          expression: ".",
        },
      },
    ],
  }
  await publish(definition)
  const detail = await request("GET", api + "/pipelines/" + slug)
  assert(detail.behavior.steps[0].checks.some((s) => s.includes("No output acceptance")))
  for (const inputs of [{ coverage: 70 }, { dir: "../private" }]) {
    const r = await context.request.post(base + api + "/pipelines/" + slug + "/run", {
      data: { inputs },
      headers: { Origin: base },
    })
    assert(r.status() >= 400 && r.status() < 500)
  }
  const before = await request("GET", api + "/pipelines/" + slug + "/run-records")
  assert.equal(before.length, 0)
  report.checks.push("API rejects bounds and path before any run")
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    await page.goto(base + "/routines?slug=" + slug)
    if (width === 1440) {
      const summary = page.getByText("How this routine works · checks and recovery", {
        exact: true,
      })
      await summary.click()
      await summary
        .locator("..")
        .getByText(
          "No output acceptance checks declared. Successful execution alone does not establish result quality.",
          { exact: true },
        )
        .waitFor()
      await summary.click()
      report.checks.push("Recipe displays server-derived configured rules")
    }
    await page.getByRole("button", { name: "Run", exact: true }).click()
    const dialog = page.getByRole("dialog")
    const coverage = dialog.getByLabel("Coverage", { exact: true })
    await coverage.fill("70")
    await dialog.getByLabel("Directory", { exact: true }).fill("../private")
    await dialog.getByRole("button", { name: "Run", exact: true }).click()
    await dialog.getByText("Maximum is 1", { exact: true }).waitFor()
    await page.screenshot({ path: `/tmp/crewship-1-clarity-form-${width}.png` })
    assert.equal(await coverage.getAttribute("aria-invalid"), "true")
    assert(await coverage.evaluate((el) => document.activeElement === el))
    await dialog.getByLabel("Directory", { exact: true }).fill("/crew/shared/project")
    await dialog.getByRole("button", { name: "Restore default", exact: true }).click()
    assert.equal(await coverage.inputValue(), "0.7")
    assert.equal(
      await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
      false,
    )
    report.checks.push(`Form rejects invalid inputs and restores default at ${width}px`)
    await coverage.fill("0")
    await dialog.getByLabel("Enabled", { exact: true }).uncheck()
    const accepted = page.waitForResponse(
      (r) => r.url().endsWith("/pipelines/" + slug + "/run") && r.request().method() === "POST",
    )
    await dialog.getByRole("button", { name: "Run", exact: true }).click()
    const response = await accepted
    assert(response.ok())
    const acceptedRun = await response.json()
    const runId = acceptedRun.run_id || acceptedRun.id
    assert(runId)
    report.runs.push(runId)
    let run
    for (let attempt = 0; attempt < 50; attempt++) {
      run = await request("GET", api + "/pipeline-runs/" + runId)
      if (run.status === "completed") break
      await page.waitForTimeout(200)
    }
    assert.equal(run.status, "completed")
    assert.equal(run.inputs.coverage, 0)
    assert.equal(run.inputs.enabled, false)
    assert.equal(JSON.parse(run.output).coverage, 0)
    assert(run.behavior)
    await page.getByText("Time and attempts", { exact: true }).click()
    await page.getByText(/Longest completed top-level attempt:/).waitFor()
    report.checks.push(`Stored 0/false, pinned behavior and time evidence at ${width}px`)
    await page.getByRole("button", { name: "Run again", exact: true }).click()
    const repeat = page.getByRole("dialog")
    assert.equal(await repeat.getByLabel("Coverage", { exact: true }).inputValue(), "0")
    await repeat.getByRole("button", { name: "Restore default", exact: true }).first().click()
    assert.equal(await repeat.getByLabel("Coverage", { exact: true }).inputValue(), "0.7")
    await repeat.getByRole("button", { name: "Cancel", exact: true }).click()
  }
  const failureDefinition = structuredClone(definition)
  failureDefinition.name = slug + "-failure"
  failureDefinition.steps.push({
    id: "verify",
    name: "Check acceptance example",
    type: "transform",
    transform: {
      input: "{{ inputs.coverage }}",
      expression: 'if . == 0 then error("Acceptance example needs attention") else . end',
    },
  })
  await publish(failureDefinition)
  const failedStart = await request(
    "POST",
    api + "/pipelines/" + failureDefinition.name + "/run",
    { inputs: { coverage: 0 } },
    200,
  )
  const failedId = failedStart.run_id || failedStart.id
  report.runs.push(failedId)
  let failedRun
  for (let attempt = 0; attempt < 50; attempt++) {
    failedRun = await request("GET", api + "/pipeline-runs/" + failedId)
    if (failedRun.status === "failed") break
    await page.waitForTimeout(200)
  }
  assert.equal(failedRun.status, "failed")
  assert(failedRun.step_outputs.echo)
  await page.goto(base + "/routines?slug=" + failureDefinition.name + "&run=" + failedId)
  await page.getByRole("link", { name: "inspect retained results and recorded steps" }).click()
  await page.getByTestId("run-next-step").waitFor()
  report.checks.push("Failed second step retains first output and exposes recovery guidance")
  assert.equal(report.pageErrors.length, 0)
  report.checks.push("No browser page errors")
} catch (error) {
  report.error = String(error)
  process.exitCode = 1
} finally {
  report.cleanup = []
  for (const name of owned) {
    const response = await context.request.delete(base + api + "/pipelines/" + name, {
      headers: { Origin: base },
    })
    report.cleanup.push({ slug: name, status: response.status() })
    if (response.status() !== 204) process.exitCode = 1
  }
  fs.writeFileSync(output, JSON.stringify(report, null, 2), { mode: 0o600 })
  console.log(
    JSON.stringify({
      checks: report.checks,
      error: report.error,
      runs: report.runs,
      cleanup: report.cleanup,
    }),
  )
  await browser.close()
}
