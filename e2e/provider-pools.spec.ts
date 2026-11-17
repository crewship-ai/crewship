import { test, expect } from "@playwright/test"

// Isolated static-export fixture: every API response is synthetic. Harbor
// account groups are full pages in the OWNER/ADMIN AI providers space.
for (const width of [1280, 390]) {
  test(`account group editing at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const pool = { id: "pool", name: "Team group", provider: "OPENAI", mode: "api_key", revision: 3, member_count: 1, allow_cross_owner: false, members: [{ credential_id: "account", priority: 1 }] }
    const account = { id: "account", name: "Team OpenAI", type: "API_KEY", provider: "OPENAI", status: "ACTIVE", tags: [], crew_ids: [], agent_names: [], last_used_ips: [], created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", security_level: 1, login: { mode: "api_key", provider: "OPENAI", owner_email: "fixture@example.test", owner_user_id: "user", plan: null, expires_at: null, refresh: { supported: false, status: "none" }, quota: null, delivery: { kind: "env", target: "OPENAI_API_KEY" }, pays_for: { agents: 0, crews: 0 } } }
    let updated = false
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "user", email: "fixture@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: "OWNER" }]
      else if (path === "/api/v1/credentials") body = [account]
      else if (path === "/api/v1/provider-logins/pools") body = { items: [pool], next_cursor: null }
      else if (path === "/api/v1/provider-logins/pools/pool") {
        if (route.request().method() === "PUT") {
          expect(route.request().headers()["if-match"]).toBe('"3"')
          expect(route.request().headers()["x-workspace-id"]).toBe("ws")
          expect(route.request().postDataJSON()).not.toHaveProperty("provider")
          expect(route.request().postDataJSON()).toMatchObject({ name: "Updated group", allow_cross_owner: false, members: [{ credential_id: "account", priority: 0 }] })
          Object.assign(pool, route.request().postDataJSON(), { revision: 4 })
          updated = true
          return route.fulfill({ status: 204 })
        }
        body = pool
      } else if (/settings|config|health/.test(path)) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials?space=providers&pool=pool")
    const group = page.locator("[data-slot=account-group-page]")
    await expect(group.getByRole("heading", { name: "Team group", exact: true })).toBeVisible()
    await expect(group.getByText("Team OpenAI", { exact: true })).toBeVisible()
    await group.getByRole("tab", { name: "Settings", exact: true }).click()
    await expect(page).toHaveURL((url) => url.searchParams.get("pool") === "pool" && url.searchParams.get("tab") === "settings")
    await group.getByLabel("Name", { exact: true }).fill("Updated group")
    const box = await group.boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(width + 1)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`provider-pool-editor-${width}.png`) })
    await page.getByRole("region", { name: "Unsaved changes", exact: true }).getByRole("button", { name: "Save", exact: true }).click()
    await expect.poll(() => updated).toBe(true)
    await expect(group.getByRole("heading", { name: "Updated group", exact: true })).toBeVisible()
  })
}

for (const role of ["MANAGER", "MEMBER", "VIEWER"]) {
  test(`${role} cannot access account groups`, async ({ page }) => {
    let reads = 0
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "user", email: "fixture@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: role }]
      else if (/settings|config|health/.test(path)) body = {}
      if (path.includes("provider-logins/pools")) reads++
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials?space=providers&pool=pool")
    await expect(page.getByRole("heading", { name: "The vault is empty", exact: true })).toBeVisible()
    await expect(page.getByText("AI provider accounts are managed by owners and admins.", { exact: true })).toBeVisible()
    await expect(page.getByRole("button", { name: "New account group", exact: true })).toHaveCount(0)
    await expect(page.locator("[data-slot=account-group-page]")).toHaveCount(0)
    expect(reads).toBe(0)
  })
}

// The real exported wizard, with only HTTP responses replaced. Covers both
// layouts and the exact account -> model -> agent provisioning payload.
for (const width of [1280, 390]) {
  test(`Bedrock account onboarding at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 950 })
    let saved = false
    let assigned = false
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      const method = route.request().method()
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "user", email: "fixture@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: "OWNER" }]
      else if (path === "/api/v1/agents") body = [{ id: "agent", name: "Bedrock test agent", cli_adapter: "CLAUDE_CODE" }]
      else if (path === "/api/v1/credentials" && method === "POST") {
        const submitted = route.request().postDataJSON()
        expect(submitted).toMatchObject({ name: "Bedrock EU", type: "PROVIDER_LOGIN", provider: "AWS_BEDROCK", mode: "api_key" })
        expect(JSON.parse(submitted.value)).toMatchObject({ auth_method: "bedrock_api_key", api_key: "synthetic-bedrock-key", region: "eu-central-1", models: ["eu.anthropic.claude-opus-5-5"], eu_only: true })
        saved = true
        return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ id: "bedrock-account" }) })
      } else if (path === "/api/v1/credentials/bedrock-account/bedrock-assign") {
        expect(saved).toBe(true)
        expect(route.request().postDataJSON()).toEqual({ agent_ids: ["agent"], crew_ids: [], model: "eu.anthropic.claude-opus-5-5" })
        assigned = true
        body = { assigned: 1 }
      } else if (path === "/api/v1/credentials/bindings") body = { bindings: [] }
      else if (path === "/api/v1/provider-logins/pools") body = { items: [] }
      else if (/settings|config|health/.test(path)) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials?space=providers")
    await page.getByRole("button", { name: "Add AI provider", exact: true }).first().click()
    const dialog = page.getByRole("dialog")
    await dialog.getByRole("button", { name: /^Amazon Bedrock/ }).click()
    await dialog.getByText("Name and labels", { exact: false }).click()
    await dialog.getByLabel("Name", { exact: true }).fill("Bedrock EU")
    await dialog.getByLabel("API key", { exact: true }).fill("synthetic-bedrock-key")
    await expect(dialog.getByLabel("AWS region")).toHaveValue("eu-central-1")
    await dialog.getByRole("checkbox", { name: /^Claude Opus 5\.5 · EU/ }).check()
    await dialog.getByLabel("Key expires at").fill("2099-01-01T12:00")
    await expect(dialog.getByText(/automatic renewal is not available/i)).toBeVisible()
    await dialog.getByRole("button", { name: "Continue", exact: true }).click()
    await dialog.getByRole("button", { name: "Choose now", exact: true }).click()
    await dialog.getByLabel("Bedrock test agent", { exact: true }).check()
    await expect(dialog.getByLabel("Agent model")).toHaveValue("eu.anthropic.claude-opus-5-5")
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`bedrock-pays-for-${width}.png`) })
    await dialog.getByRole("button", { name: "Save & assign", exact: true }).click()
    await expect.poll(() => assigned).toBe(true)
  })
}
