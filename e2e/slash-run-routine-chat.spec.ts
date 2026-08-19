import { test, expect, type Page } from "@playwright/test"

// Running a routine from the CHAT slash palette, in a real browser.
//
// The unit tests drive the modal with a hand-built catalog entry. They cannot
// tell you whether the entry the SERVER produces reaches the palette, whether
// the palette classifies it as runnable rather than greying it out, or whether
// picking the row opens the modal the host mounts. Every one of those is a
// separate seam between four files, and each was written on the assumption
// that the next one behaved.
//
// The header of command-palette.spec.ts states the rule this file inherits: a
// row does not merely render, it OPENS THE THING. Here the thing is a run, and
// the witness is the run's own recorded output — not a toast, which is gone
// before it can be read twice, and not the modal closing, which it would also
// do if the request had 400'd.

// Serial for the same reason as routine-run-inputs.spec.ts: these tests share
// one seeded routine and count its runs, which cannot be asked while a sibling
// is starting one.
test.describe.configure({ mode: "serial" })

const TIMEOUT = 20_000
const SLUG = "e2e-chat-slash-probe"
const LABEL = "E2E chat slash probe"

/** Agentless and instant, and it echoes its inputs so the run itself says
 *  which values arrived. */
function probeDefinition() {
  return {
    dsl_version: "1.0",
    name: SLUG,
    description: "E2E probe for the chat slash palette.",
    agentless: true,
    slash: { enabled: true, label: LABEL, icon: "receipt" },
    inputs: [
      { name: "obdobi", type: "string", description: "YYYY-MM; empty means the previous month" },
      { name: "limit", type: "integer", default: 42 },
    ],
    steps: [
      {
        id: "echo",
        type: "transform",
        transform: { input: "{{ inputs.obdobi }}|{{ inputs.limit }}", expression: "." },
      },
    ],
  }
}

interface Seeded {
  workspaceId: string
  agentSlug: string
}

/**
 * Seed the probe and find an agent to chat with, through the public API from
 * inside the page so both carry the session the suite logged in with.
 *
 * Two-step test_run → save, the protocol `crewship routine save` uses: the
 * store refuses a definition that has not been validated.
 */
async function seedProbe(page: Page): Promise<Seeded> {
  await page.goto("/")
  const seeded = await page.evaluate(async (def) => {
    const workspaces = await (await fetch("/api/v1/workspaces")).json()
    for (const ws of Array.isArray(workspaces) ? workspaces : []) {
      const agents = await (await fetch(`/api/v1/agents?workspace_id=${ws.id}`)).json()
      const agentList = Array.isArray(agents) ? agents : []
      if (!agentList.length) continue
      const crews = await (await fetch(`/api/v1/crews?workspace_id=${ws.id}`)).json()
      const crewList = Array.isArray(crews) ? crews : (crews?.crews ?? [])
      if (!crewList.length) continue

      const testRun = await fetch(`/api/v1/workspaces/${ws.id}/pipelines/test_run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ definition: def, author_crew_id: crewList[0].id }),
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
          author_crew_id: crewList[0].id,
          ...(validated.save_token ? { save_token: validated.save_token } : {}),
        }),
      })
      if (!save.ok) continue
      return { workspaceId: ws.id as string, agentSlug: agentList[0].slug as string }
    }
    return null
  }, probeDefinition())

  expect(
    seeded,
    "no workspace on this instance has both an agent and a crew — seed one first",
  ).not.toBeNull()
  return seeded!
}

async function probeRuns(page: Page, workspaceId: string) {
  return page.evaluate(
    async ([ws, slug]) => {
      const res = await fetch(`/api/v1/workspaces/${ws}/pipelines/${slug}/run-records?limit=10`)
      if (!res.ok) return []
      const body = await res.json()
      const rows = Array.isArray(body) ? body : (body.records ?? body.items ?? [])
      return rows as Array<{ status?: string; output?: string }>
    },
    [workspaceId, SLUG] as const,
  )
}

/** Open the chat slash palette the way the button does. The ⌘/ hotkey is the
 *  other route, but react-hotkeys-hook matches on `event.code`, so a keyboard
 *  layout where Slash sits behind a modifier never fires it — which is why the
 *  host puts a button on screen, and why the button is what this drives. */
async function openSlashPalette(page: Page) {
  const trigger = page.getByTestId("chat-commands-trigger")
  await expect(trigger).toBeVisible({ timeout: TIMEOUT })
  await trigger.click()
  const input = page.locator("[cmdk-input]")
  await expect(input).toBeVisible({ timeout: TIMEOUT })
  return input
}

let seeded: Seeded

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  try {
    seeded = await seedProbe(page)
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
  // Leave the instance as it was found — a probe left behind would appear in
  // every other suite's palette, and in a real workspace's.
  const page = await browser.newPage()
  try {
    await page.goto("/")
    await page.evaluate(
      async ([ws, slug]) => {
        await fetch(`/api/v1/workspaces/${ws}/pipelines/${slug}`, { method: "DELETE" })
      },
      [seeded?.workspaceId ?? "", SLUG] as const,
    )
  } finally {
    await page.close()
  }
})

test.describe("Run a routine from the chat slash palette", () => {
  test("the routine is offered, and is offered as runnable", async ({ page }) => {
    await page.goto(`/chat/${seeded.agentSlug}`)
    await openSlashPalette(page)

    const row = page.getByTestId(`slash-action-routine.run:${SLUG}`)
    await expect(row).toBeVisible({ timeout: TIMEOUT })

    // Enabled, not greyed with a reason. Every unknown catalog id is
    // classified "this build doesn't know how to run this action", so a
    // dispatcher that failed to recognise the routine.run: prefix would still
    // render a row here — a disabled one — and a test that only asserted the
    // row existed would pass on a feature that does nothing.
    await expect(row).not.toHaveAttribute("aria-disabled", "true")
    await expect(row.locator("[data-slash-reason]")).toHaveCount(0)

    // The row teaches the command: the label is prose the author chose, and
    // the thing to type is the slug.
    await expect(row).toContainText(LABEL)
    await expect(row).toContainText(`/${SLUG}`)
  })

  test("typing the slug finds it — which is the command a person is given", async ({ page }) => {
    await page.goto(`/chat/${seeded.agentSlug}`)
    const input = await openSlashPalette(page)

    // Searching on the label alone would hide the routine from the one search
    // term anybody who was TOLD about it actually has.
    await input.fill(SLUG)
    await expect(page.getByTestId(`slash-action-routine.run:${SLUG}`)).toBeVisible({
      timeout: TIMEOUT,
    })
  })

  test("picking it opens its inputs and running starts a run with them", async ({ page }) => {
    const before = await probeRuns(page, seeded.workspaceId)

    await page.goto(`/chat/${seeded.agentSlug}`)
    await openSlashPalette(page)
    await page.getByTestId(`slash-action-routine.run:${SLUG}`).click()

    const dialog = page.getByRole("dialog")
    await expect(dialog).toBeVisible({ timeout: TIMEOUT })
    // Prefilled from the routine's own declaration, and the integer default
    // renders as 42 rather than as the 42.0 a float64 round-trip would give.
    await expect(dialog.getByLabel(/limit/i)).toHaveValue("42")
    await expect(dialog.getByLabel(/obdobi/i)).toHaveValue("")

    await dialog.getByLabel(/obdobi/i).fill("2026-07")
    await dialog.getByRole("button", { name: /^Run$/ }).click()
    await expect(dialog).toBeHidden({ timeout: TIMEOUT })

    await expect
      .poll(async () => (await probeRuns(page, seeded.workspaceId)).length, { timeout: TIMEOUT })
      .toBeGreaterThan(before.length)

    // The run's own output. `2026-07|42` proves the typed string arrived, the
    // untouched default arrived, and the integer arrived as a NUMBER — the one
    // the browser holds as the string "42" and which a `code` step would fail
    // the run on if it were sent that way.
    // "A run with this output exists", not "runs[0] has it" — the seed's own
    // test_run leaves a record too, and indexing is a bet on an ordering this
    // endpoint has never promised.
    const runs = await probeRuns(page, seeded.workspaceId)
    const match = runs.find((r) => r.output === "2026-07|42")
    expect(
      match,
      `no run carried the typed inputs — got ${JSON.stringify(runs.map((r) => r.output))}`,
    ).toBeTruthy()
    expect(match!.status?.toLowerCase()).toBe("completed")
  })
})
