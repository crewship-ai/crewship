import { test, expect, type Page } from "@playwright/test"

// Harbor 2 Vault (/credentials). The API is stubbed with inert fixtures, so
// these runs need the built frontend only; no secret value exists anywhere in
// this file and none is requested.

const DAY = 24 * 3600 * 1000
const now = Date.now()

function secret(id: string, name: string, over: Record<string, unknown> = {}) {
  return {
    id, name, description: null, type: "API_KEY", provider: "GITHUB", status: "ACTIVE", scope: "WORKSPACE",
    crew_id: null, crew_ids: [], account_label: null, account_email: null, username: null,
    token_expires_at: null, last_checked_at: null, last_error: null,
    last_used_at: new Date(now - DAY).toISOString(), last_used_ips: [], tags: [],
    created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z",
    _count_agent_credentials: 0, agent_names: [], agent_ids: [], mcp_used: false,
    security_level: 2, security_level_label: "L2 · medium", sensitivity: "STANDARD", testable: false,
    ...over,
  }
}

const SECRETS = [
  secret("gh", "GitHub token · Code review", { agent_ids: ["ag-rev"], agent_names: ["Reviewer"] }),
  secret("linear", "Linear token · Triage", { provider: "LINEAR", status: "ERROR", last_error: "401 Unauthorized" }),
  secret("stripe", "Stripe secret key", { provider: "STRIPE", sensitivity: "SEALED", security_level: 4, last_used_at: null }),
  secret("tls", "acme.dev wildcard", { type: "CERTIFICATE", provider: "NONE", token_expires_at: new Date(now + 9 * DAY).toISOString() }),
  ...Array.from({ length: 60 }, (_, i) => secret(`g${i}`, `Service key ${String(i).padStart(2, "0")}`)),
]

interface Stub { role?: string; failList?: boolean }

async function stub(page: Page, { role = "OWNER", failList = false }: Stub = {}) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    let status = 200
    let body: unknown = []
    if (path === "/api/auth/session") body = { user: { id: "user", email: "demo@example.test" }, expires: "2099-01-01T00:00:00Z" }
    else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: role, currentUserCapabilities: [] }]
    else if (path === "/api/v1/credentials") {
      if (failList) { status = 503; body = { error: "unavailable" } }
      else body = url.searchParams.get("kind") === "provider_login" ? [] : SECRETS
    }
    else if (path === "/api/v1/crews") body = [{ id: "crew-review", name: "Code review", slug: "code-review" }]
    else if (path === "/api/v1/agents") body = [{ id: "ag-rev", slug: "reviewer", name: "Reviewer", crew_id: "crew-review" }]
    else if (/^\/api\/v1\/credentials\/[^/]+\/access\/me$/.test(path)) body = { actions: { read: { state: "allowed" }, edit: { state: "allowed" } } }
    else if (/^\/api\/v1\/credentials\/[^/]+\/dependents$/.test(path)) body = { routines: [] }
    else if (/^\/api\/v1\/credentials\/bindings$/.test(path)) body = { bindings: [] }
    else if (/^\/api\/v1\/credentials\/([^/]+)$/.test(path)) {
      const id = path.split("/").pop()
      const hit = SECRETS.find((s) => s.id === id)
      if (hit) body = hit
      else { status = 404; body = { error: "Credential not found" } }
    }
    else if (/settings|config|health/.test(path)) body = {}
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
  })
}

const table = (page: Page) => page.getByRole("table", { name: "Secrets" })

for (const [width, height, label] of [[1440, 1000, "desktop"], [1024, 800, "tablet"], [390, 844, "phone"]] as const) {
  test(`Q01 secrets table at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height })
    await stub(page)
    await page.goto("/credentials")
    await expect(page.getByRole("heading", { level: 1, name: "All secrets" })).toBeVisible()
    // The header row is not drawn on a phone; the rows carry name, type and state.
    await expect(table(page).getByRole("row")).toHaveCount(width < 768 ? 50 : 51)
    await expect(page.getByText("Showing 50 of 64")).toBeVisible()
    // Broken first.
    await expect(table(page).locator("tbody tr").first()).toContainText("Linear token · Triage")
    const health = page.getByRole("group", { name: "Vault health" })
    await expect(health.getByRole("button", { name: /^1 rejected/i })).toBeVisible()
    await expect(health.getByRole("button", { name: /^1 expire within 14 days/i })).toBeVisible()
    // No horizontal page scroll at any width.
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: `/tmp/vault-h2-secrets-${label}.png`, fullPage: false })
  })
}

test("Q01 /credentials?crew=<id> shows only that crew's secrets", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page)
  await page.goto("/credentials?crew=code-review")
  await expect(table(page).getByRole("row")).toHaveCount(2)
  await expect(table(page)).toContainText("GitHub token · Code review")
  await page.reload()
  await expect(table(page).getByRole("row")).toHaveCount(2)
})

test("links: ?class= filters", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page)
  await page.goto("/credentials?class=SEALED")
  await expect(table(page).getByRole("row")).toHaveCount(2)
  await expect(table(page)).toContainText("Stripe secret key")
})

test("Q01 load error never renders the empty vault", async ({ page }) => {
  await stub(page, { failList: true })
  await page.goto("/credentials")
  await expect(page.getByText("Couldn't load the vault")).toBeVisible()
  await expect(page.getByText("The vault is empty")).toHaveCount(0)
  await expect(page.getByRole("button", { name: /retry/i })).toBeVisible()
})

test("links: ?tab=providers opens AI providers", async ({ page }) => {
  await stub(page)
  await page.goto("/credentials?tab=providers")
  await expect(page.getByRole("heading", { level: 1, name: "AI providers" })).toBeVisible()
})

test("Q02 MEMBER deep link ?tab=providers lands on Secrets with note", async ({ page }) => {
  await stub(page, { role: "MEMBER" })
  await page.goto("/credentials?tab=providers")
  await expect(page.getByRole("note")).toHaveText("AI provider accounts are managed by owners and admins.")
  await expect(table(page)).toBeVisible()
})

test("links: ?id= opens the entity page", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page)
  await page.goto("/credentials?id=gh&tab=access")
  const pageEl = page.locator("[data-slot=secret-page]")
  await expect(pageEl.getByRole("heading", { level: 1, name: "GitHub token · Code review" })).toBeVisible()
  await expect(pageEl.getByRole("tab", { name: "Access" })).toHaveAttribute("aria-selected", "true")
  await page.goto("/credentials?id=missing")
  await expect(page.getByText("This secret doesn’t exist")).toBeVisible()
})

test("Q03 secret page at 390 is full width with back", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await stub(page)
  await page.goto("/credentials?id=linear")
  const pageEl = page.locator("[data-slot=secret-page]")
  await expect(pageEl.getByRole("alert").filter({ hasText: "Rejected" })).toBeVisible()
  const box = await pageEl.boundingBox()
  expect(box?.width ?? 0).toBeGreaterThanOrEqual(388)
  await pageEl.getByRole("button", { name: "Secrets", exact: true }).click()
  await expect(table(page)).toBeVisible()
  await page.screenshot({ path: "/tmp/vault-h2-secret-phone.png" })
})
