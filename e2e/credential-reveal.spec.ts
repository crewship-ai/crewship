import { test, expect, type Page } from "@playwright/test"

// The rail's mode menu: pin the panel open (the old cycling "Sidebar: hover"
// button landed on pinned from the default hover mode).
async function pinSidebar(page: Page) {
  await page.getByRole("button", { name: /^Sidebar: / }).click()
  await page.getByRole("menuitemradio", { name: /^Expanded/ }).click()
}

test("Static export helper serves root and denies traversal", async ({ request }) => {
  expect((await request.get("/")).status()).toBe(200)
  expect((await request.get("/%2e%2e%2fpackage.json")).status()).toBe(403)
  expect((await request.get("/missing-fixture-2461")).status()).toBe(404)
})

for (const scenario of [
  { role: "OWNER", capability: true, enabled: true, sealed: false, allowed: true },
  { role: "ADMIN", capability: true, enabled: true, sealed: false, allowed: true },
  { role: "MEMBER", capability: true, enabled: true, sealed: false, allowed: false },
  { role: "VIEWER", capability: true, enabled: true, sealed: false, allowed: false },
  { role: "ADMIN", capability: false, enabled: true, sealed: false, allowed: false },
  { role: "OWNER", capability: true, enabled: false, sealed: false, allowed: false },
  { role: "OWNER", capability: true, enabled: true, sealed: true, allowed: false },
]) {
  const { role, capability, enabled, sealed, allowed } = scenario
  test(`${role} reveal capability=${capability} policy=${enabled} sealed=${sealed}`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.clock.install()
    let reveals = 0
    const credential = { id: "demo", name: "Demo file", type: "GENERIC_SECRET", provider: "GCP", status: "ACTIVE", scope: "WORKSPACE", tags: ["demo"], crew_ids: [], agent_names: [], last_used_ips: [], created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", security_level: 2, sensitivity: sealed ? "SEALED" : "STANDARD", _count_agent_credentials: 0 }
    await page.route("**/api/**", async route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "user", email: "demo@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: role, currentUserCapabilities: capability ? ["credentials:reveal"] : [] }]
      else if (path === "/api/v1/credentials") body = [credential]
      else if (path === "/api/v1/credentials/reveal-policy") body = { enabled }
      else if (path === "/api/v1/credentials/demo/sensitivity") body = { sensitivity: credential.sensitivity }
      else if (path === "/api/v1/credentials/demo/access/me") body = {
        credential_id: credential.id,
        actions: { reveal: { state: allowed ? "conditional" : "denied", reason: allowed ? "fresh_login_reason_and_audit_required" : "missing_role_or_capability" } },
      }
      else if (path === "/api/v1/credentials/demo") body = credential
      else if (path === "/api/v1/credentials/demo/reveal") { reveals++; body = { value: "dummy-not-a-real-secret-browser" } }
      else if (/settings|config|health/.test(path)) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto("/credentials")
    await pinSidebar(page)
    await page.getByRole("table", { name: "Secrets" }).getByText("Demo file", { exact: true }).click()
    await expect(page.locator("[data-slot=secret-page]").getByRole("button", { name: "Secrets", exact: true })).toBeVisible()
    const reveal = page.getByRole("button", { name: "Reveal…", exact: true })
    if (!allowed) {
      await expect(reveal).toHaveCount(0)
      expect(reveals).toBe(0)
      return
    }
    await reveal.click()
    await page.getByLabel(/^Reason \(required/).fill("Inspecting the inert demo credential for acceptance")
    await page.getByRole("button", { name: "Reveal for 30 s", exact: true }).click()
    await expect(page.getByTestId("revealed-value")).toHaveText("dummy-not-a-real-secret-browser")
    await page.screenshot({ path: "/tmp/credential-reveal-desktop.png" })
    await page.clock.fastForward(30001)
    await expect(page.getByTestId("revealed-value")).toHaveCount(0)
    await expect(page.getByLabel(/^Reason \(required/)).toHaveValue("")
    expect(reveals).toBe(1)
  })
}

// Same isolated static-export fixture as reveal; this never reads a real value.
test("Native delivery Activity shows its run without claiming authentication", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const credential = { id: "native-delivery", name: "Synthetic SSH fixture", type: "SSH_KEY", provider: "NONE", status: "ACTIVE", scope: "WORKSPACE", tags: [], crew_ids: [], agent_names: [], last_used_at: null, last_used_ips: [], created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", security_level: 3, sensitivity: "STANDARD", _count_agent_credentials: 0 }
  const common = { agent_id: "fixture-agent", actor_kind: "agent", actor_id: "fixture-agent", actor_name: "Fixture agent", ip_address: null, occurred_at: "2026-09-01T00:00:00Z" }
  await page.route("**/api/**", async route => {
    const path = new URL(route.request().url()).pathname
    let body: unknown = []
    if (path === "/api/auth/session") body = { user: { id: "user", email: "fixture@example.test" }, expires: "2099-01-01T00:00:00Z" }
    else if (path === "/api/v1/workspaces") body = [{ id: "ws", name: "Fixture", slug: "fixture", currentUserRole: "OWNER", currentUserCapabilities: [] }]
    else if (path === "/api/v1/credentials") body = [credential]
    else if (path === "/api/v1/credentials/native-delivery") body = credential
    else if (path === "/api/v1/credentials/native-delivery/access/me") body = { actions: { read: { state: "allowed" }, edit: { state: "allowed" } } }
    else if (path === "/api/v1/credentials/native-delivery/audit") body = [
      { ...common, id: "delivered", event_type: "DELIVERED", metadata: { channel: "file", run_id: "msg_123_fixture", authentication: "unknown", value: "never-render-synthetic-secret" } },
      { ...common, id: "fetch", event_type: "USE", metadata: { source: "sidecar_fetch" } },
    ]
    else if (/settings|config|health/.test(path)) body = {}
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
  })
  await page.goto("/credentials?id=native-delivery&tab=activity")
  const entity = page.locator("[data-slot=secret-page]")
  await expect(entity.getByRole("tab", { name: "Activity" })).toHaveAttribute("aria-selected", "true")
  await expect(entity.getByText("Run msg_123_fixture", { exact: true })).toBeVisible()
  await expect(entity.getByText("File delivered · Authentication unverified", { exact: true })).toBeVisible()
  await expect(entity.getByText("Sidecar fetch · Authentication unverified", { exact: true })).toBeVisible()
  await expect(page.getByText("never-render-synthetic-secret")).toHaveCount(0)
})
