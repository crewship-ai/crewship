import { test, expect } from "@playwright/test"

for (const role of ["MANAGER", "MEMBER", "VIEWER"]) {
  test(`${role} cannot open provider administration`, async ({ page }) => {
    let providerReads = 0
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url())
      let body: unknown = []
      if (url.pathname === "/api/auth/session") body = { user: { id: "ui-test", email: "ui@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (url.pathname === "/api/v1/workspaces") body = [{ id: "ui-workspace", name: "Browser fixture", slug: "browser-fixture", currentUserRole: role }]
      else if (url.pathname.includes("/settings") || url.pathname.includes("/config") || url.pathname.includes("/health")) body = {}
      if (url.searchParams.get("kind") === "provider_login") providerReads++
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials?tab=providers")
    await expect(page.getByText("No credentials yet", { exact: true })).toBeVisible()
    await expect(page.getByRole("button", { name: "Add provider", exact: true })).toHaveCount(0)
    await expect(page.getByRole("tab", { name: /Providers/ })).toHaveCount(0)
    expect(providerReads).toBe(0)
    if (role === "MANAGER") await expect(page.getByRole("button", { name: "Add secret", exact: true }).first()).toBeVisible()
  })
}

// Isolated UI acceptance: no real token, account creation or provider traffic.
// The real page and wizard render against an empty workspace API fixture.
for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
  test(`provider detail and metadata-only editing at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const credential = { id: "fixture-provider", name: "Example ChatGPT", description: "Test account — no live credentials", type: "AI_CLI_TOKEN", provider: "OPENAI", scope: "WORKSPACE", status: "ACTIVE", crew_id: null, crew_ids: [], tags: [], created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", last_used_ips: [], agent_names: [], _count_agent_credentials: 0, security_level: 1,
      login: { mode: "subscription", provider: "OPENAI", plan: "plus", plan_label: "ChatGPT Plus", owner_user_id: "ui-test", owner_email: "ui@example.test", expires_at: "2026-09-08T16:47:22Z", refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null }, quota: null, delivery: { kind: "file", target: ".codex/auth.json" }, pays_for: { agents: 0, crews: 0 } } }
    let patch: Record<string, unknown> | undefined
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "ui-test", email: "ui@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ui-workspace", name: "Browser fixture", slug: "browser-fixture", currentUserRole: "OWNER" }]
      else if (path === "/api/v1/credentials") body = [credential]
      else if (path === "/api/v1/credentials/fixture-provider" && route.request().method() === "PATCH") { patch = route.request().postDataJSON(); Object.assign(credential, patch); body = credential }
      else if (path.includes("/settings") || path.includes("/config") || path.includes("/health")) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials?tab=providers")
    if (viewport.width > 640) await page.getByRole("button", { name: "Sidebar: hover", exact: true }).click()
    await page.getByText("Example ChatGPT", { exact: true }).first().click()
    await expect(page.getByText("Connection health", { exact: true })).toBeVisible()
    await expect(page.getByRole("button", { name: /Rotate/ })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Re-login", exact: true })).toBeVisible()
    await expect(page.getByText("Delivered to the agent as", { exact: true })).not.toBeVisible()
    await page.getByText("Connection details", { exact: true }).click()
    await expect(page.getByText("Delivered to the agent as", { exact: true })).toBeVisible()
    await page.getByText("Connection details", { exact: true }).click()
    await page.waitForTimeout(600)
    await page.screenshot({ path: `/tmp/provider-detail-${viewport.width}.png` })
    await page.getByRole("button", { name: "Edit", exact: true }).first().click()
    await expect(page.getByRole("dialog", { name: /Edit provider/ })).toBeVisible()
    await expect(page.getByRole("checkbox", { name: "Replace the stored secret" })).toHaveCount(0)
    await page.getByLabel("Name", { exact: true }).fill("Work ChatGPT")
    await page.getByRole("button", { name: "Save changes", exact: true }).click()
    await expect.poll(() => patch?.name).toBe("Work ChatGPT")
    await expect(page.getByRole("heading", { name: "Work ChatGPT", exact: true })).toBeVisible()
    expect(patch).not.toHaveProperty("value")
    expect(patch).not.toHaveProperty("provider")
    expect(patch).not.toHaveProperty("token_expires_at")
  })
  test(`provider-first onboarding and zero-account filters at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "ui-test", email: "ui@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ui-workspace", name: "Browser fixture", slug: "browser-fixture", currentUserRole: "OWNER" }]
      else if (path === "/api/v1/provider-logins/device") body = { device_id: "fixture-device", user_code: "TEST-CODE", verification_url: "https://auth.openai.com/device", interval_s: 30, expires_at: new Date(Date.now() + 600_000).toISOString() }
      else if (path === "/api/v1/provider-logins/device/fixture-device") body = { status: "pending" }
      else if (path.includes("/settings") || path.includes("/config") || path.includes("/health")) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto(viewport.width > 640 ? "/credentials" : "/credentials?tab=providers")
    if (viewport.width > 640) await page.getByRole("button", { name: "Sidebar: hover", exact: true }).click()
    await expect(page.getByRole("button", { name: "Connect via OAuth", exact: true })).toHaveCount(0)
    await page.getByRole("button", { name: "Add secret", exact: true }).first().click()
    await expect(page.getByTestId("shape-grid").getByRole("button")).toHaveCount(6)
    await expect(page.getByRole("group", { name: "Choose AI provider" })).toHaveCount(0)
    await page.getByRole("button", { name: "Cancel", exact: true }).click()
    if (viewport.width > 640) {
      // Pin global navigation so its hover overlay cannot cover the vault rail.
      await expect(page.getByText("All providers", { exact: true })).toBeVisible()
      await expect(page.getByLabel("0 connected accounts")).toHaveCount(12)
      await page.getByText("Grok / xAI", { exact: true }).click()
    }
    await page.getByRole("button", { name: "Add provider", exact: true }).first().click()
    await page.getByRole("button", { name: /^ChatGPT \/ OpenAI/ }).click()
    await expect(page.getByLabel("Provider", { exact: true })).toHaveCount(0)
    await expect(page.getByTestId("device-user-code")).toHaveText("TEST-CODE")
    await page.screenshot({ path: `/tmp/provider-guided-chatgpt-${viewport.width}.png` })
    await page.getByRole("button", { name: /Import from Codex CLI/ }).click()
    await expect(page.getByLabel(/Codex login \(auth.json\)/)).toBeVisible()
    await page.getByRole("button", { name: "Back", exact: true }).click()
    await page.getByRole("button", { name: /^Gemini \/ Google/ }).click()
    await expect(page.getByRole("link", { name: "Get API key" })).toHaveAttribute("href", "https://aistudio.google.com/apikey")
    await page.getByRole("button", { name: /^Google account/ }).click()
    await expect(page.getByLabel(/Gemini login/)).toBeVisible()
    await page.getByLabel(/Gemini login/).fill("fixture-only-not-a-token")
    await page.getByRole("button", { name: "Back", exact: true }).click()
    await page.getByRole("button", { name: /^Grok \/ xAI/ }).click()
    await expect(page.getByLabel("API key", { exact: true })).toHaveValue("")
    await expect(page.getByRole("button", { name: /^Subscription/ })).toHaveCount(0)
    await expect(page.getByRole("button", { name: /Sign in with a code/ })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Continue", exact: true })).toBeVisible()
    await page.getByLabel("API key", { exact: true }).fill("fixture-xai-key")
    await expect(page.getByText("Name and labels · Grok / xAI", { exact: true })).toBeVisible()
    await page.screenshot({ path: `/tmp/provider-guided-grok-${viewport.width}.png` })
    await page.getByRole("button", { name: "Continue", exact: true }).click()
    await expect(page.getByText("Account protection", { exact: true })).toBeVisible()
    await expect(page.getByRole("group", { name: "How closely Keeper guards it" })).toHaveCount(0)
    await expect(async () => {
      const box = await page.getByRole("dialog").boundingBox()
      expect(box).not.toBeNull()
      expect(box!.x).toBeGreaterThanOrEqual(-1)
      expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width + 1)
    }).toPass()
  })

  test(`credential metadata editing at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const credential = { id: "fixture-secret", name: "Example certificate", description: "Browser fixture — not a real secret", type: "CERT", provider: "NONE", scope: "WORKSPACE", status: "ACTIVE", crew_id: null, crew_ids: [], tags: [], created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", last_used_ips: [], agent_names: [], _count_agent_credentials: 0, security_level: 3 }
    const fields = [{ key: "ca_pem", value: "old-public-fixture", is_secret: false }, { key: "key_pem", value: null, is_secret: true }]
    let patch: Record<string, unknown> | undefined
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url())
      const path = url.pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "ui-test", email: "ui@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ui-workspace", name: "Browser fixture", slug: "browser-fixture", currentUserRole: "OWNER" }]
      else if (path === "/api/v1/credentials") body = url.searchParams.has("kind") ? [] : [credential]
      else if (path === "/api/v1/credentials/fixture-secret/fields") body = fields
      else if (path === "/api/v1/credentials/fixture-secret/fields/ca_pem" && route.request().method() === "PUT") {
        fields[0].value = route.request().postDataJSON().value
        body = fields[0]
      }
      else if (path === "/api/v1/credentials/fixture-secret" && route.request().method() === "PATCH") {
        patch = route.request().postDataJSON()
        Object.assign(credential, patch)
        body = credential
      }
      else if (path.includes("/settings") || path.includes("/config") || path.includes("/health")) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials")
    if (viewport.width > 640) await page.getByRole("button", { name: "Sidebar: hover", exact: true }).click()
    // Open from Recently used is unavailable for an unused secret; the rail is
    // the canonical list. At mobile width, open it through the explorer toggle.
    if (viewport.width < 640) await page.getByRole("button", { name: "Expand sidebar", exact: true }).click()
    await page.getByText("Example certificate", { exact: true }).first().click()
    await expect(page.getByTestId("connection-verification")).toContainText("Connection not verified")
    await expect(page.getByTestId("credential-access-summary")).toContainText("Your access:")
    await expect(page.getByText("Properties & protection", { exact: true })).toBeVisible()
    await page.waitForTimeout(600)
    await page.screenshot({ path: `/tmp/credential-detail-${viewport.width}.png` })
    await page.getByRole("button", { name: "Edit", exact: true }).first().click()
    await expect(page.getByRole("dialog", { name: "Edit credential" })).toBeVisible()
    await expect(page.getByLabel(/^Replace certificate/)).toHaveCount(0)
    await page.getByLabel("Name", { exact: true }).fill("Renamed certificate")
    await page.getByLabel("Description", { exact: true }).fill("Metadata only")
    await page.getByRole("checkbox", { name: "Replace the stored secret", exact: true }).click()
    await page.getByLabel(/^Replace certificate/).fill("discarded-fixture-only\nsecond-line")
    await page.getByRole("checkbox", { name: "Replace the stored secret", exact: true }).click()
    await expect(page.getByLabel(/^Replace certificate/)).toHaveCount(0)
    await page.getByRole("button", { name: "Edit CA chain (PEM)", exact: true }).click()
    await page.getByLabel("CA chain (PEM)", { exact: true }).fill("new-public-fixture\nsecond-line")
    await page.getByRole("button", { name: "Save CA chain (PEM)", exact: true }).click()
    await expect.poll(() => fields[0].value).toBe("new-public-fixture\nsecond-line")
    await page.getByRole("button", { name: "Edit Private key (PEM)", exact: true }).click()
    await expect(page.getByLabel("Private key (PEM)", { exact: true })).toHaveValue("")
    await page.getByRole("button", { name: "Cancel Private key (PEM)", exact: true }).click()
    const tags = page.getByLabel(/^Tags/)
    await tags.fill("Demo")
    await tags.press("Enter")
    await tags.fill("demo")
    await tags.press(",")
    await expect(page.getByRole("button", { name: "Remove tag demo" })).toHaveCount(1)
    await page.getByRole("button", { name: "Remove tag demo" }).click()
    await tags.fill("production")
    await expect(page.getByRole("button", { name: "Save changes", exact: true })).toBeInViewport()
    await page.screenshot({ path: `/tmp/credentials-edit-${viewport.width}.png` })
    await page.getByRole("button", { name: "Save changes", exact: true }).click()
    await expect.poll(() => patch?.name).toBe("Renamed certificate")
    await expect(page.getByRole("heading", { name: "Renamed certificate", exact: true })).toBeVisible()
    await expect(page.getByText("production", { exact: true })).toBeVisible()
    expect(patch).not.toHaveProperty("value")
    expect(patch?.security_level).toBe(3)
    expect(patch?.tags).toEqual(["production"])
  })
}
