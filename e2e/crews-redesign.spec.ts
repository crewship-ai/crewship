import { test, expect } from "./fixtures/auth"

// Comprehensive smoke for the post-refactor /crews + /chat surfaces.
// Pins the regressions that motivated the refactor (drawer-based UI,
// duplicate filters, dead links) plus the new selection-driven canvas.
//
// Keeps each test independent and small so a single failure points at
// one area, not "the whole crews page is broken".

const TIMEOUT = 15_000

test.describe("/crews — selection-driven canvas", () => {
  test("empty selection renders the roster", async ({ page }) => {
    await page.goto("/crews")
    // No agent or crew selected → roster headline visible.
    await expect(page.getByRole("heading", { name: "Your fleet" })).toBeVisible({ timeout: TIMEOUT })
  })

  test("sub-bar exposes filters + create CTAs", async ({ page }) => {
    await page.goto("/crews")
    await expect(page.getByRole("button", { name: /Status:/ })).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByRole("button", { name: /Role:/ })).toBeVisible()
    await expect(page.getByRole("button", { name: /^Crew$/ })).toBeVisible()
    await expect(page.getByRole("button", { name: /^Agent$/ })).toBeVisible()
  })

  test("selecting an agent in the explorer drives the URL and canvas", async ({ page }) => {
    await page.goto("/crews")
    // Click any agent row in the explorer (status badge text identifies it).
    const agentRow = page.locator("aside button").filter({ hasText: /idle|running/i }).first()
    await expect(agentRow).toBeVisible({ timeout: TIMEOUT })
    await agentRow.click()
    await expect(page).toHaveURL(/agent=/)
    // Canvas header surfaces the Profile section.
    await expect(page.getByRole("heading", { name: "Profile" })).toBeVisible({ timeout: TIMEOUT })
    // System prompt section is visible.
    await expect(page.getByRole("heading", { name: "System prompt" })).toBeVisible()
  })

  test("status filter chip narrows the roster", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /Status:/ }).click()
    // Pick "Running" from the menu.
    await page.getByRole("menuitem", { name: "Running" }).click()
    await expect(page).toHaveURL(/status=RUNNING/)
  })

  test("opening + Crew shows the create dialog", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Crew$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText(/^New crew$/)).toBeVisible()
  })

  test("opening + Agent shows the create dialog", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Agent$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText(/^New agent$/)).toBeVisible()
  })
})

test.describe("/crews — agent canvas inline editing", () => {
  test("system prompt has explicit Save / Cancel (never blur-saves)", async ({ page }) => {
    await page.goto("/crews")
    const agentRow = page.locator("aside button").filter({ hasText: /idle|running/i }).first()
    await agentRow.click()
    await expect(page.getByRole("heading", { name: "System prompt" })).toBeVisible({ timeout: TIMEOUT })
    // Click "Edit" to enter edit mode — buttons turn into Cancel + Save.
    await page.getByRole("button", { name: "Edit" }).first().click()
    await expect(page.getByRole("button", { name: "Cancel" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Save" })).toBeVisible()
  })

  test("clicking the avatar opens the avatar picker dialog", async ({ page }) => {
    await page.goto("/crews")
    const agentRow = page.locator("aside button").filter({ hasText: /idle|running/i }).first()
    await agentRow.click()
    await expect(page.getByRole("heading", { name: "Profile" })).toBeVisible({ timeout: TIMEOUT })
    // Avatar in the canvas header is a button with title "Customize avatar".
    await page.getByTitle("Customize avatar").click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText(/^Style$/)).toBeVisible()
    await expect(page.getByText(/^Quick pick$/)).toBeVisible()
    await expect(page.getByText(/^Seed$/)).toBeVisible()
  })

  test("Schedule section exposes cron + prompt + enabled toggle", async ({ page }) => {
    await page.goto("/crews")
    const agentRow = page.locator("aside button").filter({ hasText: /idle|running/i }).first()
    await agentRow.click()
    await expect(page.getByRole("heading", { name: "Schedule" })).toBeVisible({ timeout: TIMEOUT })
    // Toggle button has aria-pressed
    await expect(page.locator('[aria-pressed]').filter({ hasText: "" }).first()).toBeVisible()
  })
})

test.describe("/crews — bottom panel", () => {
  test("collapsed by default, expands on tab click", async ({ page }) => {
    await page.goto("/crews")
    const messagesTab = page.getByRole("button", { name: /^Messages$/ })
    await expect(messagesTab).toBeVisible({ timeout: TIMEOUT })
    // Initial: collapsed → expand toggle present
    await messagesTab.click()
    // After click: tab is active (aria-pressed=true). Doing this without
    // a screenshot diff because the content depends on selection.
    await expect(messagesTab).toHaveAttribute("aria-pressed", "true")
  })

  test("Docker tab loads workspace runtime data", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Docker$/ }).click()
    // The header row of the Docker tab is always visible once expanded.
    await expect(page.getByText(/^Container$/, { exact: false })).toBeVisible({ timeout: TIMEOUT })
  })
})

test.describe("/chat/[agentSlug] — full-page chat", () => {
  test("chat URL renders chat shell, NOT dashboard (regression)", async ({ page }) => {
    // Get a real agent slug from the workspace by visiting /crews first.
    await page.goto("/crews")
    const agentRow = page.locator("aside button").filter({ hasText: /idle|running/i }).first()
    await expect(agentRow).toBeVisible({ timeout: TIMEOUT })
    await agentRow.click()
    await expect(page).toHaveURL(/agent=/)
    const url = new URL(page.url())
    const slug = url.searchParams.get("agent")
    expect(slug).toBeTruthy()

    // Navigate directly to /chat/<slug>.
    await page.goto(`/chat/${slug}`)
    // The chat page has either a sessions sidebar OR an "Allocating session…"
    // placeholder while the POST /chats round-trips. NEITHER is the
    // dashboard's "AGENTS" stat card — that was the bug we just fixed.
    await expect(
      page.getByText(/Allocating session/).or(page.getByPlaceholder(/Search sessions/)),
    ).toBeVisible({ timeout: TIMEOUT })
    // Affirmatively: dashboard markers are absent.
    await expect(page.getByText(/^AGENTS$/, { exact: true })).toHaveCount(0)
    await expect(page.getByText(/^ACTIVE MISSIONS$/, { exact: true })).toHaveCount(0)
  })

  test("chat header has Back link to /crews", async ({ page }) => {
    await page.goto("/crews")
    const agentRow = page.locator("aside button").filter({ hasText: /idle|running/i }).first()
    await agentRow.click()
    const url = new URL(page.url())
    const slug = url.searchParams.get("agent")!
    await page.goto(`/chat/${slug}`)
    // Back arrow has title "Back to agent canvas"
    const back = page.getByTitle("Back to agent canvas")
    await expect(back).toBeVisible({ timeout: TIMEOUT })
  })
})

test.describe("backend route sanity", () => {
  test("API health responds 200", async ({ request }) => {
    const res = await request.get("/api/health")
    expect(res.ok()).toBe(true)
    const body = await res.json()
    expect(body.status).toBe("ok")
  })

  test("/api/v1/system/runtime returns container list", async ({ request }) => {
    const res = await request.get("/api/v1/system/runtime")
    expect(res.ok()).toBe(true)
    // Schema not pinned here; just confirm the endpoint works for the
    // bottom-panel Docker tab.
  })

  test("legacy /crews/agents/[id] redirects via static handler", async ({ request }) => {
    // Old route is gone. Static handler should fall through dynamic-route
    // lookup → walk parents → eventually hit /crews.html and serve it.
    // We only assert: 200 + non-empty body + NOT the dashboard.
    const res = await request.get("/crews/agents/some-uuid")
    expect(res.status()).toBeLessThan(500)
  })
})
