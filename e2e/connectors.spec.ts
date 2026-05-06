// E2E for the Connectors redesign. Covers Sprint 0 (Marketplace tab
// removal + Connectors copy) and Sprint 1 (catalog-first add flow,
// schema-driven connect sheet, custom-MCP escape hatch).
//
// These tests fail against the current /integrations implementation
// and pass once the implementer ships the changes. Each assertion
// pins one decision from the architecture spec so a regression points
// at exactly one bullet.

import { test, expect } from "./fixtures/auth"

const TIMEOUT = 15_000

// -------------------------------------------------------------------
// Sprint 0 — Marketplace tab removal + Connectors rename
// -------------------------------------------------------------------

test.describe("/integrations — Sprint 0 cleanup", () => {
  test("URL still resolves at /integrations (no breaking redirect)", async ({ page }) => {
    const resp = await page.goto("/integrations")
    expect(resp?.status() ?? 0).toBeLessThan(400)
    await page.waitForLoadState("domcontentloaded")
  })

  test("page no longer shows a Marketplace tab", async ({ page }) => {
    await page.goto("/integrations")
    await page.waitForLoadState("networkidle")
    // The tab strip used to have a "Marketplace" tab next to "Connected".
    // Sprint 0 removes the tab strip entirely (single connected list).
    // Match a tab role + accessible name, not arbitrary text, so an
    // unrelated mention of "marketplace" elsewhere on the page (e.g. in
    // a description string) doesn't accidentally pass the test.
    await expect(page.getByRole("tab", { name: /marketplace/i })).toHaveCount(0)
    await expect(page.getByRole("button", { name: /^marketplace$/i })).toHaveCount(0)
  })

  test("page no longer shows a Connected tab either (single list, no tab strip)", async ({ page }) => {
    await page.goto("/integrations")
    await page.waitForLoadState("networkidle")
    // If we drop Marketplace, we should also drop the "Connected" tab —
    // a one-tab strip is worse UX than no strip. The list lives directly
    // on the page.
    await expect(page.getByRole("tab", { name: /^connected$/i })).toHaveCount(0)
  })

  test("page heading + add button copy uses 'Connector' wording", async ({ page }) => {
    await page.goto("/integrations")
    // Heading and Add button must be re-worded. Match "Connector"
    // (singular OR plural) so we accept either "Connectors" page title
    // or "+ Add Connector" button without binding to an exact form.
    const visibleConnectorText = page.getByText(/connector/i).first()
    await expect(visibleConnectorText).toBeVisible({ timeout: TIMEOUT })
  })
})

// -------------------------------------------------------------------
// Sprint 1 — catalog-first add flow
// -------------------------------------------------------------------

test.describe("/integrations — Sprint 1 catalog", () => {
  test("clicking + Add Connector opens the catalog", async ({ page }) => {
    await page.goto("/integrations")
    // The Add button replaces the previous "Add MCP server" wizard
    // entry. Match by accessible name with a tolerant regex so copy
    // can be "+ Add Connector", "Add Connector", or "Add a connector".
    const addBtn = page.getByRole("button", { name: /add\s*connector/i }).first()
    await expect(addBtn).toBeVisible({ timeout: TIMEOUT })
    await addBtn.click()

    // Catalog must show a search input (the primary affordance).
    await expect(page.getByPlaceholder(/search/i)).toBeVisible({ timeout: TIMEOUT })
  })

  test("catalog shows at least one connector tile from the shipped fixtures", async ({ page }) => {
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    // Linear ships in the canonical fixture set. Its tile must appear
    // in the catalog grid.
    await expect(page.getByText(/^linear$/i)).toBeVisible({ timeout: TIMEOUT })
  })

  test("search filter narrows the visible tiles", async ({ page }) => {
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    await expect(page.getByText(/^linear$/i)).toBeVisible({ timeout: TIMEOUT })

    const search = page.getByPlaceholder(/search/i)
    await search.fill("postgres")

    await expect(page.getByText(/postgresql/i)).toBeVisible()
    await expect(page.getByText(/^linear$/i)).toBeHidden()
  })

  test("clicking a tile opens the connect sheet for that manifest", async ({ page }) => {
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    await page.getByText(/^github$/i).click()
    // PAT manifest → Personal Access Token field must be visible in the sheet.
    await expect(page.getByLabel(/personal access token/i)).toBeVisible({ timeout: TIMEOUT })
  })

  test("custom MCP server escape hatch opens the legacy wizard", async ({ page }) => {
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    // Escape-hatch button is at the bottom of the catalog.
    const customBtn = page.getByRole("button", { name: /custom\s*mcp\s*server/i })
    await expect(customBtn).toBeVisible({ timeout: TIMEOUT })
    await customBtn.click()

    // Legacy wizard's step strip is the most stable identifier.
    // It used to label step 1 as "Source" — bind to that label.
    await expect(page.getByText(/source/i).first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("Slack tile renders the PAT form (bot token + optional team id)", async ({ page }) => {
    // The shipped Slack fixture is PAT mode (bot token paste). A
    // future byo_oauth flavor with setup_md walkthrough will get its
    // own E2E once we ship a real byo_oauth connector — until then
    // there's no shipped byo_oauth fixture to drive this from.
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    await page.getByText(/^slack$/i).click()
    await expect(page.getByLabel(/slack bot token/i)).toBeVisible({ timeout: TIMEOUT })
    // No literal placeholders should leak into the rendered help.
    const literalPlaceholderCount = await page.getByText(/\$\{(field|derived|instance_url)/).count()
    expect(literalPlaceholderCount).toBe(0)
  })

  test("Everything (MCP demo) tile installs without any auth fields", async ({ page }) => {
    // The "everything" connector is the auth_mode=none demo — the
    // catalog must render it as a single Connect button with no
    // form, mirroring the mcp_oauth UX.
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    await page.getByText(/^everything/i).click()
    await expect(page.getByLabel(/token/i)).toHaveCount(0)
    await expect(page.getByLabel(/password/i)).toHaveCount(0)
    await expect(page.getByRole("button", { name: /^connect$/i })).toBeVisible({ timeout: TIMEOUT })
  })

  test("ConnString tile renders host/port/db form fields", async ({ page }) => {
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    await page.getByText(/postgresql/i).click()
    await expect(page.getByLabel(/^host$/i)).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByLabel(/^port$/i)).toBeVisible()
    await expect(page.getByLabel(/^database$/i)).toBeVisible()
    await expect(page.getByLabel(/^user$/i)).toBeVisible()
    await expect(page.getByLabel(/^password$/i)).toBeVisible()
    // Port should default to 5432 from the manifest.
    const port = page.getByLabel(/^port$/i)
    await expect(port).toHaveValue("5432")
  })

  test("MCPOAuth tile shows a single Connect button (no fields)", async ({ page }) => {
    await page.goto("/integrations")
    await page.getByRole("button", { name: /add\s*connector/i }).first().click()

    await page.getByText(/^linear$/i).click()
    // No fields for mcp_oauth — neither labeled "Token" nor "Password".
    await expect(page.getByLabel(/token/i)).toHaveCount(0)
    await expect(page.getByLabel(/password/i)).toHaveCount(0)
    // A Connect button must be present so the user can kick off DCR.
    await expect(page.getByRole("button", { name: /^connect$/i })).toBeVisible({ timeout: TIMEOUT })
  })
})
