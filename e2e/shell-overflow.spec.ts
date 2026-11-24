import { randomUUID } from "node:crypto"
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

test.describe("short chat viewport", () => {
  test.use({ hasTouch: true, isMobile: true })

  for (const viewport of [{ width: 667, height: 375 }, { width: 844, height: 390 }, { width: 915, height: 412 }, { width: 390, height: 844 }]) {
    for (const available of [true, false]) {
      test(`the chat composer fits at ${viewport.width}×${viewport.height} with Docker ${available ? "available" : "unavailable"}`, async ({ page }) => {
        await page.setViewportSize(viewport)
        await page.route("**/api/v1/system/runtime", (route) => route.fulfill({ json: { available } }))
        const runtime = page.waitForResponse("**/api/v1/system/runtime")
        await page.goto("/chat/jordan")
        await runtime
        await expect(page.getByRole("button", { name: "New chat" }).first()).toBeVisible()
        const banner = page.getByText("Docker is not running.", { exact: true })
        if (available) {
          await expect(banner).toBeHidden()
        } else {
          await expect(banner).toBeVisible()
        }

        const editor = page.locator("textarea[data-slot=input-group-control]")
        await expect(editor).toBeVisible()
        await expect.poll(() => editor.evaluate((element) =>
          element.closest("[data-slot=input-group]")!.getBoundingClientRect().bottom -
          element.closest(".surface-pane")!.getBoundingClientRect().bottom,
        )).toBeLessThanOrEqual(1)

        await editor.fill("@jord")
        const suggestion = page.getByRole("option", { name: /@jordan/ })
        await expect(suggestion).toBeVisible()
        await expect.poll(() => suggestion.evaluate((element) => {
          const rect = element.getBoundingClientRect()
          const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
          return !!hit && element.contains(hit)
        })).toBe(true)

        await suggestion.tap()
        await expect(editor).toHaveValue(/@jordan/i)
        await expect(editor).toBeFocused()

        await editor.fill(Array.from({ length: 12 }, (_, i) => `Draft line ${i + 1}`).join("\n"))
        const send = page.locator("form").filter({ has: editor }).locator('button[type="submit"]')
        await expect(send).toBeVisible()
        await expect.poll(() => send.evaluate((element) => {
          const rect = element.getBoundingClientRect()
          const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
          const paneBottom = element.closest(".surface-pane")!.getBoundingClientRect().bottom
          return rect.bottom <= Math.min(paneBottom, window.innerHeight) + 1 &&
            !!hit && element.contains(hit)
        })).toBe(true)
      })
    }
  }
  test("a short chat keeps failed upload actions and the latest reply reachable", async ({ page }) => {
    await page.setViewportSize({ width: 667, height: 375 })
    await page.route("**/api/v1/system/runtime", route => route.fulfill({ json: { available: false } }))
    await page.route("**/api/v1/agents/*/chats/*/attachments?workspace_id=*",
      route => route.fulfill({ status: 413, json: { error: "test upload rejected" } }))
    const roster = page.waitForResponse(response => {
      const url = new URL(response.url())
      return url.pathname === "/api/v1/agents" && url.searchParams.has("workspace_id") && response.ok()
    })
    await page.goto("/chat/jordan")
    const agents = await roster
    const workspace = new URL(agents.url()).searchParams.get("workspace_id")!
    const jordan = (await agents.json() as { id: string; slug: string }[]).find(agent => agent.slug === "jordan")
    expect(jordan).toBeDefined()
    const session = randomUUID()
    const created = await page.request.post(`/api/v1/agents/${jordan!.id}/chats?workspace_id=${workspace}`, {
      data: { session_id: session, origin: "UI" },
    })
    expect(created.ok()).toBe(true)
    await page.route("**/api/v1/chats/*/participants?workspace_id=*", route => route.fulfill({
      json: { participants: Array.from({ length: 5 }, (_, i) => ({ user_id: `test-mention-${i}`, full_name: `Teammate ${i}` })) },
    }))
    await page.route("**/api/v1/chats/*/messages?workspace_id=*", route => route.fulfill({
      json: {
        messages: Array.from({ length: 20 }, (_, i) => ({
          id: `short-chat-message-${i}`,
          role: i % 2 === 0 ? "user" : "assistant",
          content: i === 19 ? "Newest reply in the short chat" : `Earlier chat message ${i + 1}`,
          ts: new Date(Date.UTC(2026, 10, 24, 9, i)).toISOString(),
        })),
      },
    }))
    await page.goto(`/chat/jordan?session=${session}`)
    await expect(page.getByText("Docker is not running.", { exact: true })).toBeVisible()
    const editor = page.locator("textarea[data-slot=input-group-control]")
    await expect(editor).toBeVisible()
    await page.setViewportSize({ width: 667, height: 575 })
    await editor.fill("@")
    const matches = page.getByRole("listbox", { name: "Mention suggestions" })
    await expect(matches.getByRole("option")).toHaveCount(6)
    for (let i = 0; i < 5; i++) await editor.press("ArrowDown")
    await expect(matches.getByRole("option", { selected: true })).toBeInViewport({ ratio: 0.5 })
    await editor.press("Escape")
    await page.setViewportSize({ width: 667, height: 375 })
    await editor.fill(Array.from({ length: 12 }, (_, i) => `Draft line ${i + 1}`).join("\n"))
    const put = page.waitForRequest(request => request.method() === "POST" && request.url().includes("/attachments?"))
    const chooser = page.waitForEvent("filechooser")
    await page.getByRole("button", { name: "Attach files" }).click()
    await (await chooser).setFiles({ name: "sample.txt", mimeType: "text/plain", buffer: Buffer.from("local test attachment") })
    await put
    const failed = page.locator('[data-status="error"]').filter({ hasText: "sample.txt" })
    await expect(failed).toContainText("Upload failed")
    const send = editor.locator("xpath=ancestor::form").locator('button[type="submit"]')
    await expect(send).toHaveAttribute("aria-disabled", "true")
    await expect.poll(() => send.evaluate(element => {
      const rect = element.getBoundingClientRect()
      return element.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2))
    })).toBe(true)
    await failed.getByRole("button", { name: "Retry sample.txt" }).click({ trial: true })
    await failed.getByRole("button", { name: "Remove sample.txt" }).click()
    await expect(failed).toHaveCount(0)
    await page.setViewportSize({ width: 667, height: 575 })
    await editor.fill("")
    const newest = page.getByText("Newest reply in the short chat", { exact: true })
    await expect(newest).toBeInViewport({ ratio: 0.5 })
    await expect(editor).toBeInViewport({ ratio: 0.5 })

    const transcript = page.locator('[data-slot="chat-transcript"]').getByRole("log")
    const history = transcript.locator(":scope > div").first()
    await expect.poll(() => history.evaluate((element) => element.scrollHeight - element.clientHeight)).toBeGreaterThan(80)
    const pane = page.locator('[data-slot="compact-chat-scroll"]')
    await expect.poll(() => pane.evaluate(element => element.scrollTop)).toBeCloseTo(0, 0)
    await history.evaluate(element => { element.scrollTop = 60 })
    const jumpToLatest = transcript.getByRole("button", { name: "Scroll to bottom" })
    await expect(jumpToLatest).toBeVisible()
    await expect(newest).not.toBeInViewport()

    // Model the last turn gaining another block while the user reads upthread.
    await history.evaluate(element => {
      const growth = document.createElement("p")
      growth.dataset.testStreamGrowth = ""
      growth.style.height = "48px"
      growth.textContent = "Another block of the latest answer"
      element.firstElementChild!.appendChild(growth)
    })
    await page.setViewportSize({ width: 667, height: 595 })
    await expect(jumpToLatest).toBeVisible()
    await expect.poll(() => history.evaluate(element => element.scrollTop)).toBeCloseTo(60, 0)
    await history.locator("[data-test-stream-growth]").evaluate(element => element.remove())
    await jumpToLatest.click()
    await expect(newest).toBeInViewport({ ratio: 0.5 })
    await expect(editor).toBeInViewport({ ratio: 0.5 })
  })

})

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
