import { test, expect, type Page } from "@playwright/test"

// The ⌘K palette, driven the way a person drives it.
//
// This exists because of a class of bug no unit test can catch. The palette
// pointed at five routes that do not exist — /crews/agents,
// /crews/agents/<id>, /crews/new, /crews/agents/new and /crews/<crewId> — and
// every one of them still answered 200 and rendered the sidebar with an empty
// page under it. A jsdom test can assert what href a row carries; only a
// browser can tell you whether the thing on the other end exists.
//
// The rule each test encodes: a row does not merely navigate, it OPENS THE
// THING. Landing on the index of what you just searched for is the same
// failure as landing nowhere — you are made to search twice.
//
// Run against a fully seeded instance. Rows must exist: missing seed data
// is a failed prerequisite, never a silent skip. The project test owns and
// removes its own fixture so another spec cannot erase its prerequisites.

const TIMEOUT = 20_000

// An instance can hold several workspaces and a fresh browser profile lands
// on whichever the API lists first — which on a dev box is quite possibly the
// empty one. Pin the first workspace that actually has agents, or say plainly
// that there is nothing here to test rather than passing vacuously.
test.beforeEach(async ({ page }) => {
  await page.goto("/")
  const wsId = await page.evaluate(async () => {
    // eslint-disable-next-line no-restricted-syntax -- Browser-evaluated fixtures cannot capture the app apiFetch import.
    const list = await (await fetch("/api/v1/workspaces")).json()
    for (const w of Array.isArray(list) ? list : []) {
      // eslint-disable-next-line no-restricted-syntax -- Browser-evaluated fixtures cannot capture the app apiFetch import.
      const agents = await (await fetch(`/api/v1/agents?workspace_id=${w.id}`)).json()
      if (Array.isArray(agents) && agents.length > 0) return w.id as string
    }
    return null
  })
  expect(wsId, "no workspace on this instance has any agents — seed one first").not.toBeNull()
  await page.evaluate((id) => window.localStorage.setItem("crewship.workspaceId", id as string), wsId)
})

async function openPalette(page: Page) {
  await page.goto("/")
  // No networkidle: the dashboard polls crews-status every 6s and streams
  // realtime, so "the network went quiet" is a state this app may never
  // reach. The visible waits below are the readiness signal.
  //
  // ControlOrMeta, not Meta: on Linux and Windows runners Meta is the Super
  // key, so a hardcoded Meta+k opens nothing and every test here fails for a
  // reason that has nothing to do with the palette.
  const input = page.locator("[cmdk-input]")
  // The static export can paint the toolbar before React installs the global
  // shortcut listener. Open through the idempotent Search button first to
  // establish interactivity, then verify a single keyboard shortcut works.
  await expect(async () => {
    if (!await input.isVisible()) {
      await page.getByRole("button", { name: "Search", exact: true }).filter({ visible: true }).click()
    }
    await expect(input).toBeVisible({ timeout: 1_000 })
  }).toPass({ timeout: TIMEOUT })
  await page.keyboard.press("Escape")
  await expect(input).toBeHidden({ timeout: TIMEOUT })
  await page.keyboard.press("ControlOrMeta+k")
  await expect(input).toBeVisible({ timeout: TIMEOUT })
  // Rows arrive from several parallel fetches; wait for the first one rather
  // than racing them.
  await expect(page.locator("[cmdk-item]").first()).toBeVisible({ timeout: TIMEOUT })
  return input
}

/**
 * The palette's own first row of a given kind, and the text it shows.
 *
 * Reading the row out of the palette instead of typing a known name is what
 * keeps this file instance-agnostic — and it still proves the thing that
 * matters, because the destination comes from the product, not the test.
 */
async function firstRow(page: Page, hrefPattern: RegExp) {
  // The palette fans out over nine lists in parallel, so the group you want
  // may simply not have arrived yet. Scanning once made "no crews in this
  // workspace" and "the crews fetch was 200ms behind" indistinguishable, and
  // the run skipped a test that should have run. Poll until it appears, and
  // only then conclude there is nothing of that kind.
  const deadline = Date.now() + TIMEOUT
  do {
    for (const row of await page.locator("[cmdk-item][data-href]").all()) {
      const href = await row.getAttribute("data-href")
      if (href && hrefPattern.test(href)) {
        return { row, href, text: (await row.innerText()).trim() }
      }
    }
    await page.waitForTimeout(250)
  } while (Date.now() < deadline)
  return null
}

test.describe("⌘K — every row opens the thing it names", () => {
  test("an agent opens that agent on the canvas", async ({ page }) => {
    await openPalette(page)
    const hit = await firstRow(page, /\/crews\?agent=/)
    expect(hit, "seeded palette is missing agents").not.toBeNull()
    await hit!.row.click()

    const slug = decodeURIComponent(/agent=([^&]+)/.exec(hit!.href)![1])
    await expect(page).toHaveURL(/\/crews\?agent=/, { timeout: TIMEOUT })
    // The canvas, not an empty shell: /crews/agents/<id> answered 200 and
    // rendered only the sidebar, so "the URL changed" proves nothing. The
    // agent has to be ON it.
    await expect(page.locator("main")).toContainText(slug, { timeout: TIMEOUT })
    await expect(page.getByText("Current work", { exact: true })).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText("Recent outcomes", { exact: true })).toBeVisible({ timeout: TIMEOUT })
  })

  test("a crew selects that crew, rather than a route that does not exist", async ({ page }) => {
    await openPalette(page)
    const hit = await firstRow(page, /\/crews\?crew=/)
    expect(hit, "seeded palette is missing crews").not.toBeNull()
    await hit!.row.click()

    await expect(page).toHaveURL(/\/crews\?crew=/, { timeout: TIMEOUT })
    // /crews/<id> answered 200 and rendered nothing but the sidebar. A crew
    // that is really selected names itself in the sub-bar.
    await expect(page.getByText(/Crews & Agents/)).toBeVisible({ timeout: TIMEOUT })
  })

  test("a routine opens that routine", async ({ page }) => {
    await openPalette(page)
    const hit = await firstRow(page, /\/routines\?slug=/)
    expect(hit, "seeded palette is missing routines").not.toBeNull()
    const slug = decodeURIComponent(/slug=([^&]+)/.exec(hit!.href)![1])
    await hit!.row.click()

    await expect(page).toHaveURL(/\/routines\?slug=/, { timeout: TIMEOUT })
    await expect(page.locator("main")).toContainText(slug, { timeout: TIMEOUT })
  })

  test("a project filters the board to that project", async ({ page }) => {
    // Own this fixture: selecting a workspace with agents does not guarantee
    // that it has projects, and a missing row must not turn coverage into skip.
    const fixture = await page.evaluate(async () => {
      const workspace = window.localStorage.getItem("crewship.workspaceId")
      // eslint-disable-next-line no-restricted-syntax -- Browser-evaluated fixtures cannot capture the app apiFetch import.
      const response = await fetch(`/api/v1/projects?workspace_id=${workspace}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: `Palette project ${crypto.randomUUID()}` }),
      })
      if (!response.ok) throw new Error(`project fixture failed: ${response.status}`)
      const project = await response.json() as { id: string; name: string }
      return { ...project, workspace }
    })
    try {
      await openPalette(page)
      const row = page.locator(`[cmdk-item][data-href="/issues?project=${fixture.id}"]`)
      await expect(row).toBeVisible({ timeout: TIMEOUT })
      await row.click()
      await expect(page).toHaveURL(new RegExp(`/issues\\?project=${fixture.id}`), { timeout: TIMEOUT })
      await expect(page.locator("main")).toContainText(fixture.name, { timeout: TIMEOUT })
    } finally {
      const cleaned = await page.evaluate(async ({ id, workspace }) => {
        // eslint-disable-next-line no-restricted-syntax -- Browser-evaluated fixtures cannot capture the app apiFetch import.
        const response = await fetch(`/api/v1/projects/${id}?workspace_id=${workspace}`, { method: "DELETE" })
        return response.ok
      }, fixture)
      expect(cleaned, "delete the project created by this test").toBeTruthy()
    }
  })

  test("a person opens that person's row on the roster", async ({ page }) => {
    await openPalette(page)
    const hit = await firstRow(page, /\/settings\?tab=members&member=/)
    expect(hit, "seeded palette is missing members").not.toBeNull()
    await hit!.row.click()

    // Two bugs in one step: the settings tab deep link worked on a full page
    // load but not on a click (the layout read window.location during a
    // client-side transition, so every ?tab= landed on Profile), and the
    // roster then gave no sign of which person had been picked.
    await expect(page).toHaveURL(/tab=members&member=/, { timeout: TIMEOUT })
    await expect(page.getByRole("heading", { name: "Members", exact: true })).toBeVisible({ timeout: TIMEOUT })
    await expect(page.locator('[aria-expanded="true"]').first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("a credential opens that credential, not the vault", async ({ page }) => {
    await openPalette(page)
    const hit = await firstRow(page, /\/credentials\?id=/)
    expect(hit, "seeded palette is missing credentials").not.toBeNull()
    await hit!.row.click()

    await expect(page).toHaveURL(/\/credentials\?id=/, { timeout: TIMEOUT })
    await expect(page.getByRole("button", { name: /Back to credentials/i })).toBeVisible({ timeout: TIMEOUT })
  })
})

test.describe("⌘K — the panel itself", () => {
  test("ranks a name match above a mention of it", async ({ page }) => {
    // "gmail" used to match "Rewrite the HarborliGht reADMe so a newcomer can
    // folLow it" through cmdk's subsequence matcher, and rank it FIRST.
    const input = await openPalette(page)
    await input.fill("gmail")
    // Assert on what cmdk MATCHED (data-value), not on the visible text: a
    // person legitimately matches through their email address while the row
    // shows only their name. What must never survive is a value that merely
    // contains the letters scattered through it.
    for (const row of await page.locator("[cmdk-item]").all()) {
      const value = (await row.getAttribute("data-value")) ?? ""
      expect(value.toLowerCase(), `row "${value}" matched "gmail" as a subsequence`).toContain("gmail")
    }
  })

  test("says which keys drive it", async ({ page }) => {
    await openPalette(page)
    // Scoped to the hint strip: "open" and "close" are ordinary words that
    // appear in result rows and all over the dashboard underneath.
    const hints = page.getByTestId("palette-hints")
    await expect(hints).toContainText("navigate")
    await expect(hints).toContainText("open")
    await expect(hints).toContainText("close")
    await expect(hints.getByText("esc")).toBeVisible()
  })

  test("closes on Escape", async ({ page }) => {
    const input = await openPalette(page)
    await expect(input).toBeVisible()
    await page.keyboard.press("Escape")
    await expect(input).toBeHidden({ timeout: TIMEOUT })
  })
})

test.describe("top bar — one status pill, two bells", () => {
  test("states the connection, and the fleet only when it matters", async ({ page }) => {
    await page.goto("/")
    const pill = page.getByTestId("system-status-pill")
    await expect(pill).toBeVisible({ timeout: TIMEOUT })

    // NOT specifically "Online": that depends on the websocket connecting on
    // the runner, and this PR deliberately hides the fleet half when it has
    // not — so pinning the connected state would fail for the very behaviour
    // it is meant to check. Assert the pill states A known one.
    await expect(pill).toContainText(/Online|Connecting|Reconnecting|Offline/)

    const text = (await pill.innerText()).replace(/\s+/g, " ")
    if (!/Online/.test(text)) {
      // Not connected: the counts are last-known, so the pill must not recite
      // them. That is the whole reason the two pills became one.
      expect(text).not.toMatch(/agents?|errors?|queued/)
    } else {
      // Connected and healthy, the pill says nothing beyond "Online" — it
      // speaks for errors, a queue, or an empty workspace, and otherwise
      // stays quiet. So a plain census is the thing that must never appear.
      expect(text).not.toMatch(/\d+ agents?\b/)
    }
  })

  test("carries Activity and Inbox, and no third bell", async ({ page }) => {
    await page.goto("/")
    await expect(page.getByTestId("activity-trigger")).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByTestId("bell-trigger")).toBeVisible()
    // The notification bell rendered a table nothing in the product ever
    // wrote to, so it was permanently empty by construction.
    await expect(page.getByTestId("notifications-trigger")).toHaveCount(0)
  })

  test("Activity and Inbox draw the same panel chrome", async ({ page }) => {
    await page.goto("/")
    for (const testId of ["activity", "bell"]) {
      await page.getByTestId(`${testId}-trigger`).click()
      const panel = page.getByTestId(`${testId}-popover`)
      await expect(panel).toBeVisible({ timeout: TIMEOUT })
      await page.keyboard.press("Escape")
      await expect(panel).toBeHidden({ timeout: TIMEOUT })
    }
  })
})
