import { test, expect, type Page } from "@playwright/test"

// Running a routine WITH ITS INPUTS, driven the way a person drives it.
//
// This exists because of a class of bug the unit tests cannot reach. Both new
// surfaces here are forms whose values are translated twice — a routine's
// declared input types become widgets on the way out, and the filled widgets
// become typed JSON on the way back — and jsdom will happily tell you a
// `<input type="number">` holds "42" while the real browser is what decides
// whether the value the server receives is `42` or `"42"`. A routine's `code`
// step sees inputs with their original types, so that difference fails a run.
//
// The rule each test encodes: filling the form is not the point — the point is
// that the run STARTS and starts with the values that were typed. A form that
// opens, accepts input and posts an empty `inputs` object is the exact bug this
// whole change exists to fix, and it would pass any test that only asserted the
// dialog appeared.
//
// The routine is seeded by the spec rather than assumed. Nothing on a seeded
// instance carries a `slash` block (it is opt-in, and rightly so), and a test
// that skipped when it found none would go quietly green on the very instance
// where the feature is broken.

// Serial, against the suite's fullyParallel default. These tests share one
// seeded routine AND its run history: two of them assert on how many runs
// exist, which is not a question you can ask while a sibling test is starting
// one. Run in parallel, "cancelling starts nothing" failed on a run the
// previous test had started — a true statement about the wrong subject.
test.describe.configure({ mode: "serial" })

const TIMEOUT = 20_000

/** A period string no other invocation will have used. The probe routine
 *  echoes its inputs, so this is what lets a test recognise ITS run among
 *  whatever earlier runs are still on the record. */
function uniquePeriod() {
  return `2026-07-${Date.now().toString(36)}`
}

/** The routine this feature was built against, reduced to something
 *  agentless and instant: three declared inputs, two with defaults, and a
 *  transform that echoes what it was given so the run's own output proves
 *  which values arrived. */
const SLUG = "e2e-slash-run-probe"

function probeDefinition() {
  return {
    dsl_version: "1.0",
    name: SLUG,
    description: "E2E probe — echoes its inputs so the run proves what arrived.",
    agentless: true,
    slash: {
      enabled: true,
      label: "E2E slash run probe",
      icon: "receipt",
    },
    inputs: [
      { name: "obdobi", type: "string", description: "YYYY-MM; empty means the previous month" },
      { name: "ucetnictvi_root", type: "string", default: "Unify - Účetnictví" },
      { name: "limit", type: "integer", default: 42 },
    ],
    steps: [
      {
        id: "echo",
        type: "transform",
        transform: {
          input: "{{ inputs.obdobi }}|{{ inputs.ucetnictvi_root }}|{{ inputs.limit }}",
          expression: ".",
        },
      },
    ],
  }
}

interface Seeded {
  workspaceId: string
  crewId: string
}

/**
 * Seed the probe routine through the public API, from inside the page so it
 * carries the session the whole suite already logged in with.
 *
 * Uses the same two-step test_run → save protocol `crewship routine save`
 * uses: the store refuses a definition that has not been validated, and the
 * save_token is what proves it was.
 */
async function seedProbeRoutine(page: Page): Promise<Seeded> {
  await page.goto("/")
  const seeded = await page.evaluate(async (def) => {
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
      return { workspaceId: ws.id as string, crewId: crewId as string }
    }
    return null
  }, probeDefinition())

  expect(
    seeded,
    "could not seed the probe routine — no workspace on this instance has a crew, or save is refusing",
  ).not.toBeNull()
  await page.evaluate(
    (id) => window.localStorage.setItem("crewship.workspaceId", id as string),
    seeded!.workspaceId,
  )
  return seeded!
}

/** Every run of the probe, newest first. The run record is the only witness
 *  that survives the page — a toast is gone before you can read it twice. */
async function probeRuns(page: Page, workspaceId: string) {
  return page.evaluate(
    async ([ws, slug]) => {
      const res = await fetch(
        `/api/v1/workspaces/${ws}/pipelines/${slug}/run-records?limit=50`,
      )
      if (!res.ok) return []
      const body = await res.json()
      const rows = Array.isArray(body) ? body : (body.records ?? body.items ?? [])
      return rows as Array<{ status?: string; output?: string }>
    },
    [workspaceId, SLUG] as const,
  )
}

let seeded: Seeded

// Seeded ONCE, not per test. The save protocol's first step is a
// `test_run`, which for an agentless routine really executes and lands a
// run record — asynchronously. Re-seeding before every test meant that
// record could arrive in the middle of one, and "cancelling starts
// nothing" duly failed on a run it had not started. Seeding once removes
// the interference rather than sleeping around it.
test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  try {
    seeded = await seedProbeRoutine(page)
  } finally {
    await page.close()
  }
})

test.beforeEach(async ({ page }) => {
  // Each test gets its own context, so the workspace pick has to be
  // restated even though the routine is already there.
  await page.goto("/")
  await page.evaluate(
    (id) => window.localStorage.setItem("crewship.workspaceId", id as string),
    seeded.workspaceId,
  )
})

test.afterAll(async ({ browser }) => {
  // Leave the instance as it was found. A probe routine left behind would
  // show up in every other suite's palette — and in a real workspace's.
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

test.describe("Run a routine with its inputs — routines detail page", () => {
  test("Run opens the routine's inputs, prefilled from its own defaults", async ({ page }) => {
    await page.goto(`/routines?slug=${SLUG}`)
    const runButton = page.getByRole("button", { name: /^Run$/ })
    await expect(runButton).toBeVisible({ timeout: TIMEOUT })
    await runButton.click()

    const dialog = page.getByRole("dialog")
    await expect(dialog).toBeVisible({ timeout: TIMEOUT })

    // The two declared defaults arrive filled in; the one with none opens
    // empty, because for this routine an empty period is what means "last
    // month". This is the difference between a form and a form that knows
    // what the routine wants.
    await expect(dialog.getByLabel(/ucetnictvi root/i)).toHaveValue("Unify - Účetnictví")
    await expect(dialog.getByLabel(/limit/i)).toHaveValue("42")
    await expect(dialog.getByLabel(/obdobi/i)).toHaveValue("")
  })

  test("submitting the form starts a run carrying the typed values", async ({ page }) => {
    // A value only this invocation uses. Counting runs cannot work — the
    // records endpoint is paginated, so once history reaches the page size
    // the count stops growing and "more than before" is unsatisfiable. And
    // polling for a FIXED output is worse than useless: a run left by an
    // earlier invocation carries the same string, so the assertion passes
    // without anything having happened. A unique input fixes both, because
    // only the run this test started can produce this output.
    const period = uniquePeriod()

    await page.goto(`/routines?slug=${SLUG}`)
    await page.getByRole("button", { name: /^Run$/ }).click()
    const dialog = page.getByRole("dialog")
    await expect(dialog).toBeVisible({ timeout: TIMEOUT })

    await dialog.getByLabel(/obdobi/i).fill(period)
    await dialog.getByRole("button", { name: /^Run$/ }).click()
    await expect(dialog).toBeHidden({ timeout: TIMEOUT })

    // The run's own output is the assertion. The routine echoes
    // `obdobi|ucetnictvi_root|limit`, so this proves three things at once:
    // the typed value arrived, the untouched default arrived, and the
    // integer arrived as 42 rather than as the string a text box holds.
    const want = `${period}|Unify - Účetnictví|42`
    await expect
      .poll(
        async () => (await probeRuns(page, seeded.workspaceId)).find((r) => r.output === want),
        { timeout: TIMEOUT },
      )
      .toBeTruthy()

    const match = (await probeRuns(page, seeded.workspaceId)).find((r) => r.output === want)
    expect(match!.status?.toLowerCase()).toBe("completed")
  })

  test("the run reaches the Activity surface", async ({ page }) => {
    // The acceptance criterion is "the run shows up in Activity with the right
    // inputs", and a run record existing in the API is not that — the surface
    // a person actually watches is a separate query, a separate projection and
    // a separate render. A run that completes and never appears there is
    // indistinguishable, to the user, from one that never started.
    await page.goto(`/routines?slug=${SLUG}`)
    await page.getByRole("button", { name: /^Run$/ }).click()
    const dialog = page.getByRole("dialog")
    await expect(dialog).toBeVisible({ timeout: TIMEOUT })
    await dialog.getByLabel(/obdobi/i).fill(uniquePeriod())
    await dialog.getByRole("button", { name: /^Run$/ }).click()
    await expect(dialog).toBeHidden({ timeout: TIMEOUT })

    // Inline first: the panel surfaces the run it just started, which is what
    // makes the button feel like it did something.
    await expect(page.getByTestId("run-activity").first()).toBeVisible({ timeout: TIMEOUT })

    // And on the workspace-wide surface, named.
    await page.goto("/activity")
    await expect(page.getByText(SLUG).first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("cancelling starts nothing", async ({ page }) => {
    // Watch the wire, not the run list. Proving a negative by counting runs
    // means sleeping first, and any sleep short enough to be tolerable is
    // also short enough for a wrongly-started run to land just after it —
    // the test would go green ON the bug it exists to catch. The request
    // either left the browser or it did not, and that is knowable exactly.
    const runRequests: string[] = []
    page.on("request", (req) => {
      if (req.method() === "POST" && /\/pipelines\/[^/]+\/run$/.test(req.url())) {
        runRequests.push(req.url())
      }
    })

    await page.goto(`/routines?slug=${SLUG}`)
    await page.getByRole("button", { name: /^Run$/ }).click()
    const dialog = page.getByRole("dialog")
    await expect(dialog).toBeVisible({ timeout: TIMEOUT })
    await dialog.getByLabel(/obdobi/i).fill("2026-09")
    await dialog.getByRole("button", { name: /Cancel/i }).click()
    await expect(dialog).toBeHidden()

    // Filled in and then cancelled — a dialog that fires the run on the way
    // out would be worse than no dialog at all, because the old button at
    // least did what it said.
    expect(runRequests).toEqual([])
  })
})
