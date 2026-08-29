import { test, expect, type Page } from "@playwright/test"

// Shell geometry — #2156.
//
// SidebarInset renders <main> as a flex item (`w-full flex-1`). Without
// `min-w-0` it keeps `min-width: auto`, so its min-content width is set by
// whatever the page renders inside it. A page with a wide descendant then
// pushes the inset past the 64px icon rail: the inset measures a full
// viewport wide while starting at x=64, and the last 64px of the page —
// the account menu included — is clipped.
//
// Nothing on the path scrolls horizontally, so document.scrollWidth stays at
// the viewport width and the clipped strip cannot be reached at all. That is
// what makes this worth a geometric test rather than a class assertion: the
// bug is invisible to jsdom (the markup is fine, the layout is not) and
// invisible to an overflow check on <html> (there is no overflow to find,
// only clipping).
//
// /activity?run=<id> is the page that first tripped it — the run steps rail
// renders long single-line step output — but the defect is in the shell, so
// the assertion is "no page's inset exceeds the viewport", not "this page".
//
// Uses the bare @playwright/test `page` rather than the auth fixture: the
// fixture lands every spec on "/" first, and this one measures a freshly
// navigated page. storageState from global-setup still authenticates it.

const VIEWPORT = { width: 1600, height: 950 }

/** Every route that renders inside the shell and is reachable on demo data. */
const PAGES = [
  { name: "Dashboard", path: "/" },
  { name: "Inbox", path: "/inbox" },
  { name: "Activity", path: "/activity" },
  { name: "Routines", path: "/routines" },
]

/**
 * The shell's geometry: where <main> starts, how wide it is, and how wide the
 * viewport is. Read from the live layout, which is the only place this bug
 * exists.
 *
 * Waits for the inset rather than reading whatever is mounted at networkidle:
 * the workspace-gated pages render a skeleton first and swap the shell in
 * afterwards, so an immediate read finds no <main> and fails for the wrong
 * reason.
 */
async function insetGeometry(page: Page) {
  const inset = page.locator("[data-slot=sidebar-inset]")
  await inset.waitFor({ state: "attached", timeout: 15_000 })
  return page.evaluate(() => {
    const main = document.querySelector<HTMLElement>("[data-slot=sidebar-inset]")!
    const r = main.getBoundingClientRect()
    return {
      left: Math.round(r.left),
      right: Math.round(r.right),
      width: Math.round(r.width),
      viewport: document.documentElement.clientWidth,
    }
  })
}

/**
 * The newest routine run in the workspace, so the Activity run detail — the
 * surface that actually carries the wide content — is exercised with real
 * output rather than an empty state. Null on a workspace with no runs, so the
 * test skips instead of hard-failing on a bare database.
 *
 * The runs endpoint is journal-backed: rows are journal entries whose `id` is
 * the entry (`j_…`), and the run is `run_id`.
 */
async function newestRunID(page: Page): Promise<string | null> {
  return page.evaluate(async () => {
    const ws = await fetch("/api/v1/workspaces").then((r) => r.json())
    const wsId = (Array.isArray(ws) ? ws : [ws])[0]?.id
    if (!wsId) return null
    const pipes = await fetch(`/api/v1/workspaces/${wsId}/pipelines`).then((r) => r.json())
    const rows: Array<{ slug?: string }> = Array.isArray(pipes) ? pipes : (pipes?.rows ?? [])
    for (const p of rows) {
      if (!p.slug) continue
      const runs = await fetch(
        `/api/v1/workspaces/${wsId}/pipelines/${encodeURIComponent(p.slug)}/runs?limit=1`,
      )
        .then((r) => r.json())
        .catch(() => null)
      const entries: Array<{ run_id?: string }> = Array.isArray(runs) ? runs : (runs?.rows ?? [])
      const runID = entries.find((e) => e.run_id)?.run_id
      if (runID) return runID
    }
    return null
  })
}

test.use({ viewport: VIEWPORT })

for (const { name, path } of PAGES) {
  test(`${name}: the shell inset stays inside the viewport`, async ({ page }) => {
    await page.goto(path)
    await page.waitForLoadState("networkidle")

    const geo = await insetGeometry(page)

    // The inset sits after the icon rail, so its width is the viewport
    // minus wherever it starts. Anything wider overhangs the right edge
    // into clipped space.
    expect(
      geo.right,
      `${name}: the shell inset's right edge is ${geo.right}px on a ${geo.viewport}px viewport ` +
        `(starts at x=${geo.left}, measures ${geo.width}px) — the overhang is clipped and unreachable`,
    ).toBeLessThanOrEqual(geo.viewport)
  })
}

test("Activity run detail: a wide run does not push the shell off-screen", async ({ page }) => {
  await page.goto("/activity")
  await page.waitForLoadState("networkidle")

  const runID = await newestRunID(page)
  test.skip(runID === null, "no routine runs in this workspace")

  await page.goto(`/activity?run=${encodeURIComponent(runID!)}`)
  await page.waitForLoadState("networkidle")
  // The steps rail streams in after the journal fetch; its content is what
  // makes this page the widest one in the app.
  await page.waitForTimeout(2500)

  const geo = await insetGeometry(page)
  expect(
    geo.right,
    `run detail: the shell inset's right edge is ${geo.right}px on a ${geo.viewport}px viewport ` +
      `(starts at x=${geo.left}, measures ${geo.width}px)`,
  ).toBeLessThanOrEqual(geo.viewport)

  // And the top bar's trailing control is actually on screen — the
  // user-visible symptom was an account menu cut to "DU D".
  const account = page.locator("header").first().locator("button").last()
  if (await account.count()) {
    const box = await account.boundingBox()
    if (box) {
      expect(
        Math.round(box.x + box.width),
        "the top bar's trailing control is fully inside the viewport",
      ).toBeLessThanOrEqual(VIEWPORT.width)
    }
  }
})
