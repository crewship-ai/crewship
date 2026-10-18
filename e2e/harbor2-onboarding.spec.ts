import { test, expect, type Page, type Route } from "@playwright/test"

// Harbor 2 acceptance for the first-run setup (#3409). The signed-in E2E user
// finished onboarding long ago, so every onboarding read is answered here:
// the page believes it is a fresh workspace, and nothing is written to the
// real server. What is measured is the real page in a real browser — the
// card's geometry, the lockup holding still, the step bar, the phone footer —
// and the requests the wizard sends.

const WS = { id: "ws-harbor2-e2e", name: "Harbor Test", preferred_language: null as string | null, created_at: "2026-01-01T00:00:00Z" }

interface Fixture {
  preferredLanguage?: string | null
  savedCredential?: boolean
  runtime?: { available: boolean; in_use: boolean }
}

async function mockSetup(page: Page, f: Fixture = {}) {
  const state = { savedCredential: f.savedCredential ?? false, patched: [] as Record<string, unknown>[] }
  const json = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

  await page.route("**/api/v1/onboarding/status", (r) => json(r, { completed: false }))
  await page.route("**/api/v1/system/runtime", (r) => json(r, f.runtime ?? { available: true, in_use: true }))
  await page.route("**/api/v1/system/telemetry", (r) => json(r, { enabled: false }))
  await page.route("**/api/v1/workspaces", (r) =>
    r.request().method() === "GET"
      ? json(r, [{ ...WS, preferred_language: f.preferredLanguage ?? null }])
      : r.fallback(),
  )
  await page.route(`**/api/v1/workspaces/${WS.id}`, async (r) => {
    if (r.request().method() !== "PATCH") return r.fallback()
    state.patched.push(r.request().postDataJSON())
    return json(r, { ...WS })
  })
  await page.route("**/api/v1/credentials?*", (r) =>
    json(
      r,
      state.savedCredential
        ? [{ id: "cred-e2e", name: "CLAUDE_CODE_OAUTH_TOKEN", provider: "anthropic", type: "AI_CLI_TOKEN", status: "ACTIVE" }]
        : [],
    ),
  )
  // The crew step's Guide: answer "unavailable" so the template path shows
  // and no container is started.
  await page.route("**/api/v1/onboarding/setup-agent/start", (r) => json(r, { error: "unavailable" }, 503))
  return state
}

/**
 * Wait until the setup has restored and the real step-1 card is up. The
 * route first shows the loading skeleton, which is also a SetupCard; measuring
 * right after goto() can catch that one while it unmounts (boundingBox null).
 */
async function openStepOne(page: Page) {
  await page.goto("/onboarding")
  await expect(page.getByRole("heading", { level: 1, name: "Set up Crewship" })).toBeVisible()
  await expect(page.locator("[data-slot=setup-card][data-state=loading]")).toHaveCount(0)
}

async function box(page: Page, selector: string) {
  const b = await page.locator(selector).first().boundingBox()
  expect(b, selector).not.toBeNull()
  return b!
}

test.describe("setup card — desktop", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("step 1 is an 800 px card under a fixed lockup, with the kit step bar and no spinner", async ({ page }) => {
    await mockSetup(page)
    await page.goto("/onboarding")
    await expect(page.getByRole("heading", { level: 1, name: "Set up Crewship" })).toBeVisible()
    expect(Math.round((await box(page, "[data-slot=setup-card]")).width)).toBe(800)
    await expect(page.getByRole("navigation", { name: "Steps" })).toBeVisible()
    await expect(page.locator(".animate-spin")).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Close" })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Back" })).toHaveCount(0)
    await expect(page.getByTestId("onboarding-docker-pill")).toHaveText("Docker detected")
  })

  test("step 1 → 2 persists preferred_language and the lockup does not move", async ({ page }) => {
    const state = await mockSetup(page)
    await page.goto("/onboarding")
    const name = page.locator("#workspace_name")
    await name.fill("a")
    await expect(page.getByText("At least 2 characters")).toBeVisible()
    await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled()
    await name.fill("Harbor Test")
    const logoBefore = await box(page, "[data-slot=setup-frame] svg")
    await page.getByRole("button", { name: "Continue" }).click()
    await expect(page.getByRole("heading", { level: 1, name: "Give your agents a model" })).toBeVisible()
    expect(state.patched).toHaveLength(1)
    expect(state.patched[0]).toMatchObject({ name: "Harbor Test" })
    expect(typeof state.patched[0].preferred_language).toBe("string")
    const logoAfter = await box(page, "[data-slot=setup-frame] svg")
    expect(Math.round(logoAfter.y)).toBe(Math.round(logoBefore.y))
    await expect(page.getByRole("button", { name: "Step 1: Workspace" })).toBeVisible()
  })

  // ONB-01 (#3496): the command snippet takes its sizes from the type tokens,
  // so it follows the Text size setting instead of a fixed 11 px.
  test("step 2 command snippet uses the micro text token", async ({ page }) => {
    await mockSetup(page, { preferredLanguage: "English" })
    await page.goto("/onboarding")
    const caption = page.getByText("Run this on your machine, then paste the output above:")
    await expect(caption).toBeVisible()
    const [got, token] = await caption.evaluate((el) => {
      const probe = document.createElement("span")
      probe.className = "text-micro"
      document.body.appendChild(probe)
      const want = getComputedStyle(probe).fontSize
      probe.remove()
      return [getComputedStyle(el).fontSize, want]
    })
    expect(got).toBe(token)
  })

  test("reload on step 2 returns to step 2", async ({ page }) => {
    await mockSetup(page, { preferredLanguage: "English" })
    await page.goto("/onboarding")
    await expect(page.getByRole("heading", { level: 1, name: "Give your agents a model" })).toBeVisible()
    await page.reload()
    await expect(page.getByRole("heading", { level: 1, name: "Give your agents a model" })).toBeVisible()
    await expect(page.locator("#workspace_name")).toHaveCount(0)
  })

  test("no runtime: the banner blocks Continue and Check again recovers", async ({ page }) => {
    let inUse = false
    await mockSetup(page, { preferredLanguage: "English" })
    await page.route("**/api/v1/system/runtime", (r) =>
      r.fulfill({ contentType: "application/json", body: JSON.stringify({ available: true, in_use: inUse }) }),
    )
    await page.goto("/onboarding")
    await page.locator("#api_key").fill("sk-ant-oat-e2e-fake-token")
    await expect(page.getByTestId("onboarding-runtime-blocker")).toContainText("Restart the server")
    await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled()
    await expect(page.getByTestId("onboarding-blocking-reason")).toHaveText("Waiting for a container runtime.")
    inUse = true
    await page.getByRole("button", { name: "Check again" }).click()
    await expect(page.getByRole("button", { name: "Continue" })).toBeEnabled()
  })

  test("pair flow with mocked /auth/pair/poll consumed advances token field to filled", async ({ page }) => {
    const state = await mockSetup(page, { preferredLanguage: "English" })
    await page.route("**/api/v1/auth/pair/start", (r) =>
      r.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: "E2E-PAIR", expires_at: new Date(Date.now() + 600_000).toISOString() }),
      }),
    )
    await page.route("**/api/v1/auth/pair/poll?*", (r) => {
      state.savedCredential = true // the CLI handed the model token over
      return r.fulfill({ contentType: "application/json", body: JSON.stringify({ status: "consumed" }) })
    })
    await page.goto("/onboarding")
    await page.getByRole("button", { name: /Also pair my CLI/ }).click()
    await expect(page.getByText(/crewship login --pair --code=E2E-PAIR/)).toBeVisible()
    // one pairing component for Connect CLI and the setup (COM-09)
    await expect(page.locator("[data-slot=cli-pairing]")).toHaveCount(1)
    await expect(page.getByText("CLI paired and model token received.")).toBeVisible({ timeout: 15_000 })
    await expect(page.locator("#api_key")).toBeDisabled()
    await expect(page.locator("#api_key")).toHaveAttribute("placeholder", "Token saved — encrypted on the server")
    await expect(page.getByRole("button", { name: "Continue" })).toBeEnabled()
  })

  test("crew step widens to 960 px; an unreachable Guide lands on templates with a banner", async ({ page }) => {
    await mockSetup(page, { preferredLanguage: "English", savedCredential: true })
    await page.goto("/onboarding")
    await expect(page.getByTestId("onboarding-guide-unavailable")).toBeVisible({ timeout: 20_000 })
    expect(Math.round((await box(page, "[data-slot=setup-card]")).width)).toBe(960)
    await expect(page.locator("button[aria-pressed]")).toHaveCount(5)
    await page.getByRole("button", { name: /Software Development/ }).click()
    await expect(page.getByTestId("onboarding-template-agent")).toHaveCount(4)
    await expect(page.getByRole("button", { name: "Step 1: Workspace" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Step 2: Model" })).toBeVisible()
  })

  test("Skip setup asks first, with Keep going focused", async ({ page }) => {
    await mockSetup(page)
    await page.goto("/onboarding")
    await page.getByRole("button", { name: "Skip setup" }).click()
    const dialog = page.getByRole("alertdialog")
    await expect(dialog.getByText(/this setup does not open again/)).toBeVisible()
    await expect(dialog.getByRole("button", { name: "Keep going" })).toBeFocused()
    await dialog.getByRole("button", { name: "Keep going" }).click()
    await expect(dialog).toHaveCount(0)
  })
})


async function pairingPending(page: Page) {
  await mockSetup(page, { preferredLanguage: "English" })
  await page.route("**/api/v1/auth/pair/start", (r) =>
    r.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ code: "E2E-PAIR", expires_at: new Date(Date.now() + 600_000).toISOString() }),
    }),
  )
  await page.route("**/api/v1/auth/pair/poll?*", (r) =>
    r.fulfill({ contentType: "application/json", body: JSON.stringify({ status: "pending" }) }),
  )
  await page.goto("/onboarding")
  await page.getByRole("button", { name: /Also pair my CLI/ }).click()
  await expect(page.getByTestId("pair-command")).toContainText("E2E-PAIR")
}

test.describe("setup card — tablet", () => {
  test.use({ viewport: { width: 1024, height: 768 } })

  test("the card keeps its 800 px width with room either side", async ({ page }) => {
    await mockSetup(page)
    await openStepOne(page)
    const card = await box(page, "[data-slot=setup-card]")
    expect(Math.round(card.width)).toBe(800)
    expect(card.x).toBeGreaterThanOrEqual(16)
  })

  test("pairing block: command, labelled Copy and the waiting note fit the card", async ({ page }) => {
    await pairingPending(page)
    await expect(page.getByRole("button", { name: "Copy command" })).toHaveText("Copy")
    await expect(page.getByText(/Waiting for the CLI/)).toBeVisible()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})

test.describe("setup card — phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("full-bleed card, step count instead of labels, a sticky footer with 44 px buttons", async ({ page }) => {
    await mockSetup(page)
    await openStepOne(page)
    const card = await box(page, "[data-slot=setup-card]")
    expect(Math.round(card.width)).toBe(390)
    await expect(page.locator(".wizard-steps-count")).toHaveText("1 / 3")
    const footer = await box(page, "[data-slot=setup-card-footer]")
    expect(footer.y + footer.height).toBeLessThanOrEqual(844 + 1)
    for (const name of ["Skip setup", "Continue"]) {
      const b = await page.getByRole("button", { name }).boundingBox()
      expect(b, name).not.toBeNull()
      expect(b!.height, name).toBeGreaterThanOrEqual(44)
    }
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })

  test("pairing block: 44 px Copy, no horizontal overflow", async ({ page }) => {
    await pairingPending(page)
    const copy = await page.getByRole("button", { name: "Copy command" }).boundingBox()
    expect(copy!.height).toBeGreaterThanOrEqual(44)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})
