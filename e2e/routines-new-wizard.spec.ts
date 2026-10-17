import { test, expect, type Page } from "@playwright/test"

// New routine — the five-step wizard, driven the way a person drives it.
//
// The unit tests pin every request body; what only a real browser and a real
// server can show is that the chain holds end to end: the slug check answers,
// the server's own cron parser previews the schedule, test_run mints a token
// the save accepts, and the saved routine lands in the panel.

const TIMEOUT = 20_000

function uniqueName(prefix: string) {
  return `${prefix} ${Date.now().toString(36)}`
}
const slugOf = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "")

/** The workspace the app is showing. hooks/use-workspace.ts stores it under
 *  `crewship.workspaceId:<session identity>` (the bare key is the legacy one),
 *  so read whichever is present; fall back to the first workspace. */
async function workspaceId(page: Page): Promise<string> {
  const id = await page.evaluate(async () => {
    const keys = Object.keys(window.localStorage).filter((k) => k === "crewship.workspaceId" || k.startsWith("crewship.workspaceId:"))
    const stored = keys.map((k) => window.localStorage.getItem(k)).find(Boolean)
    if (stored) return stored
    const list = await (await fetch("/api/v1/workspaces")).json()
    const rows = Array.isArray(list) ? list : (list?.data ?? [])
    return (rows[0]?.id as string | undefined) ?? null
  })
  expect(id, "the signed-in session has a workspace").toBeTruthy()
  return id!
}

/** Leave nothing behind: the routine (soft delete) and any draft. */
async function cleanup(page: Page, slug: string) {
  const ws = await workspaceId(page)
  await page.evaluate(
    async ([w, s]) => {
      await fetch(`/api/v1/workspaces/${w}/pipelines/${s}`, { method: "DELETE" }).catch(() => {})
      const draft = await fetch(`/api/v1/workspaces/${w}/pipelines/${s}/draft`).then((r) => (r.ok ? r.json() : null)).catch(() => null)
      if (draft?.id) await fetch(`/api/v1/workspaces/${w}/pipelines/${s}/draft`, { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: draft.id, revision: draft.revision }) })
    },
    [ws, slug] as const,
  )
}

async function openWizard(page: Page) {
  await page.goto("/routines")
  await page.getByRole("button", { name: "New routine" }).first().click()
  const dialog = page.getByRole("dialog", { name: "New routine" })
  await expect(dialog).toBeVisible({ timeout: TIMEOUT })
  return dialog
}

async function basics(page: Page, name: string) {
  const dialog = page.getByRole("dialog", { name: "New routine" })
  await dialog.getByLabel("Name").fill(name)
  await expect(dialog.getByText("Free")).toBeVisible({ timeout: TIMEOUT })
  const crews = dialog.getByRole("radiogroup", { name: "Crew" })
  await crews.getByRole("radio").and(page.locator(":not([disabled])")).first().click()
  await dialog.getByRole("button", { name: "Continue" }).click()
}

test("F03 build a weekly routine with one agent step, test and turn on; it appears with Live", async ({ page }) => {
  const name = uniqueName("E2E weekly")
  const slug = slugOf(name)
  const dialog = await openWizard(page)
  try {
    await basics(page, name)
    await dialog.getByRole("radio", { name: /^On a schedule/ }).click()
    await dialog.getByRole("radio", { name: "Weekly", exact: true }).click()
    await dialog.getByRole("radio", { name: "Th", exact: true }).click()
    await expect(dialog.getByTestId("routine-cron")).toHaveText("0 8 * * 4")
    await expect(dialog.getByTestId("routine-next-start")).toHaveCount(3, { timeout: TIMEOUT })
    await dialog.getByRole("button", { name: "Continue" }).click()

    await dialog.getByRole("radiogroup", { name: "Agent" }).getByRole("radio").first().click()
    await dialog.getByLabel("Instructions").fill("Summarise last week's activity in three bullets.")
    await dialog.getByRole("button", { name: "Continue" }).click()
    await dialog.getByRole("button", { name: "Continue" }).click()

    await dialog.getByRole("button", { name: "Run the test" }).click()
    await expect(dialog.getByText("Test passed")).toBeVisible({ timeout: TIMEOUT })
    const save = page.waitForResponse((r) => r.url().endsWith("/pipelines/save") && r.request().method() === "POST")
    await dialog.getByRole("button", { name: "Save and turn on" }).click()
    const res = await save
    expect(res.status()).toBe(201)
    const body = await res.json()
    expect(body.trigger).toMatchObject({ kind: "schedule", cron_expr: "0 8 * * 4" })
    await expect(page.getByText(new RegExp(`${name} is live · first start`))).toBeVisible({ timeout: TIMEOUT })
    await expect(dialog).toBeHidden()

    // In the panel, and its page opens.
    await page.getByRole("button", { name: new RegExp(name) }).first().click()
    await expect(page).toHaveURL(new RegExp(`slug=${slug}`))
  } finally {
    await cleanup(page, slug)
  }
})

test("F03 copy a routine as draft lands on the draft with no triggers", async ({ page }) => {
  const dialog = await openWizard(page)
  await dialog.getByRole("radio", { name: /Copy a routine/ }).click()
  const source = dialog.locator('[data-testid^="routine-copy-"]').first()
  test.skip(!(await source.count()), "this instance has no published routine to copy")
  await source.click()
  await expect(dialog.getByText(/Copied from/)).toBeVisible({ timeout: TIMEOUT })
  // Back to Basics for a name no earlier run used.
  await dialog.getByRole("button", { name: /^Step 1: Basics/ }).click()
  const name = uniqueName("E2E copy")
  const slug = slugOf(name)
  try {
    await dialog.getByLabel("Name").fill(name)
    await expect(dialog.getByText("Free")).toBeVisible({ timeout: TIMEOUT })
    await dialog.getByRole("button", { name: "Continue" }).click()
    await dialog.getByRole("radio", { name: /^Manually/ }).click()
    for (let i = 0; i < 3; i++) await dialog.getByRole("button", { name: "Continue" }).click()
    const draft = page.waitForResponse((r) => r.url().endsWith("/pipelines/drafts") && r.request().method() === "POST")
    await dialog.getByRole("button", { name: "Save as draft" }).click()
    expect((await draft).status()).toBeLessThan(300)
    await expect(page).toHaveURL(new RegExp(`slug=${slug}`), { timeout: TIMEOUT })
    // A draft has no schedule of its own.
    const ws = await workspaceId(page)
    const schedules = await page.evaluate(async ([w, s]) => (await (await fetch(`/api/v1/workspaces/${w}/pipeline-schedules`)).json()).filter?.((x: { target_pipeline_slug?: string }) => x.target_pipeline_slug === s) ?? [], [ws, slug] as const)
    expect(schedules).toHaveLength(0)
  } finally {
    await cleanup(page, slug)
  }
})

test.describe("F03 phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  test("the wizard is a full-height sheet with 1 / 5 progress and ≥44 px targets", async ({ page }) => {
    const dialog = await openWizard(page)
    const box = await dialog.boundingBox()
    expect(box!.height).toBeGreaterThanOrEqual(840)
    await expect(dialog.getByRole("navigation", { name: "Steps" })).toContainText("1 / 5")
    for (const name of ["Continue", "Cancel", "Close"]) {
      const b = await dialog.getByRole("button", { name, exact: true }).boundingBox()
      expect(b!.height, name).toBeGreaterThanOrEqual(44)
    }
  })
})

test.describe("F03 tablet 1024", () => {
  test.use({ viewport: { width: 1024, height: 900 } })
  test("the summary sits under the steps", async ({ page }) => {
    const dialog = await openWizard(page)
    const summary = await dialog.getByRole("complementary", { name: "Summary" }).boundingBox()
    const name = await dialog.getByLabel("Name").boundingBox()
    expect(summary!.y).toBeGreaterThan(name!.y)
    expect(summary!.x).toBeLessThan(name!.x + 20)
  })
})
