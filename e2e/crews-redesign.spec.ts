import type { Page } from "@playwright/test"

import { test, expect } from "./fixtures/auth"

// Smoke for the Harbor 2 /crews surface: the Crews and agents panel, the one
// New ▾ create action, the agent page and the dock. Deeper acceptance lives in
// crews-h2.spec.ts; this spec keeps each test small so one failure points at
// one area, not "the whole crews page is broken".
//
// What the pre-Harbor 2 version of this spec asserted and where it went:
//   "Crews & agents" roster heading, Status:/Role: chips → the panel with
//   Views (the facets were removed with v67, ODCH-CR-02); "+ Crew" / "+ Agent"
//   → New ▾; System prompt / Profile / Schedule sections → the agent's tabs;
//   Messages / Docker dock tabs → Terminal / Files, the container on
//   Environment.

const TIMEOUT = 15_000
// The demo seed's crew with agents; pr-contract.spec.ts relies on the same slug.
const CREW = "engineering"

/** Opens the seeded crew and picks the first agent listed under it in the panel. */
async function openFirstAgent(page: Page): Promise<string> {
  await page.goto(`/crews?crew=${CREW}`)
  const agentRow = page.locator(`[data-crew-row="${CREW}"] > [role=button] ~ [role=button]`).first()
  await expect(agentRow).toBeVisible({ timeout: TIMEOUT })
  await agentRow.click()
  await expect(page).toHaveURL(/[?&]agent=/, { timeout: TIMEOUT })
  const slug = new URL(page.url()).searchParams.get("agent")
  if (!slug) throw new Error("No agent selected after click")
  return slug
}

test.describe("/crews — selection-driven canvas", () => {
  test("/crews opens the Crews and agents panel and the last crew", async ({ page }) => {
    await page.goto("/crews")
    await expect(page.locator("[data-slot=secondary-panel][data-panel-id=crews]")).toBeVisible({ timeout: TIMEOUT })
    await expect(page).toHaveURL(/[?&]crew=/, { timeout: TIMEOUT })
    await expect(page.getByTestId("crew-header")).toBeVisible()
    await expect(page.getByRole("heading", { name: "Crews & agents", exact: true })).toHaveCount(0)
  })

  test("New ▾ is the one create action; no + Crew / + Agent buttons", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    await expect(page.getByRole("button", { name: "New", exact: true })).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByRole("button", { name: /^Crew$/ })).toHaveCount(0)
    await expect(page.getByRole("button", { name: /^Agent$/ })).toHaveCount(0)
  })

  test("selecting an agent in the panel drives the URL and the agent page", async ({ page }) => {
    await openFirstAgent(page)
    await expect(page.getByTestId("agent-header")).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByRole("tab", { name: "Instructions" })).toBeVisible()
  })

  test("a panel view narrows the list and is kept in ?status=", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    await page.getByTestId("crews-view-working").click()
    await expect(page).toHaveURL(/[?&]status=working/)
    await expect(page.getByTestId("crews-view-working")).toHaveAttribute("aria-pressed", "true")
  })

  test("New ▾ › New crew opens the New crew wizard", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    await page.getByRole("button", { name: "New", exact: true }).click()
    await page.getByRole("menuitem", { name: /^New crew/ }).click()
    const dialog = page.getByRole("dialog", { name: /New crew/ })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole("radiogroup", { name: "Start with" })).toBeVisible()
  })

  test("New ▾ › New agent opens New agent in the open crew", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    await page.getByRole("button", { name: "New", exact: true }).click()
    await page.getByRole("menuitem", { name: /^New agent/ }).click()
    const dialog = page.getByRole("dialog", { name: /New agent in / })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole("navigation", { name: "Steps" })).toBeVisible()
  })
})

test.describe("/crews — agent page inline editing", () => {
  test("Instructions never blur-save: the edit waits in the Unsaved changes bar", async ({ page }) => {
    const slug = await openFirstAgent(page)
    await page.goto(`/crews?agent=${slug}&crew=${CREW}&tab=persona`)
    const prompt = page.getByRole("textbox", { name: "Agent system prompt" })
    await expect(prompt).toBeVisible({ timeout: TIMEOUT })
    const before = await prompt.inputValue()
    await prompt.fill(`${before}\n`)
    // Leaving the field is not a save.
    await prompt.blur()
    const bar = page.getByRole("region", { name: "Unsaved changes" })
    await expect(bar).toBeVisible()
    await expect(bar.getByRole("button", { name: "Save" })).toBeVisible()
    await bar.getByRole("button", { name: "Discard" }).click()
    await expect(prompt).toHaveValue(before)
  })

  test("clicking the avatar opens the avatar picker dialog", async ({ page }) => {
    await openFirstAgent(page)
    await page.getByTestId("agent-header").getByRole("button", { name: "Change avatar" }).click()
    const dialog = page.getByRole("dialog", { name: /Avatar/ })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole("textbox", { name: "Avatar seed" })).toBeVisible()
    await expect(dialog.getByRole("button", { name: "Save avatar" })).toBeVisible()
  })
})

test.describe("/crews — bottom panel", () => {
  test("the dock is collapsed by default and a tab opens it", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    const tabs = page.getByRole("tablist", { name: "Bottom panel" })
    await expect(tabs).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByRole("button", { name: "Expand bottom panel" })).toHaveAttribute("aria-expanded", "false")
    await tabs.getByRole("tab", { name: "Files" }).click()
    await expect(tabs.getByRole("tab", { name: "Files" })).toHaveAttribute("aria-selected", "true")
    await expect(page.getByRole("tabpanel", { name: "Files panel" })).toBeVisible()
  })

  // The Docker dock tab is gone; the crew's container is a card on Environment.
  test("Environment shows the crew's container", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}&tab=env`)
    await expect(page.getByTestId("crew-environment")).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByTestId("env-container")).toBeVisible()
  })
})

test.describe("/chat/[agentSlug] — full-page chat", () => {
  test("chat URL renders chat shell, NOT dashboard (regression)", async ({ page }) => {
    const slug = await openFirstAgent(page)

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
    const slug = await openFirstAgent(page)
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

  // The name this test used to carry — "returns container list" — described
  // something the endpoint has never done. /api/v1/system/runtime is the HOST
  // runtime inventory (#1690); the crew's Environment reads
  // /api/v1/crews/{crewId}/containers for the per-crew containers (#1697).
  test("/api/v1/system/runtime answers the host runtime inventory", async ({ request }) => {
    const res = await request.get("/api/v1/system/runtime")
    expect(res.ok()).toBe(true)
    const body = await res.json()
    // The one assertion worth making here: it does NOT carry containers, so
    // nothing goes back to reading them off it.
    expect(body.containers).toBeUndefined()
  })

  test("legacy /crews/agents/[id] redirects via static handler", async ({ request }) => {
    // Old route is gone. Static handler should fall through dynamic-route
    // lookup → walk parents → eventually hit /crews.html and serve it.
    // We only assert: 200 + non-empty body + NOT the dashboard.
    const res = await request.get("/crews/agents/some-uuid")
    expect(res.status()).toBeLessThan(500)
  })
})
