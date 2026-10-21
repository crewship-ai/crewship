import { test, expect, type APIRequestContext, type Browser, type BrowserContext, type Page } from "@playwright/test"

// Workspace default look (WSD-01..04, #3746) for a NEW member, end to end.
//
//   1. An admin sets the workspace default (style, accent, theme).
//   2. A member who is added afterwards signs in for the first time and sees
//      the default: nothing was chosen, everything is inherited.
//   3. The member changes one value: that one becomes theirs ("Set by you"),
//      is saved to their account, and the other two stay inherited.
//   4. "Use workspace default" gives the member the default back and removes
//      their saved choice.
//   5. When the admin later changes the default, the member gets the new
//      value for everything they have not chosen and keeps their own choice.
//
// Writes: it changes the default of the workspace the admin signs in to and
// adds one account. Both are undone at the end (the default is put back as it
// was, the member is removed). Set E2E_ALLOW_WRITES=1 on a disposable
// environment to run it; E2E_EMAIL / E2E_PASSWORD (the admin) come from
// global-setup like every other spec.

test.describe.configure({ mode: "serial" })
test.setTimeout(120_000)

const PASSWORD = "e2e-wsd-pass-1234"
const WS_QUERY = (id: string) => `workspace_id=${encodeURIComponent(id)}`

type Look = { style: string; accent: string; theme: "dark" | "light" | "system" }
const FIRST_DEFAULT: Look = { style: "slate", accent: "teal", theme: "dark" }
const SECOND_DEFAULT: Look = { style: "plum", accent: "sky", theme: "light" }

let workspaceId = ""
let originalDefault: unknown = {}
let memberId = ""
const email = `e2e-wsd-${Date.now().toString(36)}@example.test`

async function setDefault(request: APIRequestContext, body: object) {
  const res = await request.patch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}?${WS_QUERY(workspaceId)}`, { data: { appearance_default: body } })
  expect(res.status(), await res.text()).toBeLessThan(300)
}

// A fresh browser, signed in as the new member. The sign-in form is used as a
// person would. Only when the web app runs on an origin the backend does not
// accept for sign-in (a branch dev server on another port), E2E_AUTH_BASE_URL
// names the backend's own origin: the same credentials call global-setup makes
// is sent from there and the cookies are carried over, and the page's
// API writes carry that origin as well.
async function signInAsMember(browser: Browser): Promise<{ context: BrowserContext; page: Page }> {
  const authBase = process.env.E2E_AUTH_BASE_URL
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] }, viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  if (authBase) {
    // The backend refuses writes from any other origin, so the page's own
    // requests carry the backend's origin too.
    await context.route("**/api/**", (route) => {
      const headers = route.request().headers()
      if (headers["origin"]) headers["origin"] = authBase
      if (headers["referer"]) headers["referer"] = `${authBase}/`
      if (route.request().method() === "GET") return route.continue()
      return route.fetch({ headers }).then((response) => route.fulfill({ response }))
    })
    await page.goto(`${authBase}/login`)
    await page.waitForLoadState("networkidle")
    const result = await page.evaluate(async ({ email, password }) => {
      const csrf = (await (await fetch("/api/auth/csrf")).json()).csrfToken as string
      const res = await fetch("/api/auth/callback/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password, csrfToken: csrf, redirect: "false" }),
      })
      return res.json()
    }, { email, password: PASSWORD })
    expect(result?.error, "member sign-in").toBeFalsy()
    return { context, page }
  }
  await page.goto("/login")
  await page.getByLabel("Email").fill(email)
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 30_000 })
  return { context, page }
}

const html = (page: Page, attr: string) => page.evaluate((a) => document.documentElement.getAttribute(a), attr)
const isDark = (page: Page) => page.evaluate(() => document.documentElement.classList.contains("dark"))
async function savedAppearance(context: BrowserContext): Promise<{ appearance: unknown; style: unknown }> {
  const res = await context.request.get("/api/v1/me/preferences")
  expect(res.ok()).toBe(true)
  const prefs = (await res.json()) as Record<string, unknown>
  return { appearance: prefs["appearance"], style: prefs["appearance.style"] }
}
const parse = (v: unknown): Record<string, unknown> => {
  if (typeof v === "string") { try { return JSON.parse(v) } catch { return {} } }
  return v && typeof v === "object" ? (v as Record<string, unknown>) : {}
}

async function expectChips(page: Page, want: Record<"style" | "theme" | "accent", "Workspace default" | "Set by you">) {
  for (const field of ["style", "theme", "accent"] as const) {
    await expect(page.getByTestId(`inherit-${field}`), field).toHaveText(want[field])
  }
}
async function expectLook(page: Page, look: Look) {
  await expect.poll(() => html(page, "data-style"), "style").toBe(look.style)
  await expect.poll(() => html(page, "data-accent"), "accent").toBe(look.accent)
  await expect.poll(() => isDark(page), "theme").toBe(look.theme === "dark")
}

test.describe("Workspace default look for a new member", () => {
  test.skip(!process.env.E2E_ALLOW_WRITES, "changes the workspace default and adds an account; set E2E_ALLOW_WRITES=1 on a disposable environment")

  test.beforeAll(async ({ playwright, baseURL }) => {
    if (!process.env.E2E_ALLOW_WRITES) return
    const { storageFilePath } = await import("./global-setup")
    const request = await playwright.request.newContext({ baseURL, storageState: storageFilePath() })
    try {
      const list = await (await request.get("/api/v1/workspaces")).json()
      workspaceId = (Array.isArray(list) ? list[0] : list)?.id
      expect(workspaceId, "the admin's workspace").toBeTruthy()
      const ws = await (await request.get(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}?${WS_QUERY(workspaceId)}`)).json()
      originalDefault = ws.appearance_default ?? {}

      await setDefault(request, FIRST_DEFAULT)

      // The member is added AFTER the default exists, the way the people page does it.
      const provisioned = await request.post(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/provision?${WS_QUERY(workspaceId)}`, {
        data: { email, role: "MEMBER", full_name: "E2E Look Member", create_only: true },
      })
      expect(provisioned.status(), await provisioned.text()).toBeLessThan(300)
      const body = await provisioned.json()
      const token = new URL(body.setup_url).searchParams.get("token")
      expect(token, "setup link token").toBeTruthy()
      const reset = await request.post("/api/v1/auth/reset", { data: { token, new_password: PASSWORD } })
      expect(reset.status(), await reset.text()).toBeLessThan(300)
    } finally {
      await request.dispose()
    }
  })

  test.afterAll(async ({ playwright, baseURL }) => {
    if (!process.env.E2E_ALLOW_WRITES || !workspaceId) return
    const { storageFilePath } = await import("./global-setup")
    const request = await playwright.request.newContext({ baseURL, storageState: storageFilePath() })
    try {
      await setDefault(request, originalDefault && typeof originalDefault === "object" ? originalDefault : {})
      const members = await (await request.get(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members?${WS_QUERY(workspaceId)}`)).json()
      memberId = (Array.isArray(members) ? members : []).find((m: { user?: { email?: string } }) => m.user?.email === email)?.id ?? ""
      if (memberId) await request.delete(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(memberId)}?${WS_QUERY(workspaceId)}`)
    } finally {
      await request.dispose()
    }
  })

  test("WSD-01..04: inherit, choose one value, go back, follow a later change", async ({ browser, request }) => {
    const { context, page } = await signInAsMember(browser)
    try {
      await test.step("first sign-in: the whole workspace default applies", async () => {
        await page.goto("/settings?tab=appearance")
        await expect(page.getByRole("radiogroup", { name: "Style" })).toBeVisible()
        await expectLook(page, FIRST_DEFAULT)
        await expectChips(page, { style: "Workspace default", theme: "Workspace default", accent: "Workspace default" })
        // Nothing is written to the account for values nobody chose.
        await page.waitForTimeout(1500)
        const saved = await savedAppearance(context)
        expect(parse(saved.appearance)).toEqual({})
        expect(parse(saved.style)).toEqual({})
      })

      await test.step("changing one value makes only that one explicit, and saves it", async () => {
        await page.getByRole("radiogroup", { name: "Accent colour" }).getByRole("radio", { name: "Pink" }).click()
        await expectChips(page, { style: "Workspace default", theme: "Workspace default", accent: "Set by you" })
        await expect.poll(() => html(page, "data-accent")).toBe("pink")
        await expect.poll(async () => parse((await savedAppearance(context)).appearance), { timeout: 15_000 }).toEqual({ accent: "pink" })
        // The other two still follow the default.
        await expect.poll(() => html(page, "data-style")).toBe("slate")
        expect(await isDark(page)).toBe(true)
        // Undo is on offer for the personal change.
        await expect(page.getByRole("button", { name: "Undo" })).toBeVisible()
      })

      await test.step("it survives a reload and is the same on a second visit", async () => {
        await page.reload()
        await expect(page.getByRole("radiogroup", { name: "Style" })).toBeVisible()
        await expectLook(page, { ...FIRST_DEFAULT, accent: "pink" })
        await expectChips(page, { style: "Workspace default", theme: "Workspace default", accent: "Set by you" })
      })

      await test.step("Use workspace default returns the member to the default", async () => {
        await page.getByRole("button", { name: "Use workspace default" }).click()
        await expectLook(page, FIRST_DEFAULT)
        await expectChips(page, { style: "Workspace default", theme: "Workspace default", accent: "Workspace default" })
        await expect.poll(async () => parse((await savedAppearance(context)).appearance), { timeout: 15_000 }).toEqual({})
        await expect(page.getByRole("button", { name: "Use workspace default" })).toBeDisabled()
      })

      await test.step("a later change of the default reaches inherited values only", async () => {
        await page.getByRole("radiogroup", { name: "Accent colour" }).getByRole("radio", { name: "Pink" }).click()
        await expect.poll(async () => parse((await savedAppearance(context)).appearance), { timeout: 15_000 }).toEqual({ accent: "pink" })

        await setDefault(request, SECOND_DEFAULT)
        await page.reload()
        await expect(page.getByRole("radiogroup", { name: "Style" })).toBeVisible()
        // Style and theme are inherited, so they follow; the accent is the member's own.
        await expectLook(page, { ...SECOND_DEFAULT, accent: "pink" })
        await expectChips(page, { style: "Workspace default", theme: "Workspace default", accent: "Set by you" })

        // And going back now lands on the NEW default.
        await page.getByRole("button", { name: "Use workspace default" }).click()
        await expectLook(page, SECOND_DEFAULT)
      })

      await test.step("a second device (fresh browser) gets the same result from the account", async () => {
        const other = await signInAsMember(browser)
        try {
          await other.page.goto("/settings?tab=appearance")
          await expect(other.page.getByRole("radiogroup", { name: "Style" })).toBeVisible()
          await expectLook(other.page, SECOND_DEFAULT)
          await expectChips(other.page, { style: "Workspace default", theme: "Workspace default", accent: "Workspace default" })
        } finally {
          await other.context.close()
        }
      })
    } finally {
      await context.close()
    }
  })
})
