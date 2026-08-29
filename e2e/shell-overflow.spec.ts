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

test("a wide descendant does not push the shell inset off-screen", async ({ page }) => {
  await page.goto("/activity")
  await page.waitForLoadState("networkidle")

  const before = await insetGeometry(page)
  expect(before.right).toBeLessThanOrEqual(before.viewport)

  // Put a known-wide element inside the inset and re-measure.
  //
  // This started out as "open a run and hope its steps are wide", which
  // CodeRabbit rightly flagged: a run that never reached a step renders a
  // short rail, and the assertion then passes against the broken shell too.
  // Chasing a guaranteed-wide run through the journal API only moves the
  // guess. The invariant under test is not "the Activity page is wide" — it
  // is "a wide descendant must not push the inset past the rail", which is
  // exactly what min-w-0 buys and what any page can trip. So state it
  // directly, with content this test owns.
  const injected = await page.evaluate((): boolean => {
    const inset = document.querySelector<HTMLElement>("[data-slot=sidebar-inset]")
    if (!inset) return false
    const probe = document.createElement("div")
    probe.id = "shell-overflow-probe"
    // A fixed width is a min-content floor for a block box, which is the
    // thing `min-width: auto` was refusing to shrink below.
    probe.style.width = "3000px"
    probe.style.height = "1px"
    inset.appendChild(probe)
    return true
  })
  expect(injected, "the shell inset is present to probe").toBe(true)

  const after = await insetGeometry(page)
  expect(
    after.right,
    `a 3000px descendant pushed the shell inset's right edge to ${after.right}px on a ` +
      `${after.viewport}px viewport (starts at x=${after.left}, measures ${after.width}px). ` +
      `The inset must shrink and let the descendant overflow inside it, not grow past the rail.`,
  ).toBeLessThanOrEqual(after.viewport)
})
