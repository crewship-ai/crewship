import { test, expect, type Browser, type Page } from "@playwright/test"

// Harbor 2 acceptance for the Skills area (#3396): the panel, the library
// table, the skill page, Add skill and the old links, against a seeded
// workspace (built-in skills, crews with agents). The repository import is
// answered by a route so the test does not depend on a reachable git host.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

type Json = Record<string, unknown>

async function api(page: Page) {
  await page.goto("/skills")
  return page.evaluate(async () => {
    const ws = await fetch("/api/v1/workspaces").then((r) => r.json())
    const id = (Array.isArray(ws) ? ws : [ws])[0].id as string
    const q = `workspace_id=${encodeURIComponent(id)}`
    const [skills, agents, crews] = await Promise.all([
      fetch(`/api/v1/skills?${q}`).then((r) => r.json()),
      fetch(`/api/v1/agents?${q}`).then((r) => r.json()),
      fetch(`/api/v1/crews?${q}`).then((r) => r.json()),
    ])
    return { id, skills: (skills ?? []) as Json[], agents: (agents ?? []) as Json[], crews: (crews ?? []) as Json[] }
  })
}

const panel = (page: Page) => page.locator("[data-slot=secondary-panel][data-panel-id=skills]")
const rows = (page: Page) => page.locator("[data-slot=skill-row]")

async function loginAs(browser: Browser, email: string): Promise<Page | null> {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto("/login")
  const ok = await page.evaluate(
    async ({ email, password }) => {
      const csrf = (await fetch("/api/auth/csrf").then((r) => r.json())).csrfToken
      const res = await fetch("/api/auth/callback/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password, csrfToken: csrf, redirect: "false" }),
      })
      const body = await res.json().catch(() => ({}))
      return !body?.error
    },
    { email, password: process.env.E2E_RBAC_PASSWORD || "rbac-demo-1234" },
  )
  return ok ? page : null
}

test.describe("Skills desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("L01 Library table is the default, no Cards toggle, panel order Views→Filter", async ({ page }) => {
    await seed(page)
    await page.goto("/skills")
    await expect(page.locator("[data-slot=skills-table]")).toBeVisible()
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Library")
    await expect(page.getByRole("button", { name: "Cards" })).toHaveCount(0)
    // v67 (PAR-073): the facets sit behind the toolbar's Filter button.
    await expect(panel(page).getByText("Domain", { exact: true })).toHaveCount(0)
    await panel(page).getByRole("button", { name: /^Filter/ }).click()
    const text = (await panel(page).innerText()).replace(/\s+/g, " ")
    expect(text.indexOf("Views")).toBeLessThan(text.indexOf("Source"))
    for (const facet of ["Source", "Domain", "Trust", "Status", "Used by crew"]) expect(text).toContain(facet)
    expect(text).not.toContain("Crews & agents")
    const toolbar = await panel(page).locator(".secondary-panel-toolbar").boundingBox()
    expect(Math.round(toolbar!.height)).toBe(48)
  })

  test("L01 Add skill opens the 3-step dialog; Command line opens the CLI dialog", async ({ page }) => {
    await seed(page)
    await page.goto("/skills")
    await page.getByRole("button", { name: "Add skill" }).click()
    const dialog = page.getByRole("dialog", { name: /Add skill/ })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole("navigation", { name: "Add skill steps" })).toContainText("Source")
    await expect(dialog.getByRole("navigation", { name: "Add skill steps" })).toContainText("Who gets it")
    await expect(dialog.getByText(/skills\.sh/)).toHaveCount(0)
    await page.keyboard.press("Escape")
    await page.getByRole("button", { name: "Command line" }).click()
    const cli = page.getByRole("dialog", { name: "Skills from the command line" })
    await expect(cli).toContainText("crewship skill import --repo")
    await expect(cli).not.toContainText("skill add")
    await expect(cli).not.toContainText("update --all")
  })

  test("L01 bulk bar offers Give to crew only", async ({ page }) => {
    await seed(page)
    await page.goto("/skills")
    await rows(page).first().locator("[role=checkbox]").click()
    const bar = page.getByRole("toolbar", { name: "Selected skills" })
    await expect(bar).toContainText("1 selected")
    await expect(bar.getByRole("button", { name: "Give to crew" })).toBeVisible()
    for (const gone of ["Update", "Move to folder", "Archive"]) await expect(bar.getByRole("button", { name: gone })).toHaveCount(0)
    await bar.getByRole("button", { name: "Give to crew" }).click()
    await expect(page.locator("[data-slot=give-to-picker]")).toBeVisible()
  })

  test("L02 ?view=assigned lands on On agents and keeps &q=", async ({ page }) => {
    await seed(page)
    await page.goto("/skills?view=assigned&q=web&trust=verified")
    await expect(page).toHaveURL(/view=used/)
    await expect(page).toHaveURL(/q=web/)
    await expect(page).toHaveURL(/trust=verified/)
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("On agents")
  })

  test("L02 Views advertise their 1–6 keys and the keys switch views", async ({ page }) => {
    await seed(page)
    await page.goto("/skills")
    const views = panel(page).getByRole("navigation", { name: "Skill views" })
    const labels = ["Library", "On agents", "Not on any agent", "Needs attention"]
    for (const [i, label] of labels.entries()) {
      const hint = views.getByText(label, { exact: true })
      await expect(hint).toHaveAttribute("aria-keyshortcuts", String(i + 1))
      await expect(hint).toHaveAttribute("title", `${label} · press ${i + 1}`)
    }
    await page.locator("[data-slot=skills-content]").click({ position: { x: 5, y: 5 } })
    await page.keyboard.press("2")
    await expect(page).toHaveURL(/view=used/)
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("On agents")
    await page.keyboard.press("1")
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Library")
  })

  test("L03 ?view=proposed as MEMBER falls back to Library", async ({ browser }) => {
    const page = await loginAs(browser, process.env.E2E_MEMBER_EMAIL || "member-baseline@crewship.local")
    test.skip(!page, "no RBAC member user in this environment")
    await page!.goto("/skills?view=proposed")
    await expect(page!.getByRole("heading", { level: 1 })).toHaveText("Library")
    await expect(panel(page!).getByText("Proposed by agents")).toHaveCount(0)
    await page!.context().close()
  })

  test("L04 ?agent=<id> shows a removable Agent chip and only its skills", async ({ page }) => {
    await seed(page)
    const { skills, agents } = await api(page)
    const holder = agents.find((a) => skills.some((s) => ((s.installed_on as Json[]) ?? []).some((h) => h.agent_id === a.id)))
    test.skip(!holder, "no agent holds a skill")
    const held = skills.filter((s) => ((s.installed_on as Json[]) ?? []).some((h) => h.agent_id === holder!.id) && s.lifecycle_state !== "archived")
    await page.goto(`/skills?agent=${holder!.id}`)
    const chip = panel(page).getByRole("button", { name: `Remove filter Agent: ${holder!.name}` })
    await expect(chip).toBeVisible()
    await expect(rows(page)).toHaveCount(Math.min(held.length, 50))
    await chip.click()
    await expect(page).not.toHaveURL(/agent=/)
  })

  test("L04 ?crew=&domain= set both facets", async ({ page }) => {
    await seed(page)
    const { crews } = await api(page)
    test.skip(crews.length === 0, "no crews")
    await page.goto(`/skills?crew=${crews[0].id}&domain=CODING`)
    // Closed, the picks show as chips (v67).
    await expect(panel(page).getByRole("button", { name: `Remove filter Used by ${crews[0].name}` })).toBeVisible()
    await expect(panel(page).getByRole("button", { name: "Remove filter Coding" })).toBeVisible()
  })

  test("L05 ?skill=<id> opens Overview; unknown id shows not found", async ({ page }) => {
    await seed(page)
    const { skills } = await api(page)
    await page.goto(`/skills?skill=${skills[0].id}`)
    await expect(page.getByRole("tab", { name: "Overview", selected: true })).toBeVisible()
    await expect(page.getByText("When an agent uses it")).toBeVisible()
    await expect(page.getByRole("button", { name: "Skill options" })).toBeVisible()
    await page.goto("/skills?skill=does-not-exist")
    await expect(page.getByText("This skill doesn’t exist")).toBeVisible()
    await page.getByRole("button", { name: "Back to skills" }).click()
    await expect(page).not.toHaveURL(/skill=/)
  })

  test("L06 ?tab=content opens SKILL.md", async ({ page }) => {
    await seed(page)
    const { skills } = await api(page)
    await page.goto(`/skills?skill=${skills[0].id}&tab=content`)
    await expect(page).toHaveURL(/tab=md/)
    await expect(page.getByRole("tab", { name: "SKILL.md", selected: true })).toBeVisible()
    await page.getByRole("radio", { name: "Source" }).click()
    await expect(page.locator("[data-slot=skill-md-source]")).toContainText("name:")
  })

  test("L08 Activity button opens Activity with the skill filter (or Used by › Recent uses until then)", async ({ page }) => {
    await seed(page)
    const { skills } = await api(page)
    await page.goto(`/skills?skill=${skills[0].id}`)
    await page.getByRole("button", { name: "Skill activity" }).click()
    await expect(page).toHaveURL(/tab=used/)
    await expect(page.getByText("Recent uses")).toBeVisible()
  })

  test("L09 Add skill › Write it opens the editor; Copy into this workspace pre-fills a new slug", async ({ page }) => {
    await seed(page)
    const { skills } = await api(page)
    await page.goto("/skills")
    await page.getByRole("button", { name: "Add skill" }).click()
    await page.getByRole("button", { name: /Write it/ }).click()
    await expect(page.getByRole("dialog", { name: /skill/i })).toBeVisible()
    await page.keyboard.press("Escape")
    await page.goto(`/skills?skill=${skills[0].id}`)
    await page.getByRole("button", { name: "Skill options" }).click()
    await page.getByRole("menuitem", { name: "Copy into this workspace" }).click()
    await expect(page.getByRole("dialog")).toContainText("copy")
  })

  test("L10 Add skill imports two skills from a repo and gives them to Code review", async ({ page }) => {
    await seed(page)
    const { crews, agents } = await api(page)
    const crew = crews.find((c) => agents.some((a) => a.crew_id === c.id))
    test.skip(!crew, "no crew with agents")
    const bodies: Json[] = []
    await page.route(/\/skills\/bulk-import/, async (route) => {
      const body = route.request().postDataJSON() as Json
      bodies.push(body)
      if (body.dry_run)
        return route.fulfill({
          json: {
            source: body.git_url, total_found: 3, total_imported: 0, imported: [],
            skipped: [
              { path: "skills/one/SKILL.md", slug: "h2-one", reason: "dry-run" },
              { path: "skills/two/SKILL.md", slug: "h2-two", reason: "dry-run" },
              { path: "skills/docx/SKILL.md", slug: "docx", reason: 'license "Proprietary" (SPDX ) is not on the import allowlist' },
            ],
          },
        })
      return route.fulfill({ json: { source: body.git_url, total_found: 2, total_imported: 2, imported: [{ skill_id: "fake-1", slug: "h2-one", created: true }, { skill_id: "fake-2", slug: "h2-two", created: true }], skipped: [] } })
    })
    const given: string[] = []
    await page.route(/\/api\/v1\/agents\/[^/]+\/skills\?/, async (route) => {
      if (route.request().method() !== "POST") return route.continue()
      given.push(route.request().url())
      return route.fulfill({ status: 201, json: {} })
    })
    await page.goto("/skills")
    await page.getByRole("button", { name: "Add skill" }).click()
    const dialog = page.getByRole("dialog", { name: /Add skill/ })
    await dialog.getByLabel("From").fill("acme/skills")
    await dialog.getByRole("button", { name: "Look for skills" }).click()
    await expect(dialog.getByRole("checkbox", { name: "docx" })).toBeDisabled()
    await dialog.getByRole("button", { name: "Continue" }).click()
    await dialog.getByRole("checkbox", { name: crew!.name as string }).click()
    await dialog.getByRole("button", { name: "Add 2 skills" }).click()
    await expect(dialog).toBeHidden()
    expect(bodies[0]).toMatchObject({ git_url: "https://github.com/acme/skills", dry_run: true })
    expect(bodies[1]).toMatchObject({ paths: ["skills/one/SKILL.md", "skills/two/SKILL.md"] })
    expect(given.length).toBe(2 * agents.filter((a) => a.crew_id === crew!.id).length)
  })

  test("Z11 /skills/<id>?tab=agents lands on Used by", async ({ page }) => {
    await seed(page)
    const { skills } = await api(page)
    await page.goto(`/skills/${skills[0].id}?tab=agents`)
    await expect(page).toHaveURL(new RegExp(`skill=${skills[0].id}`))
    await expect(page.getByRole("tab", { name: /^Used by/, selected: true })).toBeVisible()
  })
})

test.describe("Skills phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("L01 phone 390 shows rows, not a squeezed table", async ({ page }) => {
    await seed(page)
    await page.goto("/skills")
    await expect(page.locator("[data-slot=skills-phone-rows]")).toBeVisible()
    await expect(page.locator("[data-slot=skills-table]")).toHaveCount(0)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})

test.describe("Skills v67 parity", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("PAR-072 trust pills are sans and the toolbar has a Filter button (PAR-073)", async ({ page }) => {
    await seed(page)
    await page.goto("/skills")
    await expect(rows(page).first()).toBeVisible()
    await expect(rows(page).first().locator("[data-slot=status-pill]")).not.toHaveCSS("font-family", /mono/i)
    await expect(panel(page).getByRole("button", { name: /^Filter/ })).toHaveAttribute("aria-expanded", "false")
  })

  test("PAR-124 skill page header: sans trust pill, slug only when it differs from the title", async ({ page }) => {
    await seed(page)
    const { skills } = await api(page)
    await page.goto(`/skills?skill=${skills[0].id}`)
    const head = page.locator("[data-slot=skill-head]")
    await expect(head.locator("[data-slot=status-pill]")).not.toHaveCSS("font-family", /mono/i)
    const title = (await head.getByRole("heading", { level: 1 }).innerText()).trim()
    const slugLine = head.locator("p.font-mono")
    if (title === String(skills[0].slug)) await expect(slugLine).toHaveCount(0)
  })
})
