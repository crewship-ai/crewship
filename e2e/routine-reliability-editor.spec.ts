import { test, expect, type Page } from "@playwright/test"

// B9 (#2362) — the reliability editor, driven the way a person drives it:
// open a schedule's edit dialog, watch the live next-fire-times preview
// answer as the cron/timezone fields change, save, and see the edit land
// on the read-only row the schedules tab already renders (a6).
//
// The unit tests (routine-schedule-editor-dialog.test.tsx) prove the
// dialog's own logic against a mocked preview function. This proves the
// three pieces actually talk to each other through a real browser and a
// real server: the PATCH endpoint, the new preview endpoint, and the
// dialog that calls both.

test.describe.configure({ mode: "serial" })

const TIMEOUT = 20_000
const SLUG = "e2e-reliability-editor-probe"
const SCHEDULE_NAME = "e2e reliability schedule"

function probeDefinition() {
  return {
    dsl_version: "1.0",
    name: SLUG,
    description: "E2E probe for the B9 reliability editor — never actually fires.",
    agentless: true,
    inputs: [{ name: "x", type: "string", default: "probe" }],
    steps: [
      { id: "noop", type: "transform", transform: { input: "{{ inputs.x }}", expression: "." } },
    ],
  }
}

interface Seeded {
  workspaceId: string
  crewId: string
  scheduleId: string
}

async function seedRoutineAndSchedule(page: Page): Promise<Seeded> {
  await page.goto("/")
  const seeded = await page.evaluate(async ({ def, schedName }) => {
    const workspaces = await (await fetch("/api/v1/workspaces")).json()
    for (const ws of Array.isArray(workspaces) ? workspaces : []) {
      const crews = await (await fetch(`/api/v1/crews?workspace_id=${ws.id}`)).json()
      const list = Array.isArray(crews) ? crews : (crews?.crews ?? [])
      if (!list.length) continue
      const crewId = list[0].id

      const testRun = await fetch(`/api/v1/workspaces/${ws.id}/pipelines/test_run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ definition: def, author_crew_id: crewId }),
      })
      if (!testRun.ok) continue
      const validated = await testRun.json()
      if (validated.status !== "DRY_RUN_OK" && validated.status !== "COMPLETED") continue

      const save = await fetch(`/api/v1/workspaces/${ws.id}/pipelines/save`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          slug: def.name,
          name: def.name,
          description: def.description,
          definition: def,
          author_crew_id: crewId,
          ...(validated.save_token ? { save_token: validated.save_token } : {}),
        }),
      })
      if (!save.ok) continue

      // Sweep schedules a previous attempt left behind. Deleting the probe
      // routine (afterAll) soft-deletes the routine only — its schedules
      // survive, still enabled, still listed under the slug — so a retried
      // worker used to find two "e2e reliability schedule" rows and every
      // page-wide locator hit both (#2532).
      const existing = await (await fetch(`/api/v1/workspaces/${ws.id}/pipeline-schedules`)).json()
      for (const row of Array.isArray(existing) ? existing : []) {
        if (row?.name === schedName) {
          await fetch(`/api/v1/workspaces/${ws.id}/pipeline-schedules/${row.id}`, { method: "DELETE" })
        }
      }

      const sched = await fetch(`/api/v1/workspaces/${ws.id}/pipeline-schedules`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: schedName,
          target_pipeline_slug: def.name,
          cron_expr: "0 9 * * *",
          timezone: "UTC",
          enabled: true,
        }),
      })
      if (!sched.ok) continue
      const schedRow = await sched.json()

      return { workspaceId: ws.id as string, crewId: crewId as string, scheduleId: schedRow.id as string }
    }
    return null
  }, { def: probeDefinition(), schedName: SCHEDULE_NAME })

  expect(
    seeded,
    "could not seed the probe routine + schedule — no workspace on this instance has a crew, or save/schedule create is refusing",
  ).not.toBeNull()
  await page.evaluate(
    (id) => window.localStorage.setItem("crewship.workspaceId", id as string),
    seeded!.workspaceId,
  )
  return seeded!
}

let seeded: Seeded

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  try {
    seeded = await seedRoutineAndSchedule(page)
  } finally {
    await page.close()
  }
})

test.beforeEach(async ({ page }) => {
  await page.goto("/")
  await page.evaluate(
    (id) => window.localStorage.setItem("crewship.workspaceId", id as string),
    seeded.workspaceId,
  )
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  try {
    await page.goto("/")
    await page.evaluate(
      async ([ws, slug, scheduleId]) => {
        // The schedule first: routine delete does not cascade to it.
        if (scheduleId) {
          await fetch(`/api/v1/workspaces/${ws}/pipeline-schedules/${scheduleId}`, { method: "DELETE" })
        }
        await fetch(`/api/v1/workspaces/${ws}/pipelines/${slug}`, { method: "DELETE" })
      },
      [seeded?.workspaceId ?? "", SLUG, seeded?.scheduleId ?? ""] as const,
    )
  } finally {
    await page.close()
  }
})

test.describe("Reliability editor — edit, preview, save (B9)", () => {
  test("editing a schedule's cron shows a live preview and the saved cron sticks", async ({ page }) => {
    await page.goto(`/routines?slug=${SLUG}`)

    await page.getByRole("button", { name: "Plan", exact: true }).click()

    // Every locator is scoped to the row of the schedule this run seeded —
    // the tab renders one <li id="schedule-{id}"> per schedule (the deep-link
    // anchor), and a page-wide "Edit schedule <name>" button lookup breaks
    // the moment a second schedule carries the same name (#2532).
    const row = page.locator(`#schedule-${seeded.scheduleId}`)
    await expect(row).toBeVisible({ timeout: TIMEOUT })

    const editButton = row.getByRole("button", { name: `Edit schedule ${SCHEDULE_NAME}`, exact: true })
    await expect(editButton).toBeVisible({ timeout: TIMEOUT })
    await editButton.click()

    const dialog = page.getByRole("dialog")
    await expect(dialog).toBeVisible({ timeout: TIMEOUT })

    // The dialog opens showing a live preview for the SAVED cron/timezone —
    // proof the preview endpoint answers on mount, not only after an edit.
    const preview = dialog.getByTestId("schedule-preview")
    await expect(preview).toContainText(/Next \d+ fire times/i, { timeout: TIMEOUT })

    // Editing the cron re-fetches the preview for the NEW value — the whole
    // point of a server-computed preview over a client-side cron guess.
    await dialog.getByText(/Advanced.*cron expression/).click()
    const cronInput = dialog.getByLabel(/cron expression/i)
    await cronInput.fill("")
    await cronInput.fill("15 3 * * *")
    const timezoneInput = dialog.getByLabel(/timezone/i)
    await timezoneInput.fill("")
    await timezoneInput.fill("Europe/Prague")

    await expect(preview).toContainText("Europe/Prague", { timeout: TIMEOUT })
    await expect(preview).toContainText(/Next \d+ fire times/i, { timeout: TIMEOUT })

    await dialog.getByRole("button", { name: "Save" }).click()
    await expect(dialog).toBeHidden({ timeout: TIMEOUT })

    // The operator console shows recurrence, timezone and next start in
    // separate elements. Keep assertions scoped to this schedule, then read
    // it back from the server so optimistic UI alone cannot satisfy the test.
    await expect(row.getByText("Every day at 03:15", { exact: true })).toBeVisible({ timeout: TIMEOUT })
    await expect(row.getByText("· Europe/Prague", { exact: true })).toBeVisible({ timeout: TIMEOUT })
    await expect(row.getByTestId(`schedule-uses-${seeded.scheduleId}`)).toContainText(/ · next .+ · with:/, { timeout: TIMEOUT })

    const saved = await page.evaluate(async ({ workspaceId, scheduleId }) => {
      const response = await fetch(`/api/v1/workspaces/${workspaceId}/pipeline-schedules`)
      if (!response.ok) throw new Error(`Reading schedules failed: HTTP ${response.status}`)
      const schedules = await response.json()
      return schedules.find((schedule: { id: string }) => schedule.id === scheduleId)
    }, seeded)
    expect(saved).toMatchObject({ cron_expr: "15 3 * * *", timezone: "Europe/Prague", enabled: true })
    expect(Date.parse(saved.next_run_at)).toBeGreaterThan(Date.now())
  })
})
