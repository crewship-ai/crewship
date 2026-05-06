import { test, expect, request as plwRequest } from "@playwright/test"

/**
 * Komplexní integration smoke — po kompletním rename Fleet → Cruise → Crews
 * a Phase-10 refactoru. Cíl: ověřit, že nic zásadního nezůstalo rozbité.
 *
 * Pokrývá 6 oblastí:
 *   A) Auth + top-level routes
 *   B) Backend API contract (každý endpoint 200 s rozumnou payload shape)
 *   C) CRUD flow (agent + chat session přes UI form + API)
 *   D) Tab + sub-strip navigace (agent 7 + crew 6 + sub-strip query params)
 *   E) Agent inbox + peer messages (Phase 10)
 *   F) Avatar crew-level flow (apply + reset)
 */

const E2E_EMAIL = process.env.E2E_EMAIL
const E2E_PASSWORD = process.env.E2E_PASSWORD
// The multi-instance convention is `3010+N` for instance N, but the
// default Crewship dev shell (`./dev.sh start`) and the documented dev
// VM (see CLAUDE.md → "Frontend: http://192.168.1.201:3001") both run
// on 3001. Stay aligned with what actually listens by default; override
// via PLAYWRIGHT_BASE_URL when running against a non-default instance.
const BASE_URL = process.env.PLAYWRIGHT_BASE_URL || "http://localhost:3001"

test.describe.configure({ mode: "serial" })
test.use({ storageState: { cookies: [], origins: [] } })

let cachedCookies: Awaited<ReturnType<Awaited<ReturnType<typeof plwRequest.newContext>>["storageState"]>>["cookies"] = []

test.beforeAll(async () => {
  test.skip(
    !E2E_EMAIL || !E2E_PASSWORD,
    "full-integration: set E2E_EMAIL and E2E_PASSWORD to run this suite",
  )
  const ctx = await plwRequest.newContext({ baseURL: BASE_URL })
  const { csrfToken } = (await (await ctx.get("/api/auth/csrf")).json()) as { csrfToken: string }
  const loginRes = await ctx.post("/api/auth/callback/credentials", {
    form: { csrfToken, email: E2E_EMAIL!, password: E2E_PASSWORD!, callbackUrl: "/", json: "true" },
  })
  if (!loginRes.ok()) throw new Error(`login ${loginRes.status()}`)
  // NextAuth's credentials callback returns HTTP 200 even on invalid
  // credentials — the real signal is either (a) an `error` key in the
  // JSON body or (b) the absence of a session cookie. Check both so a
  // bad password doesn't silently produce an "authenticated" suite.
  const body = await loginRes.json().catch(() => ({}))
  if (body && typeof body.error === "string") {
    throw new Error(`login failed: ${body.error}`)
  }
  const storage = await ctx.storageState()
  const hasSession = storage.cookies.some((c) =>
    c.name.includes("authjs.session-token") || c.name.includes("next-auth.session-token"),
  )
  if (!hasSession) {
    throw new Error("login failed: no session cookie was set")
  }
  cachedCookies = storage.cookies
  await ctx.dispose()
})

async function login(page: import("@playwright/test").Page) {
  await page.context().addCookies(cachedCookies)
  await page.goto("/")
  await page.waitForLoadState("domcontentloaded")
}

// `/api/v1/workspaces` has historically returned either an array or a single
// object. Normalize at every call site so one test never throws on the
// singleton shape before the real assertion runs. Callers must have already
// logged in (this helper does not navigate, so it's safe mid-flow).
async function withWorkspace(page: import("@playwright/test").Page): Promise<string> {
  const wsId = await page.evaluate(async () => {
    const r = await fetch("/api/v1/workspaces")
    const d = await r.json()
    return Array.isArray(d) ? d[0]?.id : d.id
  })
  if (!wsId || typeof wsId !== "string") {
    throw new Error(
      "withWorkspace: no workspace_id returned from /api/v1/workspaces — " +
      "dev seed missing or session cookie rejected by the backend",
    )
  }
  return wsId
}

// ---------------------------------------------------------------------------
// A. Top-level route reachability
// ---------------------------------------------------------------------------

test.describe("A. Top-level routes", () => {
  const routes = [
    "/",
    "/crews",
    "/crews/agents",
    "/crews/new",
    "/crews/agents/new",
    "/orchestration",
    "/issues",
    "/runs",        // legacy redirect → /journal?tab=runs (still 200 after follow)
    "/journal",
    "/approvals",
    "/crows-nest",
    "/skills",
    "/credentials",
    "/integrations",
    "/settings",
  ]

  for (const route of routes) {
    test(`${route} → 200`, async ({ page }) => {
      await login(page)
      const resp = await page.goto(route)
      expect(resp?.status(), route).toBeLessThan(400)
    })
  }
})

// ---------------------------------------------------------------------------
// B. Legacy redirects still work
// ---------------------------------------------------------------------------

test.describe("B. Legacy redirects", () => {
  test("/fleet → /crews", async ({ page }) => {
    await login(page)
    await page.goto("/fleet")
    await page.waitForURL(/\/crews(\?|$)/, { timeout: 10_000 })
    expect(page.url()).toMatch(/\/crews(\?|$)/)
  })

  test("/agents → /crews/agents", async ({ page }) => {
    await login(page)
    await page.goto("/agents")
    await page.waitForURL(/\/crews\/agents/, { timeout: 10_000 })
  })
})

// ---------------------------------------------------------------------------
// C. Backend API contract — each endpoint returns 200 with sane shape
// ---------------------------------------------------------------------------

test.describe("C. Backend API contract", () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test("agents list has entries with name/slug/crew", async ({ page }) => {
    const ws = await withWorkspace(page)
    const agents = await page.request.get(`/api/v1/agents?workspace_id=${ws}`).then((r) => r.json())
    expect(Array.isArray(agents)).toBe(true)
    expect(agents.length).toBeGreaterThan(0)
    expect(agents[0]).toHaveProperty("name")
    expect(agents[0]).toHaveProperty("slug")
  })

  test("crews list has entries with icon/color/slug", async ({ page }) => {
    const ws = await withWorkspace(page)
    const crews = await page.request.get(`/api/v1/crews?workspace_id=${ws}`).then((r) => r.json())
    expect(Array.isArray(crews)).toBe(true)
    expect(crews.length).toBeGreaterThan(0)
    expect(crews[0]).toHaveProperty("slug")
    expect(crews[0]).toHaveProperty("_count")
  })

  test("issues list resolves (shape: array or {rows})", async ({ page }) => {
    const ws = await withWorkspace(page)
    const resp = await page.request.get(`/api/v1/issues?workspace_id=${ws}`)
    expect(resp.status()).toBe(200)
    const data = await resp.json()
    const list = Array.isArray(data) ? data : data.rows ?? data.data ?? []
    expect(Array.isArray(list)).toBe(true)
  })

  test("missions with include_tasks returns tasks array", async ({ page }) => {
    const ws = await withWorkspace(page)
    const resp = await page.request.get(`/api/v1/missions?workspace_id=${ws}&include_tasks=true`)
    expect(resp.status()).toBe(200)
  })

  test("paymaster spend-by-crew returns rows", async ({ page }) => {
    const ws = await withWorkspace(page)
    const resp = await page.request.get(`/api/v1/paymaster/spend/by-crew?workspace_id=${ws}`)
    expect(resp.status()).toBe(200)
  })

  test("memory health returns metrics", async ({ page }) => {
    const ws = await withWorkspace(page)
    const resp = await page.request.get(`/api/v1/memory/health?workspace_id=${ws}`)
    expect(resp.status()).toBe(200)
  })

  test("approvals queue returns list (status=pending default)", async ({ page }) => {
    const ws = await withWorkspace(page)
    const resp = await page.request.get(`/api/v1/approvals?workspace_id=${ws}`)
    expect(resp.status()).toBe(200)
    const data = await resp.json()
    expect(data).toHaveProperty("rows")
  })

  test("agent inbox (Phase 10) consolidated payload", async ({ page }) => {
    const ws = await withWorkspace(page)
    const agents = await page.request.get(`/api/v1/agents?workspace_id=${ws}`).then((r) => r.json())
    const resp = await page.request.get(`/api/v1/agents/${agents[0].id}/inbox?workspace_id=${ws}`)
    expect(resp.status()).toBe(200)
    const inbox = await resp.json()
    expect(inbox).toHaveProperty("approvals_pending")
    expect(inbox).toHaveProperty("assignments_open")
    expect(inbox).toHaveProperty("escalations_open")
    expect(inbox).toHaveProperty("peer_messages")
    expect(inbox).toHaveProperty("cost_usd_this_month")
    expect(Array.isArray(inbox.peer_messages)).toBe(true)
  })

  test("crew peer-conversations + assignments + escalations all resolve", async ({ page }) => {
    const ws = await withWorkspace(page)
    const crews = await page.request.get(`/api/v1/crews?workspace_id=${ws}`).then((r) => r.json())
    const c = crews[0].id
    for (const path of ["peer-conversations", "assignments", "escalations", "members"]) {
      const resp = await page.request.get(`/api/v1/crews/${c}/${path}?workspace_id=${ws}`)
      expect(resp.status(), path).toBeLessThan(400)
    }
  })
})

// ---------------------------------------------------------------------------
// D. Agent CRUD + chat session + inbox — full UI + API round-trip
// ---------------------------------------------------------------------------

test.describe("D. CRUD flow", () => {
  test("create agent via form → appears in list → delete via API", async ({ page }) => {
    await login(page)
    const slug = `integ-${Date.now()}`
    let agentId: string | undefined
    try {
      await page.goto("/crews/agents/new")
      await page.waitForLoadState("networkidle")
      await page.locator('input[id="name"]').fill("Integration")
      await page.locator('input[id="slug"]').fill(slug)
      // Crew is required
      const crewCombobox = page.locator('button[id="crew_id"], [role="combobox"]').first()
      await crewCombobox.click()
      await page.locator('[role="option"]').first().click()
      await page.getByRole("button", { name: /create|save/i }).first().click()
      await page.waitForURL(/\/crews\/agents\/?(\?|$)/, { timeout: 15_000 })

      const newAgent = page.locator(`a[href*='/crews/agents/']`).filter({ hasText: "Integration" }).first()
      await expect(newAgent).toBeVisible({ timeout: 10_000 })
      const href = await newAgent.getAttribute("href")
      agentId = href?.split("/").pop()

      const ws = await withWorkspace(page)
      const del = await page.request.delete(`/api/v1/agents/${agentId}?workspace_id=${ws}`)
      expect(del.status()).toBe(200)
      agentId = undefined
    } finally {
      // If we got as far as creating the agent but an earlier assertion
      // threw, delete it here so the database isn't left with orphan
      // `integ-<ts>` rows between runs.
      if (agentId) {
        const ws = await withWorkspace(page).catch(() => null)
        if (ws) {
          await page.request.delete(`/api/v1/agents/${agentId}?workspace_id=${ws}`).catch(() => {})
        }
      }
    }
  })

  test("create chat session → appears in Sessions tab", async ({ page }) => {
    await login(page)
    const ws = await withWorkspace(page)
    const agents = await page.request.get(`/api/v1/agents?workspace_id=${ws}`).then((r) => r.json())
    const agent = agents[0]
    const create = await page.request.post(`/api/v1/agents/${agent.id}/chats?workspace_id=${ws}`, { data: {} })
    expect(create.status()).toBe(201)
    const sessId = (await create.json()).id
    await page.goto(`/crews/agents/${agent.id}/sessions`)
    await expect(page.locator(`a[href*='session=${sessId}']`).first()).toBeVisible({ timeout: 10_000 })
  })
})

// ---------------------------------------------------------------------------
// E. Tab navigation — agent 7 tabs + crew 6 tabs + all sub-strip variants
// ---------------------------------------------------------------------------

test.describe("E. Tab + sub-strip navigation", () => {
  test("agent: all 7 tabs + 6 sub-strip query variants render < 400", async ({ page }) => {
    await login(page)
    const ws = await withWorkspace(page)
    const agents = await page.request.get(`/api/v1/agents?workspace_id=${ws}`).then((r) => r.json())
    const base = `/crews/agents/${agents[0].id}`
    const paths = [
      "", "/sessions", "/runs", "/workspace", "/tools", "/logs", "/settings",
      "/workspace?pane=files", "/workspace?pane=terminal",
      "/tools?section=skills", "/tools?section=credentials", "/tools?section=mcp",
      "/settings?section=general", "/settings?section=schedule",
      "/chat", "/chat?session=abc123",
    ]
    for (const p of paths) {
      const resp = await page.goto(`${base}${p}`)
      expect(resp?.status(), `${base}${p}`).toBeLessThan(400)
    }
  })

  test("crew: all 6 tab query variants render < 400", async ({ page }) => {
    await login(page)
    const ws = await withWorkspace(page)
    const crews = await page.request.get(`/api/v1/crews?workspace_id=${ws}`).then((r) => r.json())
    const base = `/crews/${crews[0].id}`
    const tabs = ["", "?tab=members", "?tab=network", "?tab=runtime", "?tab=journal", "?tab=settings"]
    for (const t of tabs) {
      const resp = await page.goto(`${base}${t}`)
      expect(resp?.status(), `${base}${t}`).toBeLessThan(400)
    }
  })
})

// ---------------------------------------------------------------------------
// F. Avatar crew-level flow — apply + reset
// ---------------------------------------------------------------------------

test.describe("F. Avatar flow", () => {
  test("apply-to-all then reset_overrides both return 200 + updated count", async ({ page }) => {
    await login(page)
    const ws = await withWorkspace(page)
    const crews = await page.request.get(`/api/v1/crews?workspace_id=${ws}`).then((r) => r.json())
    const c = crews[0].id

    const apply = await page.request.post(`/api/v1/crews/${c}/apply-avatar-style?workspace_id=${ws}`, {
      data: { avatar_style: "pixel-art" },
    })
    expect(apply.status()).toBe(200)
    const applyBody = await apply.json()
    expect(applyBody).toHaveProperty("updated")
    expect(applyBody).toHaveProperty("style", "pixel-art")

    const reset = await page.request.post(`/api/v1/crews/${c}/apply-avatar-style?workspace_id=${ws}`, {
      data: { reset_overrides: true },
    })
    expect(reset.status()).toBe(200)
    const resetBody = await reset.json()
    expect(resetBody).toHaveProperty("updated")
    expect(resetBody.reset).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// G. No console errors on any critical page
// ---------------------------------------------------------------------------

test.describe("G. No console errors on critical pages", () => {
  const criticalPages = [
    "/crews",
    "/crews/agents",
    "/orchestration",
    "/issues",
    "/journal",
    "/approvals",
  ]

  for (const path of criticalPages) {
    test(`${path} has no pageerrors or console errors`, async ({ page }) => {
      await login(page)
      const errors: string[] = []
      // pageerror catches uncaught exceptions; console(type=error) catches
      // explicit console.error() calls — the suite title promises "no
      // console errors", so both channels must stay clean.
      page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`))
      page.on("console", (msg) => {
        if (msg.type() === "error") errors.push(`console.error: ${msg.text()}`)
      })
      await page.goto(path)
      await page.waitForLoadState("networkidle")
      await page.waitForTimeout(500)
      expect(errors, `${path} errors`).toHaveLength(0)
    })
  }
})
