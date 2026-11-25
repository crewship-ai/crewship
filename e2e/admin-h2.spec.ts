import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for Admin (#3404): the console frame (panel, section
// heads, phone list → section → back), Overview at three widths, the
// destructive confirmations on Runtime, the retention scope in the URL, the
// no-access page for someone who is not an instance administrator, and the
// old ?tab= links.
//
// The console needs an instance administrator. Tests that read the console's
// own data skip when the signed-in E2E user is not one (the dev slots' seeded
// admin-baseline is a workspace ADMIN); run them with an instance admin in
// E2E_EMAIL / E2E_PASSWORD. The no-access and moved-link tests run as anyone:
// the session the browser sees is rewritten, nothing is sent to the server.
// Nothing here is saved: confirmations are cancelled.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0", "crewship-text-size": "small" }

async function seed(page: Page, entries: Record<string, string>) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, entries)
}

/** Whether the signed-in E2E user is an instance administrator. */
async function isInstanceAdmin(page: Page): Promise<boolean> {
  const res = await page.request.get("/api/auth/session")
  if (!res.ok()) return false
  const body = (await res.json().catch(() => null)) as { user?: { is_instance_admin?: boolean } } | null
  return body?.user?.is_instance_admin === true
}

/** Shows the session to the browser with is_instance_admin set as given. */
async function sessionAs(page: Page, instanceAdmin: boolean) {
  await page.route("**/api/auth/session", async (route) => {
    const res = await route.fetch()
    const body = (await res.json().catch(() => null)) as { user?: Record<string, unknown> } | null
    if (body?.user) body.user.is_instance_admin = instanceAdmin
    await route.fulfill({ response: res, json: body })
  })
}

test.describe("Admin console", () => {
  for (const width of [1440, 1024]) {
    test(`M01 overview at ${width}`, async ({ page }) => {
      test.skip(!(await isInstanceAdmin(page)), "needs an instance administrator in E2E_EMAIL")
      await page.setViewportSize({ width, height: 1000 })
      await seed(page, PINNED)
      await page.goto("/admin")
      // v67 (#3605): a list page's panel stays docked on a tablet, no button.
      const panel = page.locator("[data-slot=secondary-panel][data-panel-id=admin]")
      await expect(panel.getByRole("navigation", { name: "Admin sections" })).toBeVisible()
      await expect(panel.locator("[data-slot=admin-panel-head]")).toContainText("Instance admin")
      const region = page.getByRole("region", { name: "Admin Overview" })
      await expect(region.getByRole("region", { name: "Instance status" })).toBeVisible()
      await expect(region.getByRole("list", { name: "Health checks" }).locator("[data-check]")).toHaveCount(6)
      await expect(region.getByRole("region", { name: "Capacity" })).toBeVisible()
      for (const card of ["Platform", "Integrity", "License"]) await expect(region.getByRole("region", { name: card })).toBeVisible()
      // Overview is the wide dashboard: no section head of its own.
      await expect(region.locator("[data-slot=settings-page-head]")).toHaveCount(0)
      // Cards in a 10 px radius, no shadow.
      await expect(region.getByRole("region", { name: "Platform" })).toHaveCSS("border-top-left-radius", "10px")
      // PAR-093: the figures load (no error card); PAR-094/095: one-line headers, compact rows.
      await expect(region.getByText("Couldn’t load the instance figures")).toHaveCount(0)
      for (const card of ["Platform", "Integrity", "License"]) {
        const c = region.getByRole("region", { name: card })
        expect(await c.locator("[data-slot=settings-card-header]").evaluate((el) => el.getBoundingClientRect().height)).toBeLessThanOrEqual(56)
        const row = c.locator("[data-slot=admin-row]").first()
        await expect(row).toHaveCSS("flex-direction", "row")
        expect(await row.evaluate((el) => el.getBoundingClientRect().height)).toBeLessThanOrEqual(44)
      }
    })
  }

  test("M01 overview at 390: the list first, a section with Back", async ({ page }) => {
    test.skip(!(await isInstanceAdmin(page)), "needs an instance administrator in E2E_EMAIL")
    await page.setViewportSize({ width: 390, height: 844 })
    await seed(page, PINNED)
    await page.goto("/admin")
    const list = page.locator("[data-slot=secondary-panel][data-phone=inline]")
    await expect(list).toBeVisible()
    await list.getByRole("button", { name: "Overview" }).click()
    await expect(page.getByRole("region", { name: "Admin Overview" })).toBeVisible()
    await expect(page).toHaveURL(/tab=overview/)
    await page.getByRole("button", { name: "Back to Admin" }).click()
    await expect(page.locator("[data-slot=secondary-panel][data-phone=inline]")).toBeVisible()
    await expect(page).not.toHaveURL(/tab=/)
    // No horizontal scroll at phone width.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })

  test("M02 Runtime: strip, and Remove every runtime asks for the typed word", async ({ page }) => {
    test.skip(!(await isInstanceAdmin(page)), "needs an instance administrator in E2E_EMAIL")
    await seed(page, PINNED)
    await page.goto("/admin?tab=providers")
    const region = page.getByRole("region", { name: "Admin Runtime" })
    await expect(region.locator("[data-slot=settings-page-head]")).toContainText("What the server runs agents on")
    await expect(region.getByRole("list", { name: "Runtime at a glance" }).locator("[data-strip]")).toHaveCount(4)
    const zone = region.getByRole("region", { name: "Danger zone" })
    const open = zone.getByRole("button", { name: "Remove every runtime…" })
    test.skip(await open.isDisabled(), "no workspace to act on")
    await open.click()
    const dialog = page.getByRole("alertdialog", { name: "Remove every crew runtime?" })
    await expect(dialog.getByRole("button", { name: "Remove every runtime" })).toBeDisabled()
    await dialog.getByLabel(/Type/).fill("remove")
    await expect(dialog.getByRole("button", { name: "Remove every runtime" })).toBeEnabled()
    await dialog.getByRole("button", { name: "Cancel" }).click()
    await expect(dialog).toBeHidden()
  })

  test("M04 Limits: Show All / Changed and the save bar", async ({ page }) => {
    test.skip(!(await isInstanceAdmin(page)), "needs an instance administrator in E2E_EMAIL")
    await seed(page, PINNED)
    await page.goto("/admin?tab=ratelimits")
    const bar = page.locator("[data-slot=limits-toolbar]")
    await expect(bar).toContainText("Instance-wide · applies without a restart")
    const first = page.locator("[data-limiter] input").first()
    const saved = await first.inputValue()
    await first.fill(String(Number(saved) + 1))
    const save = page.getByRole("region", { name: /unsaved changes/i })
    await expect(save).toContainText("1 unsaved change")
    await save.getByRole("button", { name: "Discard" }).click()
    await expect(first).toHaveValue(saved)
  })

  test("M05 ?scope=&ws= survive reload", async ({ page }) => {
    test.skip(!(await isInstanceAdmin(page)), "needs an instance administrator in E2E_EMAIL")
    await seed(page, PINNED)
    await page.goto("/admin?tab=retention")
    const scope = page.locator("[data-slot=retention-scope]")
    await expect(scope.getByRole("button", { name: /^All workspaces · \d+$/ })).toHaveAttribute("aria-pressed", "true")
    await scope.getByRole("button", { name: "Chosen workspaces" }).click()
    const chips = scope.getByRole("group", { name: "Chosen workspaces" }).locator("[data-ws]")
    test.skip((await chips.count()) < 2, "needs two workspaces to choose from")
    // Untick the first: the URL names the rest.
    const dropped = await chips.first().getAttribute("data-ws")
    await chips.first().click()
    await expect(page).toHaveURL(/[?&]ws=/)
    await page.reload()
    const again = page.locator("[data-slot=retention-scope]")
    await expect(again.getByRole("button", { name: "Chosen workspaces" })).toHaveAttribute("aria-pressed", "true")
    await expect(again.locator(`[data-ws="${dropped}"]`)).toHaveAttribute("aria-pressed", "false")
    await expect(page.getByRole("region", { name: "Keep for" })).toBeVisible()
  })

  // ADM-01 (#3493): a dry run that changes nothing is a Close-only dialog.
  test("ADM-01 a no-op dry run shows a Close-only dialog, not a toast", async ({ page }) => {
    test.skip(!(await isInstanceAdmin(page)), "needs an instance administrator in E2E_EMAIL")
    await seed(page, PINNED)
    await page.route("**/api/v1/admin/instance/retention", async (route) => {
      if (route.request().method() !== "PUT") return route.fallback()
      // The server's shape (internal/api/admin_instance_retention.go): per
      // workspace, and no workspace has anything to change.
      await route.fulfill({ json: { dry_run: true, preview_id: "p0", workspaces: [] } })
    })
    await page.goto("/admin?tab=retention")
    const row = page.locator("[data-slot=retention-table] [data-key]").first()
    await expect(row).toBeVisible()
    const group = row.getByRole("group")
    // Pick a window other than the current one so Save appears.
    const options = group.getByRole("button")
    const n = await options.count()
    for (let i = 0; i < n; i++) {
      if ((await options.nth(i).getAttribute("aria-pressed")) !== "true") { await options.nth(i).click(); break }
    }
    await page.getByRole("button", { name: "Save" }).click()
    const dialog = page.getByRole("alertdialog")
    await expect(dialog).toContainText("Nothing to change")
    await expect(dialog.getByRole("button", { name: "Close" })).toBeVisible()
    await expect(dialog.getByRole("button")).toHaveCount(1)
    await dialog.getByRole("button", { name: "Close" }).click()
    await expect(dialog).toBeHidden()
  })

  // ADM-03 (#3493): Data retention and the Backups pages share the scope
  // module instead of importing each other. Each opens from a cold load —
  // whichever is evaluated first — without a page error.
  for (const [path, ready] of [
    ["/admin?tab=retention", "[data-slot=retention-page]"],
    ["/admin/backups", "[data-slot=backups-scope-summary], [data-slot=backups-console], main"],
  ] as const) {
    test(`M05 ${path} opens from a cold load without a page error`, async ({ page }) => {
      test.skip(!(await isInstanceAdmin(page)), "needs an instance administrator in E2E_EMAIL")
      const errors: string[] = []
      page.on("pageerror", (e) => errors.push(e.message))
      await seed(page, PINNED)
      await page.goto(path)
      await expect(page.locator(ready).first()).toBeVisible()
      expect(errors).toEqual([])
    })
  }
})

const applicationsPath = "/api/v1/admin/instance/artifacts/applications"
const sheetMessage = "The document engine could not start. See the server log."
const agentsMessage = "Turned off. Existing documents stay readable; nobody can create or edit them."

async function mockArtifactsConsole(page: Page, instanceAdmin: boolean) {
  const requests: string[] = []
  await seed(page, { ...PINNED, "crewship.workspaceId:fixture-admin": "fixture-ws" })
  await page.route("**/api/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === "/api/auth/session") {
      return route.fulfill({ json: {
        user: { id: "fixture-admin", email: "admin@example.test", is_instance_admin: instanceAdmin },
        expires: "2099-01-01T00:00:00Z",
      } })
    }
    if (path === "/api/v1/workspaces") {
      return route.fulfill({ json: [{ id: "fixture-ws", name: "Fixture", slug: "fixture", currentUserRole: "ADMIN" }] })
    }
    if (path === applicationsPath || path.startsWith(applicationsPath + "/")) {
      requests.push(`${request.method()} ${path}`)
      if (!instanceAdmin) return route.fulfill({ status: 403, json: { error: "Instance admin required" } })
      if (request.method() === "GET" && path === applicationsPath) {
        return route.fulfill({ json: {
          editor: "ok",
          applications: [
            { id: "wordcraft", label: "Word", desired: "enabled", source: "admin", locked: false, state: "ready",
              documents: 2, component: { available: true, development: false } },
            { id: "gridcraft", label: "Sheet", desired: "enabled", source: "server_config", locked: true, state: "unavailable",
              reason: "engine_start_failed", message: sheetMessage, documents: 0,
              component: { available: false, development: false, reason: "engine_start_failed" } },
          ],
          agents: { id: "agents", label: "Agents in Word and Sheet", desired: "disabled", source: "default",
            locked: false, state: "disabled", reason: "app_disabled", message: agentsMessage },
        } })
      }
    }
    return route.fulfill({ status: 404, json: { error: "No fixture for this endpoint" } })
  })
  return requests
}

test.describe("Admin Artifacts", () => {
  test("instance admin opens applications, sees backend reasons and cancels turning off Word", async ({ page }) => {
    const requests = await mockArtifactsConsole(page, true)
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.goto("/admin")
    await page.getByRole("navigation", { name: "Admin sections" }).getByRole("button", { name: "Artifacts", exact: true }).click()
    await expect(page).toHaveURL(/\/admin\/?\?tab=artifacts$/)
    const panel = page.getByRole("region", { name: "Admin Artifacts" })
    for (const name of ["Word", "Sheet", "Agents in Word and Sheet"]) {
      await expect(panel.getByRole("heading", { name, exact: true })).toBeVisible()
    }
    await expect(panel.getByText(sheetMessage, { exact: true })).toBeVisible()
    await expect(panel.getByText(agentsMessage, { exact: true })).toBeVisible()
    await expect(panel.getByText(/Managed by server configuration/)).toBeVisible()
    await expect(panel.getByRole("button", { name: "Turn off Sheet" })).toBeDisabled()
    await panel.getByRole("button", { name: "Turn off Word", exact: true }).click()
    const dialog = page.getByRole("alertdialog", { name: "Turn off Word?" })
    await expect(dialog.getByText("Existing documents will remain readable.")).toBeVisible()
    await dialog.getByLabel(/Allow open editors to finish saving for/).selectOption("300")
    await dialog.getByRole("button", { name: "Cancel" }).click()
    await expect(dialog).toBeHidden()
    await expect(panel.getByRole("button", { name: "Turn off Word", exact: true })).toBeEnabled()
    expect(new Set(requests)).toEqual(new Set([`GET ${applicationsPath}`]))
  })

  test("workspace admin cannot open the Artifacts deep link or trigger its API", async ({ page }) => {
    const requests = await mockArtifactsConsole(page, false)
    await page.goto("/admin?tab=artifacts")
    await expect(page.getByRole("heading", { name: "You can’t open the Admin console" })).toBeVisible()
    await expect(page.getByRole("navigation", { name: "Admin sections" })).toHaveCount(0)
    await expect(page.getByRole("region", { name: "Admin Artifacts" })).toHaveCount(0)
    expect(requests).toEqual([])
  })
})

test.describe("Admin for someone who is not an instance administrator", () => {
  test("M01 non-admin sees no-access, not a redirect", async ({ page }) => {
    await sessionAs(page, false)
    await page.goto("/admin")
    await expect(page.getByRole("heading", { name: "You can’t open the Admin console" })).toBeVisible()
    await expect(page.getByText(/needs an instance administrator/)).toBeVisible()
    await expect(page).toHaveURL(/\/admin(\?|$)/)
    await expect(page.getByRole("navigation", { name: "Admin sections" })).toHaveCount(0)
  })
})

test.describe("Z19…Z24 moved admin tabs", () => {
  const MOVED: [string, RegExp][] = [
    ["/admin?tab=users", /\/admin\/people(\?|$)/],
    ["/admin?tab=workspaces", /\/admin\/people\?view=workspaces/],
    ["/admin?tab=posture", /\/admin\/security(\?|$)/],
    ["/admin?tab=security", /\/admin\/security(\?|$)/],
    ["/admin?tab=reviews", /\/admin\/security\?(.*&)?(section|view)=activity/],
    ["/admin?tab=backups", /\/admin\/backups(\?|$)/],
  ]
  for (const [from, to] of MOVED) {
    test(`${from} lands where it moved`, async ({ page }) => {
      await page.goto(from)
      await expect(page).toHaveURL(to)
    })
  }

  test("Z23 keeps run and section", async ({ page }) => {
    await page.goto("/admin?tab=backups&section=history&run=run-123")
    await expect(page).toHaveURL(/\/admin\/backups\?/)
    const url = new URL(page.url())
    expect(url.searchParams.get("section")).toBe("history")
    expect(url.searchParams.get("run")).toBe("run-123")
  })
})

// Gap UI (ADM-08): the restore checks show "Crew images" when the server can
// read them. Needs an encrypted backup and its key, so it only checks the
// recovery page holds at each width.
for (const width of [390, 1024, 1440]) {
  test(`ADM-08 recovery fits ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.goto("/admin/backups?section=recovery")
    await expect(page.locator("main")).toBeVisible()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
}
