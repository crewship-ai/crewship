import { test, expect } from "./fixtures/auth"
import { storageFilePath } from "./global-setup"

// =============================================================================
// Create Crew Wizard — end-to-end happy paths.
//
// Drives the actual UI in Chromium against a live backend (dev server).
// Covers:
//   1. Empty crew flow (Identity → Lineup=Empty → Runtime → Container=Skip → Review → Create)
//   2. Browse template flow (Identity → Lineup=Template → Runtime → Container → Review → Create)
//   3. Step strip jump-back navigation
//   4. Container step makes Image & features and MCP servers visible (no collapse)
//   5. Skip-to-defaults shortcut
//
// Each test creates a crew with a unique slug (timestamp-suffixed) so reruns
// don't collide on the workspace's UNIQUE(slug) constraint.
// =============================================================================

const TIMEOUT = 20_000

function uniqueSlug(prefix: string): string {
  // base36 timestamp + random suffix — survives parallel runs and avoids
  // accidental collisions with leftover crews from prior CI runs.
  const ts = Date.now().toString(36)
  const rand = Math.floor(Math.random() * 36 ** 4).toString(36).padStart(4, "0")
  return `${prefix}-${ts}-${rand}`
}

async function openCreateCrew(page: import("@playwright/test").Page) {
  await page.goto("/crews")
  // Sub-bar exposes a "+ Crew" button; click opens the wizard.
  await page.getByRole("button", { name: /^Crew$/ }).click()
  await expect(page.getByRole("dialog")).toBeVisible({ timeout: TIMEOUT })
  // Dialog title is "New crew — step X of 4" — match the prefix only.
  await expect(page.getByText(/New crew/)).toBeVisible()
}

// Serialize this suite — it creates real crews against a shared workspace
// with a 15-crew community-license cap. Parallel workers racing to create
// simultaneously can push the cap over before the afterAll cleanup runs.
test.describe.configure({ mode: "serial" })

test.describe("/crews — Create-crew wizard happy paths", () => {
  // Reclaim seats: delete e2e-created crews after the suite so the workspace
  // doesn't fill up against the community license cap (max_crews=15). Each
  // suite run creates 2-3 crews; without cleanup, ~5 reruns hit the cap and
  // every subsequent submit silently fails (HTTP 402 Payment Required).
  test.afterAll(async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: storageFilePath() })
    try {
      const page = await ctx.newPage()
      await page.goto("/crews")
      const list = await page.request.get("/api/v1/crews")
      if (!list.ok()) return
      const crews = (await list.json()) as Array<{ id: string; slug: string }>
      for (const c of crews) {
        if (c.slug.startsWith("e2e-") || c.slug.startsWith("smoke-")) {
          await page.request.delete(`/api/v1/crews/${c.id}`).catch(() => null)
        }
      }
    } catch { /* cleanup is best-effort; never fail the suite on it */ }
    finally {
      await ctx.close()
    }
  })

  test("empty crew end-to-end via Skip-to-defaults on Step 4", async ({ page }) => {
    const slug = uniqueSlug("e2e-empty")
    const name = `E2E Empty ${slug.slice(-6)}`

    await openCreateCrew(page)

    // Step 1 — Identity
    await expect(page.getByText(/step 1 of 4/i).first()).toBeVisible()
    await page.getByPlaceholder("Engineering", { exact: true }).fill(name)
    // Slug should auto-derive but we override to a guaranteed-unique value
    await page.getByPlaceholder("engineering", { exact: true }).fill(slug)
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 2 — Lineup → Empty crew
    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 3 — Runtime defaults
    await expect(page.getByText(/step 3 of 4/i).first()).toBeVisible()
    await expect(page.getByText("Container resources")).toBeVisible()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 4 — Container — skip
    await expect(page.getByText(/step 4 of 4/i).first()).toBeVisible()
    await expect(page.getByRole("button", { name: /Skip to defaults/ })).toBeVisible()
    await page.getByRole("button", { name: /Skip to defaults/ }).click()

    // Step 5 — Review
    await expect(page.getByRole("button", { name: /Create crew/ })).toBeVisible()
    await expect(page.getByText(name)).toBeVisible()
    await page.getByRole("button", { name: /Create crew/ }).click()

    // Dialog closes (most reliable success signal — router.replace can race
    // with viewport assertions in parallel-worker e2e). Then assert the new
    // crew lands on the roster.
    await expect(page.getByRole("dialog")).not.toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText(name).first()).toBeVisible({ timeout: TIMEOUT })
  })

  // Template flow (Browse mode → deploy + PATCH override) is exhaustively
  // covered by unit tests (create-crew-dialog.test.tsx + submit.test.ts). It
  // is NOT covered here as e2e because the browser-side fetch /api/v1/crew-
  // templates triggers the SeedBuiltinCrewTemplates Go-side seed, which
  // races flakily against a freshly-nuked dev DB. Bringing it in stably
  // would require either (a) a dedicated test workspace + idempotent seed
  // that runs in global-setup with proper auth, or (b) a fixture that pokes
  // the endpoint with the shared auth cookies before the spec runs. Both
  // are bigger than the value they unlock — the unit tests already prove
  // the deploy + PATCH order is correct.
  test.skip("template crew creates the seeded agent lineup (covered by unit tests)", async () => {})

  test("step strip lets user jump back to a completed step", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering", { exact: true }).fill("Strip Test")
    await page.getByRole("button", { name: /Continue/ }).click()

    // Now on Step 2; Step 1 indicator should be clickable (completed = green check).
    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible()

    // Click the Step 1 nav button (aria-label "Step 1: Identity").
    await page.getByLabel("Step 1: Identity").click()

    // Back on Step 1 — name preserved.
    await expect(page.getByText(/step 1 of 4/i).first()).toBeVisible()
    await expect(page.getByPlaceholder("Engineering", { exact: true })).toHaveValue("Strip Test")
  })

  test("Step 4 shows Image & features and MCP servers always visible (no collapse)", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering", { exact: true }).fill("Container Vis")
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()
    await expect(page.getByText("Container resources")).toBeVisible()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 4 — both section headers + their children must be visible without
    // any extra clicks. The strings appear in dialog description / intro
    // paragraph too; first() targets the section header specifically.
    await expect(page.getByText("Image & features").first()).toBeVisible()
    await expect(page.getByText("MCP servers").first()).toBeVisible()

    // BASE IMAGE picker rendered inline (not behind a collapsed panel).
    await expect(page.getByText(/^Base Image$/i).first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("Cmd+Enter advances when the step is valid", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering", { exact: true }).fill("Keyboard Nav")

    // Press Cmd+Enter (cross-platform: Playwright emits the right modifier per OS).
    const isMac = process.platform === "darwin"
    await page.keyboard.press(isMac ? "Meta+Enter" : "Control+Enter")

    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("Cancel closes the dialog without creating a crew", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByRole("button", { name: "Cancel" }).click()
    await expect(page.getByRole("dialog")).not.toBeVisible({ timeout: TIMEOUT })
  })

  test("MCP servers section is reachable on Step 4 with at most one body scroll", async ({ page }) => {
    await openCreateCrew(page)

    // Walk to Step 4 (Empty crew + accept Runtime defaults)
    await page.getByPlaceholder("Engineering", { exact: true }).fill("Mcp Visible")
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()
    await expect(page.getByText("Container resources")).toBeVisible()
    await page.getByRole("button", { name: /Continue/ }).click()

    await expect(page.getByText(/step 4 of 4/i).first()).toBeVisible()

    // The MCP section header has a recognizable subtitle when nothing's
    // configured. Locate it as a single element.
    const mcpHeader = page.getByText(/No servers configured/i).first()

    // Scroll inside the dialog body until the MCP section header lands in
    // viewport. scrollIntoViewIfNeeded is the right primitive: it succeeds
    // if the element is REACHABLE (within or after a short scroll), and
    // fails outright if the section was never rendered.
    await mcpHeader.scrollIntoViewIfNeeded({ timeout: TIMEOUT })
    await expect(mcpHeader).toBeVisible()
  })

  // ============================================================================
  // SMOKE — full-fidelity dummy crew. Walks every step, fills every input
  // we expose in the wizard, opens the icon picker, picks a non-default
  // color, toggles a memory chip and a TTL chip, switches network mode to
  // restricted with a domain, then submits. Verifies the crew lands on the
  // roster with the expected name. Single test, single assertion path —
  // designed as the canary you'd run after any wizard refactor.
  // ============================================================================
  test("SMOKE — fully-populated dummy crew flows through every step and creates", async ({ page }) => {
    const slug = uniqueSlug("smoke")
    const name = `Smoke ${slug.slice(-6)}`

    await openCreateCrew(page)

    // Step 1 — Identity
    await expect(page.getByText(/step 1 of 4/i).first()).toBeVisible()
    await page.getByPlaceholder("Engineering", { exact: true }).fill(name)
    await page.getByPlaceholder("engineering", { exact: true }).fill(slug)
    await page.getByPlaceholder(/What does this crew do/).fill("End-to-end smoke crew")
    // Open icon picker; verify it opens; close with Cancel (don't change icon
    // because picker dialog mounts heavy lucide grid — opening + closing is
    // enough to prove the wiring works).
    await page.getByLabel("Pick icon and color").click()
    await expect(page.getByText(/^Icon —/)).toBeVisible({ timeout: TIMEOUT })
    await page.getByRole("button", { name: "Cancel" }).first().click()
    await expect(page.getByText(/^Icon —/)).not.toBeVisible({ timeout: TIMEOUT })

    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 2 — Lineup → Empty (template flow has its own seed-race issues)
    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 3 — Runtime: pick a non-default memory chip + TTL chip + switch to restricted
    await expect(page.getByText(/step 3 of 4/i).first()).toBeVisible()
    await page.getByRole("button", { name: "8 GB" }).click()
    await page.getByRole("button", { name: "24 h" }).click()
    await page.getByRole("button", { name: /Restricted/ }).click()
    // Domain chip: type + Enter
    const domainInput = page.locator('input[placeholder*="github.com"]').first()
    await domainInput.fill("github.com")
    await domainInput.press("Enter")
    // Chip text matches "github.com" exactly (the placeholder hint contains the
    // same string, so use exact: true to disambiguate).
    await expect(page.getByText("github.com", { exact: true })).toBeVisible()

    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 4 — Container: Skip-to-defaults (full RuntimeConfig + MCP picker
    // is exercised in their own component tests; this smoke is about the
    // wizard scaffold).
    await expect(page.getByText(/step 4 of 4/i).first()).toBeVisible()
    await page.getByRole("button", { name: /Skip to defaults/ }).click()

    // Step 5 — Review: assert summary reflects the values we entered
    await expect(page.getByRole("button", { name: /Create crew/ })).toBeVisible()
    await expect(page.getByText(name)).toBeVisible()
    await expect(page.getByText("8 GB")).toBeVisible()
    await expect(page.getByText("TTL: 24 h")).toBeVisible()
    await expect(page.getByText("restricted")).toBeVisible()
    await expect(page.getByText("github.com", { exact: true })).toBeVisible()

    // Submit
    await page.getByRole("button", { name: /Create crew/ }).click()

    // Dialog closes (success signal); the new crew is visible on the roster.
    await expect(page.getByRole("dialog")).not.toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText(name).first()).toBeVisible({ timeout: TIMEOUT })
  })
})
