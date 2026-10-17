import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for Integrations (#3395): four spaces in the panel, the
// lists, the object pages at ?id= and every old ?tab=&section= link, measured
// in a real browser against a seeded workspace.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

const panel = (page: Page) => page.locator("[data-slot=secondary-panel][data-panel-id=integrations]")
const h1 = (page: Page) => page.locator("h1:visible").first()
/** The space's one primary action in the top bar. An empty list also has its own CTA (the v67 empty state), so the bar's button is located by its hook. */
const primary = (page: Page) => page.locator("[data-action=space-primary]")

/**
 * A workspace webhook (it opens the same channel page as any channel), made
 * through the API for the test and removed after it, so the page and the
 * MEMBER check do not depend on what the dev data has. A webhook rather than
 * email: dev instances often have no mail transport, and a public https URL
 * is accepted without being called.
 */
async function withChannel(page: Page, run: (id: string, name: string) => Promise<void>) {
  const ws = await (await page.request.get("/api/v1/workspaces")).json()
  const workspaceId = (Array.isArray(ws) ? ws[0] : ws).id as string
  // Unique per test: specs run in parallel workers and may start in the same millisecond.
  const url = `https://example.com/crewship-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
  const res = await page.request.post(`/api/v1/notification-channels?workspace_id=${encodeURIComponent(workspaceId)}`, { data: { type: "webhook", url } })
  expect(res.ok(), await res.text()).toBeTruthy()
  const id = (await res.json()).id as string
  try {
    await run(id, url.replace("https://", ""))
  } finally {
    await page.request.delete(`/api/v1/notification-channels/${encodeURIComponent(id)}?workspace_id=${encodeURIComponent(workspaceId)}`)
  }
}

/** Signs a context in through the credentials endpoint global-setup uses. */
async function signIn(page: Page, email: string, password: string) {
  await page.goto("/login")
  const result = await page.evaluate(
    async ({ email, password }) => {
      const csrf = (await (await fetch("/api/auth/csrf")).json()).csrfToken as string
      const res = await fetch("/api/auth/callback/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password, csrfToken: csrf, redirect: "false" }),
      })
      return res.json()
    },
    { email, password },
  )
  expect(result?.error, "member sign-in").toBeFalsy()
}

for (const [label, width, height] of [["1440", 1440, 1000], ["1024", 1024, 800], ["390", 390, 844]] as const) {
  test.describe(`at ${label}`, () => {
    test.use({ viewport: { width, height } })

    test(`N01 channels at ${label}`, async ({ page }) => {
      await seed(page)
      await page.goto("/integrations?space=channels")
      await expect(h1(page)).toHaveText("Channels")
      // A table on desktop and tablet, rows on a phone; never a sideways scroll.
      // COM-05: below 1280 the panel starts closed; the top bar's panel button opens it.
      if (width >= 768 && width < 1280) await page.getByTestId("secondary-panel-button").click()
      if (width >= 768) await expect(panel(page)).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow).toBeLessThanOrEqual(0)
      await expect(primary(page)).toBeVisible()
      await expect(primary(page)).toHaveAccessibleName("Add channel")
    })

    test(`N09 tools at ${label}`, async ({ page }) => {
      await seed(page)
      await page.goto("/integrations")
      await expect(h1(page)).toHaveText(/Tools|Catalog/)
      await expect(primary(page)).toBeVisible()
      await expect(primary(page)).toHaveAccessibleName("Connect an app")
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow).toBeLessThanOrEqual(0)
    })
  })
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("N05 triggers at 1440", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=triggers")
    await expect(h1(page)).toHaveText("Triggers")
    await expect(primary(page)).toBeVisible()
    await expect(primary(page)).toHaveAccessibleName("New trigger")
    await expect(page.getByText("Kept for replay")).toHaveCount(0)
    await expect(page.getByText("App events")).toHaveCount(0)
  })

  test("panel shows the four spaces and 1–4 switch them", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations")
    const spaces = panel(page).getByRole("tablist", { name: "Spaces" })
    await expect(spaces.getByRole("tab")).toHaveText([/Tools/, /Triggers/, /Channels/, /Webhooks/])
    await page.locator("body").press("3")
    await expect(h1(page)).toHaveText("Channels")
    await page.locator("body").press("4")
    await expect(h1(page)).toHaveText("Webhooks")
    const toolbar = await panel(page).locator(".secondary-panel-toolbar").boundingBox()
    expect(Math.round(toolbar!.height)).toBe(48)
  })

  test("N02 ?id= opens the channel page; reload keeps it", async ({ page }) => {
    await seed(page)
    await withChannel(page, async (id, name) => {
      await page.goto("/integrations?space=webhooks")
      await page.getByRole("row", { name: `Open ${name}` }).click()
      await expect(page).toHaveURL(new RegExp(`[?&]id=${id}`))
      await expect(h1(page)).toHaveText(name)
      await page.reload()
      await expect(h1(page)).toHaveText(name)
      for (const tab of ["Overview", "Events", "Deliveries", "Settings"]) await expect(page.getByRole("tab", { name: tab })).toBeVisible()
      await expect(page.getByRole("button", { name: /Send again/ })).toHaveCount(0)
    })
  })

  test("N01 MEMBER sees workspace channels read-only", async ({ page, browser }) => {
    const email = process.env.E2E_MEMBER_EMAIL
    test.skip(!email, "set E2E_MEMBER_EMAIL / E2E_MEMBER_PASSWORD for a MEMBER account")
    await withChannel(page, async (id, name) => {
      // A clean context: the project's storageState is the admin session.
      const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
      const member = await ctx.newPage()
      try {
        await signIn(member, email!, process.env.E2E_MEMBER_PASSWORD ?? "")
        await member.goto(`/integrations?space=webhooks&id=${id}&pane=settings`)
        await expect(member.locator("h1:visible").first()).toHaveText(name)
        await expect(member.getByRole("switch", { name: "Paused" })).toBeDisabled()
        await expect(member.getByRole("button", { name: "Remove…" })).toHaveCount(0)
        await expect(primary(member)).toHaveAccessibleName("Add webhook")
        await expect(primary(member)).toBeEnabled()
      } finally {
        await ctx.close()
      }
    })
  })

  test("Z12 lands on Channels", async ({ page }) => {
    await seed(page)
    await page.goto("/settings?tab=notifications")
    await expect(page).toHaveURL(/space=channels/)
    await expect(h1(page)).toHaveText("Channels")
  })

  test("Z13 /settings?tab=notification-prefs lands on My notifications", async ({ page }) => {
    await seed(page)
    await page.goto("/settings?tab=notification-prefs")
    await expect(page).toHaveURL(/space=channels/)
    await expect(page).toHaveURL(/view=preferences/)
    await expect(h1(page)).toHaveText("My notifications")
  })

  test("N06 ?tab=incoming&section=routine&target=<id> opens filtered triggers", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=incoming&section=routine&target=nope")
    await expect(page).toHaveURL(/space=triggers/)
    await expect(page).toHaveURL(/starts=routine/)
    await expect(h1(page)).toHaveText("Triggers of routines")
  })

  for (const [section, title] of [
    ["catalog", "Catalog"],
    ["accounts", /Tools|Catalog/],
    ["agents", /Tools|Catalog/],
    ["tools", /Tools|Catalog/],
    ["triggers", /Tools|Catalog/],
    ["mcp", "MCP endpoints"],
    ["crew-tools", "Crew MCP servers"],
  ] as const) {
    test(`legacy tools sections: ${section}`, async ({ page }) => {
      await seed(page)
      await page.goto(`/integrations?tab=tools&section=${section}`)
      await expect(page).toHaveURL(/space=tools/)
      await expect(h1(page)).toHaveText(title)
    })
  }

  test("N12 old ?section=tools lands on the app list", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=tools&section=tools")
    await expect(page).toHaveURL(/space=tools/)
  })

  test("N14 ?section=mcp lands on Tools without error", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=tools&section=mcp")
    await expect(h1(page)).toHaveText("MCP endpoints")
    await expect(page.locator("[data-state=error]")).toHaveCount(0)
  })

  test("N15 ?section=crew-tools&server=<id> keeps the server", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=tools&section=crew-tools&server=srv_none")
    await expect(page).toHaveURL(/server=srv_none/)
    await expect(h1(page)).toHaveText("Crew MCP servers")
  })

  test("N10 ?id= opens the app account", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=tools")
    const first = page.locator("[data-slot=integrations-table] tbody tr").first()
    test.skip((await first.count()) === 0, "no Composio account connected")
    await first.click()
    await expect(page).toHaveURL(/[?&]id=/)
    for (const tab of ["Overview", "Access", "Tools", "Triggers", "Settings"]) await expect(page.getByRole("tab", { name: tab })).toBeVisible()
  })

  test("INT-01 New trigger is three steps; only a webhook starts it", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=triggers")
    await primary(page).click()
    const dialog = page.getByRole("dialog")
    const steps = dialog.getByRole("navigation", { name: "Steps" })
    await expect(steps.getByRole("listitem")).toHaveText(["1Goes to", "2Sender and mapping", "3URL"])
    await expect(dialog.getByRole("group", { name: "Started by" }).getByRole("button")).toHaveText(["A webhook"])
    await expect(dialog.getByText(/An app event|Email in/)).toHaveCount(0)
    await expect(dialog.getByRole("radio")).toHaveCount(3)
    await expect(dialog.getByText("Step 1 of 3")).toBeVisible()
    await expect(dialog.getByRole("button", { name: "Continue" })).toBeDisabled()
    await dialog.getByRole("combobox", { name: "Routine" }).click()
    const option = page.getByRole("option").first()
    test.skip((await option.count()) === 0, "no routine in this workspace")
    await option.click()
    await dialog.getByRole("button", { name: "Continue" }).click()
    // Step 2 for a routine: who sends it, the mapping, the limit. Nothing is created here.
    await expect(dialog.getByRole("radio", { name: "GitHub" })).toBeVisible()
    await expect(dialog.getByRole("radio", { name: "Any system" })).toHaveAttribute("aria-checked", "true")
    await dialog.getByRole("button", { name: "Add input" }).click()
    await dialog.getByLabel("Input name 1").fill("event")
    await dialog.getByLabel("Value of input 1").fill("x")
    await expect(dialog.getByText("event, raw and headers come from the call and cannot be mapped")).toBeVisible()
    await expect(dialog.getByRole("button", { name: "Create" })).toBeDisabled()
    await dialog.getByRole("button", { name: "Back" }).click()
    await expect(dialog.getByText("Step 1 of 3")).toBeVisible()
  })

  test("INT-02 a routine trigger's mapping is edited on the page and saved with the Save bar", async ({ page }) => {
    await seed(page)
    const ws = await (await page.request.get("/api/v1/workspaces")).json()
    const workspaceId = (Array.isArray(ws) ? ws[0] : ws).id as string
    const routines = await (await page.request.get(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines`)).json()
    const list = (Array.isArray(routines) ? routines : (routines.pipelines ?? routines.items ?? [])) as { id: string }[]
    test.skip(list.length === 0, "no routine in this workspace")
    const name = `e2e-mapping-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
    const created = await page.request.post(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-webhooks`, { data: { name, target_pipeline_id: list[0].id, enabled: false } })
    expect(created.ok(), await created.text()).toBeTruthy()
    const id = (await created.json()).id as string
    try {
      await page.goto(`/integrations?space=triggers&id=${encodeURIComponent(id)}&pane=mapping`)
      await expect(h1(page)).toHaveText(name)
      await page.getByRole("button", { name: "Add input" }).click()
      await page.getByLabel("Input name 1").fill("repo")
      await page.getByLabel("Value of input 1").fill("{{ inputs.event.repository.full_name }}")
      const bar = page.getByRole("region", { name: "Unsaved changes" })
      await expect(bar).toContainText("1 unsaved change")
      await bar.getByRole("button", { name: "Save" }).click()
      await expect(bar).toHaveCount(0)
      const list = await (await page.request.get(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-webhooks`)).json()
      const saved = ((Array.isArray(list) ? list : list.webhooks) as { id: string; inputs_template: Record<string, unknown> }[]).find((w) => w.id === id)
      expect(saved?.inputs_template).toEqual({ repo: "{{ inputs.event.repository.full_name }}" })
      await page.reload()
      await expect(page.getByLabel("Input name 1")).toHaveValue("repo")
    } finally {
      await page.request.delete(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-webhooks/${encodeURIComponent(id)}`)
    }
  })

  test("INT-03 app access changes wait on the Save bar", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=tools")
    const first = page.locator("[data-slot=integrations-table] tbody tr").first()
    test.skip((await first.count()) === 0, "no Composio account connected")
    await first.click()
    await page.getByRole("tab", { name: "Access" }).click()
    const group = page.getByRole("group", { name: /access for / }).first()
    test.skip((await group.count()) === 0, "no agents in this workspace")
    const next = group.locator("button[aria-pressed=false]").first()
    await next.click()
    const bar = page.getByRole("region", { name: "Unsaved changes" })
    await expect(bar).toContainText("1 unsaved change")
    // Nothing is written until Save; Discard puts it back.
    await bar.getByRole("button", { name: "Discard" }).click()
    await expect(bar).toHaveCount(0)
    await expect(next).toHaveAttribute("aria-pressed", "false")
  })

  test("INT-04 the Tools panel search finds tools across connected apps", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=tools")
    const first = page.locator("[data-slot=integrations-table] tbody tr").first()
    test.skip((await first.count()) === 0, "no Composio account connected")
    await panel(page).getByLabel("Search apps and tools").fill("list")
    const results = page.getByRole("region", { name: "Tools matching “list”" })
    await expect(results).toBeVisible()
    // Settled: a hit, or the sentence that none matched.
    await expect(results.locator("li button, p").first()).toBeVisible()
    const hit = results.getByRole("button", { name: /^Open .+ in / }).first()
    test.skip((await hit.count()) === 0, "no connected app has a tool matching “list”")
    await hit.click()
    await expect(page).toHaveURL(/pane=tools/)
    await expect(page.locator("tr[aria-current=true]")).toHaveCount(1)
  })

  test("N16 Add channel is three steps; Add webhook starts at step two", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=channels")
    await primary(page).click()
    const dialog = page.getByRole("dialog")
    const steps = dialog.getByRole("navigation", { name: "Add channel steps" })
    await expect(steps.getByRole("button", { name: "Step 1: App" })).toBeVisible()
    await expect(steps.getByRole("button", { name: "Step 2: Where it posts" })).toBeVisible()
    await expect(steps.getByRole("button", { name: "Step 3: What it sends" })).toBeVisible()
    await expect(steps.getByRole("button", { name: "Step 1: App" })).toHaveAttribute("aria-current", "step")
    await dialog.getByRole("radio", { name: /Email/ }).click()
    await dialog.getByRole("button", { name: "Continue" }).click()
    await expect(dialog.getByLabel("Email address")).toBeVisible()
    await page.keyboard.press("Escape")
    await page.goto("/integrations?space=webhooks")
    await expect(primary(page)).toHaveAccessibleName("Add webhook")
    await primary(page).click()
    await expect(page.getByRole("dialog").getByLabel("URL")).toBeVisible()
  })
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 } })
  test("spaces are a segment above the content", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations")
    const seg = page.getByRole("tablist", { name: "Spaces" })
    await seg.getByRole("tab", { name: "Webhooks" }).click()
    await expect(h1(page)).toHaveText("Webhooks")
  })

  test("INT-01 New trigger fits a phone", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=triggers")
    await primary(page).click()
    const dialog = page.getByRole("dialog")
    await expect(dialog.getByRole("navigation", { name: "Steps" })).toContainText("1 / 3")
    await expect(dialog.getByRole("radio")).toHaveCount(3)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})
