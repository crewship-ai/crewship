import { test, expect } from "./fixtures/auth"
import { storageFilePath } from "./global-setup"

// =============================================================================
// Create Crew Wizard — end-to-end happy paths.
//
// Drives the actual UI in Chromium against a live backend (dev server).
// Covers:
//   1. Empty crew flow (Identity → Lineup=Empty → Container=Skip → Review → Create)
//   2. Browse template flow (Identity → Lineup=Template → Container → Review → Create)
//   3. Step strip jump-back navigation
//   4. Container step: image and tooling up front, sizing folded, no MCP
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

  test("empty crew end-to-end via Skip-to-defaults on Container", async ({ page }) => {
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

    // Step 3 — Container
    await expect(page.getByText(/step 3 of 4/i).first()).toBeVisible()
    await expect(page.getByText("Image and tooling")).toBeVisible()
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

  test("Container leads with image and tooling, folds sizing away, and drops MCP", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering", { exact: true }).fill("Container Vis")
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Image and tooling is the first thing on the step, mounted rather than
    // hidden behind a disclosure — and the base-image picker with it.
    await expect(page.getByText("Image and tooling").first()).toBeVisible()
    await expect(page.getByText(/^Base Image$/i).first()).toBeVisible({ timeout: TIMEOUT })

    // Egress is open, and the allowlist is one switch away rather than the
    // default. An allowlist that is still maturing fails as a silent timeout
    // deep inside a run.
    await expect(page.getByText("Open egress")).toBeVisible()
    await page.getByRole("switch", { name: /allowlist/i }).click()
    await expect(page.getByText(/Allowed hosts/)).toBeVisible()
    await page.getByRole("switch", { name: /allowlist/i }).click()

    // Sizing is an administrator's question: collapsed, with its values in
    // the summary so nothing is hidden, only folded.
    const size = page.getByRole("button", { name: /^Size/ })
    await expect(size).toHaveAttribute("aria-expanded", "false")
    await expect(size).toContainText("4 GB")
    await size.click()
    await expect(page.getByText("--memory-mb 4096")).toBeVisible()

    // Tools reach agents through Composio and the integrations surface now.
    await expect(page.getByText(/MCP servers/i)).toHaveCount(0)
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

  test("everything on Container is reachable without hunting for it", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering", { exact: true }).fill("Reachable")
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()

    await expect(page.getByText(/step 3 of 4/i).first()).toBeVisible()

    // This replaces a test that scrolled to find the MCP card. The step used
    // to stack three tall sections and cap the tallest at 280px so the last
    // one stayed above the fold; with MCP gone and sizing folded, the last
    // thing on the step is a summary row.
    const size = page.getByRole("button", { name: /^Size/ })
    await size.scrollIntoViewIfNeeded({ timeout: TIMEOUT })
    await expect(size).toBeVisible()
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
    // Open the icon picker and close it again. It used to be a second Radix
    // Dialog titled "Icon — <crew>" stacked on this one; it is the kit's
    // in-body picker now, so the thing to assert is its search box appearing
    // inside this surface, and the tile toggling it back shut.
    const iconTile = page.getByLabel("Pick icon and color")
    await iconTile.click()
    await expect(page.getByPlaceholder(/search icons/i)).toBeVisible({ timeout: TIMEOUT })
    // Still exactly one dialog — that was the whole point of the change.
    await expect(page.getByRole("dialog")).toHaveCount(1)
    await iconTile.click()
    await expect(page.getByPlaceholder(/search icons/i)).not.toBeVisible({ timeout: TIMEOUT })

    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 2 — Lineup → Empty (template flow has its own seed-race issues)
    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 3 — Container: turn on the allowlist, list a host, and open the
    // sizing fold to pick a non-default memory and TTL. Sizing is folded by
    // default now, which is the point of the click.
    await expect(page.getByText(/step 3 of 4/i).first()).toBeVisible()
    await page.getByRole("switch", { name: /allowlist/i }).click()
    const domainInput = page.locator('input[placeholder*="github.com"]').first()
    await domainInput.fill("github.com")
    await domainInput.press("Enter")
    // Chip text matches "github.com" exactly (the placeholder hint contains the
    // same string, so use exact: true to disambiguate).
    await expect(page.getByText("github.com", { exact: true })).toBeVisible()

    await page.getByRole("button", { name: /^Size/ }).click()
    await page.getByRole("button", { name: "8 GB" }).click()
    await page.getByRole("button", { name: "24 h" }).click()

    // Skip-to-defaults would throw away the image overrides; there are none
    // here, and Continue is the path a user who filled the step in takes.
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 4 — Review: assert summary reflects the values we entered
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
