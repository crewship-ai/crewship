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

function seat(id: string, name: string, provider = "ANTHROPIC", over: Record<string, unknown> = {}) {
  return secret(id, name, {
    type: "PROVIDER_LOGIN", provider,
    login: {
      mode: "subscription", provider, plan: null, plan_label: "Max", owner_user_id: "user", owner_email: "demo@example.test",
      expires_at: null, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null }, quota: null,
      delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" }, pays_for: { agents: 1, crews: 0 },
      ...over,
    },
  })
}

const SEATS = [
  seat("seat-max", "Claude Max · Demo"),
  seat("seat-gem", "Gemini · Demo", "GOOGLE", { refresh: { supported: true, status: "needs_relogin", last_at: null, next_at: null, error: "invalid_grant" } }),
  seat("seat-or", "OpenRouter", "OPENROUTER", { mode: "api_key", pays_for: { agents: 0, crews: 0 } }),
]
const MANY_SEATS = Array.from({ length: 13 }, (_, i) => seat(`many-${i}`, `Claude seat ${String(i).padStart(2, "0")}`))
const POOL = {
  id: "pool-1", name: "Claude seats", provider: "ANTHROPIC", mode: "subscription", revision: 2, allow_cross_owner: false, member_count: 1,
  members: [{ credential_id: "seat-max", priority: 0 }],
}

interface Stub { role?: string; failList?: boolean; seats?: unknown[] }

async function stub(page: Page, { role = "OWNER", failList = false, seats = [] }: Stub = {}) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    let status = 200
    let body: unknown = []
    if (path === "/api/auth/session") body = { user: { id: "user", email: "demo@example.test" }, expires: "2099-01-01T00:00:00Z" }
    else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: role, currentUserCapabilities: [] }]
    else if (path === "/api/v1/credentials") {
      if (failList) { status = 503; body = { error: "unavailable" } }
      else body = url.searchParams.get("kind") === "provider_login" ? seats : [...SECRETS, ...seats]
    }
    else if (path === "/api/v1/crews") body = [{ id: "crew-review", name: "Code review", slug: "code-review" }]
    else if (path === "/api/v1/agents") body = [{ id: "ag-rev", slug: "reviewer", name: "Reviewer", crew_id: "crew-review", cli_adapter: "CLAUDE_CODE" }]
    else if (path === "/api/v1/provider-logins/pools") body = { items: [POOL], next_cursor: null }
    else if (path === `/api/v1/provider-logins/pools/${POOL.id}`) body = POOL
    else if (path === "/api/v1/me/preferences") body = {}
    // VLT-04: recorded use per account (no quota, no $).
    else if (path === "/api/v1/paymaster/subscriptions") body = { rows: [{ credential_id: "seat-max", subscription_plan: "Max", provider: "ANTHROPIC", call_count: 47, input_tokens: 900000, output_tokens: 350000, last_ts: new Date(now).toISOString() }] }
    // VLT-01: the pre-save probe. The fixture never echoes the value back.
    else if (path === "/api/v1/credentials/test") body = { valid: true, supported: true, status: 200 }
    else if (/^\/api\/v1\/credentials\/[^/]+\/access\/me$/.test(path)) body = { actions: { read: { state: "allowed" }, edit: { state: "allowed" } } }
    else if (/^\/api\/v1\/credentials\/[^/]+\/dependents$/.test(path)) body = { routines: [] }
    else if (/^\/api\/v1\/credentials\/bindings$/.test(path)) body = { bindings: [] }
    else if (/^\/api\/v1\/credentials\/([^/]+)$/.test(path)) {
      const id = path.split("/").pop()
      const hit = [...SECRETS, ...(seats as { id: string }[])].find((s) => s.id === id)
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

// ── AI providers (Harbor 2, VLT-E/F) ────────────────────────────────────────

for (const [width, height, label] of [[1440, 1000, "desktop"], [390, 844, "phone"]] as const) {
  test(`Q02 AI providers space at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height })
    await stub(page, { seats: SEATS })
    await page.goto("/credentials?space=providers")
    await expect(page.getByRole("heading", { level: 1, name: "AI providers" })).toBeVisible()
    const health = page.getByRole("group", { name: "AI providers health" })
    await expect(health.getByRole("button", { name: /^1 need a new sign-in/i })).toBeVisible()
    await expect(page.locator("[data-slot=account-card]")).toHaveCount(3)
    // No quota from the server: said, never drawn.
    await expect(page.getByText("No usage data yet").first()).toBeVisible()
    await expect(page.getByRole("progressbar")).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: `/tmp/vault-h2-providers-${label}.png` })
  })
}

for (const [width, height] of [[1440, 1000], [1024, 800], [390, 844]] as const) {
  test(`VLT-04 AI account cards show recorded tokens, no quota bars and no $ at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height })
    await stub(page, { seats: SEATS })
    await page.goto("/credentials?space=providers")
    const max = page.locator("[data-slot=account-card]", { hasText: "Claude Max · Demo" })
    await expect(max.getByText("1.3M tokens · 47 calls · last 30 days")).toBeVisible()
    await expect(page.getByText("No usage data yet")).toHaveCount(2)
    await expect(page.getByRole("progressbar")).toHaveCount(0)
    await expect(page.getByText("Near the limit")).toHaveCount(0)
    await expect(page.locator("[data-slot=provider-accounts]")).not.toContainText(/\$\d/)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  })
}

test("Q02 cards→list switch at 13 accounts", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page, { seats: MANY_SEATS })
  await page.goto("/credentials?space=providers")
  const list = page.locator("[data-slot=provider-accounts]")
  await expect(list).toHaveAttribute("data-layout", "list")
  await expect(page.getByRole("table", { name: "AI provider accounts" }).getByRole("row")).toHaveCount(14)
  await page.getByRole("radio", { name: "Cards" }).click()
  await expect(list).toHaveAttribute("data-layout", "cards")
  await expect(page).toHaveURL(/layout=cards/)
})

test("Q02 account group page: accounts, order, settings", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page, { seats: SEATS })
  await page.goto("/credentials?space=providers")
  await page.locator("[data-slot=secondary-panel]").getByText("Claude seats").click()
  await expect(page).toHaveURL(/pool=pool-1/)
  const groupPage = page.locator("[data-slot=account-group-page]")
  await expect(groupPage.getByRole("heading", { level: 1, name: "Claude seats" })).toBeVisible()
  await expect(groupPage.getByText("Claude Max · Demo")).toBeVisible()
  await groupPage.getByRole("tab", { name: "Order" }).click()
  await expect(groupPage.getByRole("list", { name: "Account order" })).toBeVisible()
  await groupPage.getByRole("tab", { name: "Settings" }).click()
  await groupPage.getByRole("button", { name: /Delete…/ }).click()
  await expect(page.getByRole("alertdialog")).toContainText("Delete Claude seats?")
  await page.screenshot({ path: "/tmp/vault-h2-account-group.png" })
})

test("links: ?space=providers&provider= opens the provider's page", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page, { seats: SEATS })
  await page.goto("/credentials?space=providers&provider=GOOGLE")
  await expect(page.getByRole("heading", { level: 1, name: "Gemini / Google" })).toBeVisible()
  await expect(page.getByText("Gemini CLI, OpenCode")).toBeVisible()
})

test("Q03 account page: Pays for lists fitting agents with switches", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page, { seats: SEATS })
  await page.goto("/credentials?space=providers&id=seat-max&tab=access")
  const pageEl = page.locator("[data-slot=secret-page]")
  await expect(pageEl.getByRole("tab", { name: "Pays for" })).toHaveAttribute("aria-selected", "true")
  await expect(pageEl.getByRole("switch", { name: "Claude Max · Demo pays for Reviewer" })).toBeVisible()
  await pageEl.getByRole("button", { name: "AI providers", exact: true }).click()
  await expect(page.getByRole("heading", { level: 1, name: "AI providers" })).toBeVisible()
})

test("Q05 Add AI provider dialog lists the backend catalog", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page, { seats: SEATS })
  await page.goto("/credentials?space=providers")
  await page.getByRole("button", { name: "Add AI provider", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: /Add AI provider/ })
  await expect(dialog.getByRole("group", { name: "Choose AI provider" })).toBeVisible()
  await expect(dialog.getByText(/Mistral/i)).toHaveCount(0)
  await expect(dialog.getByRole("navigation", { name: "Add credential steps" })).toContainText("Pays for")
  await page.screenshot({ path: "/tmp/vault-h2-add-provider.png" })
})

test("shortcuts: keys 1 and 2 switch the spaces", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page, { seats: SEATS })
  await page.goto("/credentials")
  await expect(page.getByRole("heading", { level: 1, name: "All secrets" })).toBeVisible()
  await page.keyboard.press("2")
  await expect(page).toHaveURL(/space=providers/)
  await expect(page.getByRole("heading", { level: 1, name: "AI providers" })).toBeVisible()
  await page.keyboard.press("1")
  await expect(page.getByRole("heading", { level: 1, name: "All secrets" })).toBeVisible()
})

test("VLT-01 Add AI provider: Test checks an API key before saving and never shows it", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page, { seats: SEATS })
  const sent: unknown[] = []
  page.on("request", (r) => { if (r.url().endsWith("/api/v1/credentials/test")) sent.push(r.postDataJSON()) })
  await page.goto("/credentials?space=providers")
  await page.getByRole("button", { name: "Add AI provider", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: /Add AI provider/ })
  await dialog.getByRole("group", { name: "Choose AI provider" }).getByRole("button", { name: /Claude \/ Anthropic/ }).click()
  await dialog.getByRole("button", { name: "API key", exact: true }).click()
  const key = "fixture-" + "k".repeat(32)
  await dialog.getByLabel(/API key/i).last().fill(key)
  await dialog.getByRole("button", { name: "Test", exact: true }).click()
  await expect(dialog.getByText("The key works")).toBeVisible()
  expect(sent).toEqual([{ provider: "ANTHROPIC", type: "PROVIDER_LOGIN", mode: "api_key", value: key }])
  expect(await dialog.textContent()).not.toContain(key)
})

for (const [space, label] of [["secrets", "Filter secrets"], ["providers", "Filter accounts"]] as const) {
  test(`VLT-12 ${space}: Filter opens the facets inside the panel, not a popover`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    await stub(page, { seats: SEATS })
    await page.goto(`/credentials?space=${space}`)
    const panel = page.locator("[data-slot=secondary-panel]")
    const filter = panel.getByRole("button", { name: /^Filter/ })
    await expect(filter).not.toHaveAttribute("aria-haspopup", "dialog")
    await filter.click()
    await expect(panel.getByRole("group", { name: label })).toBeVisible()
    await filter.click()
    await expect(panel.getByRole("group", { name: label })).toHaveCount(0)
  })
}

// ── v67 parity (PAR-075, 079, 080, 149) ──────────────────────────────────────
test("PAR-075/079 header: the space is a chooser and the CLI button is >_", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page)
  await page.goto("/credentials")
  const crumb = page.getByTestId("vault-space-crumb")
  await expect(crumb).toHaveText("Secrets")
  await expect(page.getByRole("button", { name: "Command line" }).locator("svg.lucide-terminal")).toHaveCount(1)
  await crumb.click()
  await expect(page.getByRole("menuitemradio", { name: "AI providers" })).toBeVisible()
})

test("PAR-080 table: state pill is sans, tier L1/L2 is a grey sans chip, Last used stays mono", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page)
  await page.goto("/credentials")
  const row = table(page).locator("tbody tr").first()
  await expect(row.locator("[data-slot=status-pill]")).not.toHaveCSS("font-family", /mono/i)
  const tier = table(page).locator("tbody tr").filter({ hasText: "GitHub token" }).getByText("L2", { exact: true })
  await expect(tier).not.toHaveCSS("font-family", /mono/i)
  await expect(tier).not.toHaveClass(/text-info/)
})

test("PAR-149 Add secret step 1: v67 copy, Continue, AI provider hint, recognition banner", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await stub(page)
  await page.goto("/credentials")
  await page.getByRole("button", { name: "Add secret", exact: true }).first().click()
  const grid = page.getByTestId("shape-grid")
  await expect(grid.getByRole("button")).toHaveCount(6)
  await expect(grid.getByRole("button", { name: /^API key or token/ })).toHaveAttribute("aria-pressed", "true")
  await expect(page.getByTestId("ai-provider-hint")).toContainText("That is an AI provider")
  await page.getByLabel(/paste the value/i).fill("sk_live_" + "x".repeat(24))
  await expect(page.getByTestId("paste-recognised")).toContainText("Looks like a Stripe secret key")
  await page.getByTestId("wizard-footer").getByRole("button", { name: "Continue" }).click()
  await expect(page.locator("#cred-name")).toHaveValue("Stripe secret key")
})
