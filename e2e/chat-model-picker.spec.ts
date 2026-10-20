import { test, expect, type Page } from "@playwright/test"
import { requireThrowawayServer } from "./throwaway-guard"

// The chat's model indicator and catalogue, against a REAL backend.
//
// Runs only on a disposable instance from scripts/throwaway-server.sh (a fresh
// seeded database, own port and data directory), never on a shared dev slot or
// stage: it sends a message and so creates a conversation. The guard in
// ./throwaway-guard skips it unless CREWSHIP_THROWAWAY_MANIFEST is set.
//
// Nothing is mocked. The chat's model is whatever the server's run-preflight
// reports for the agent; the list comes from GET /api/v1/models for the
// provider that preflight names. The page must never call the retired
// /api/v1/llm/models path and must never present a catalogue entry as the
// model in effect. A seeded offline-demo instance has no provider credential,
// so the server answers from its curated set and the page must say so.
requireThrowawayServer()
test.describe.configure({ mode: "serial" })

const TIMEOUT = 20_000
// A dev server compiles the chat route on first visit, which can take a minute.
const COLD_ROUTE = 150_000
test.setTimeout(COLD_ROUTE + 60_000)

/** The nth agent: a flow that sends uses its own agent, because an agent with a
 *  conversation opens it, and the draft state is what the first step checks. */
async function agentSlug(page: Page, nth: number): Promise<string> {
  await page.goto("/")
  const slug = await page.evaluate(async (nth) => {
    const workspaces = await (await fetch("/api/v1/workspaces")).json()
    for (const ws of Array.isArray(workspaces) ? workspaces : []) {
      const agents = await (await fetch(`/api/v1/agents?workspace_id=${ws.id}`)).json()
      const list = (Array.isArray(agents) ? agents : []).filter((x: { slug?: string }) => x?.slug)
      if (list.length > nth) return list[nth].slug as string
    }
    return null
  }, nth)
  expect(slug, "no agent with a slug on this instance - seed one first").not.toBeNull()
  return slug!
}

async function openWithPreflight(page: Page, slug: string) {
  await page.goto(`/chat/${slug}`, { timeout: COLD_ROUTE })
  await expect(page.getByPlaceholder(/^Message /)).toBeVisible({ timeout: COLD_ROUTE })
  // A draft has no conversation yet, so there is no configured model to show
  // and the control must not invent one.
  await expect(page.getByRole("button", { name: /^Model: / })).toHaveCount(0)
  // Sending creates the conversation; the seeded instance has no provider, so
  // the agent will not answer, which this test does not need. The reload then
  // opens the saved conversation the way a returning user does.
  const created = page.waitForResponse((r) => r.request().method() === "POST" && /\/agents\/[^/]+\/chats\?/.test(r.url()), { timeout: TIMEOUT })
  await page.getByPlaceholder(/^Message /).fill("model picker check")
  await page.locator('button[type="submit"]').last().click()
  expect((await created).status()).toBe(201)
  const preflight = page.waitForResponse((r) => r.url().includes("/run-preflight"), { timeout: COLD_ROUTE })
  await page.reload({ timeout: COLD_ROUTE })
  return preflight
}

async function checkFlow(page: Page, nth: number) {
  const requests: string[] = []
  page.on("request", (r) => requests.push(r.url()))
  const slug = await agentSlug(page, nth)
  const response = await openWithPreflight(page, slug)
  expect(response.status()).toBe(200)
  const snapshot = await response.json()

  // The current model is the configured one, not the first catalogue entry.
  const provider: string | undefined = snapshot.payer?.provider
  // The list is requested only when the menu opens.
  await expect(page.getByRole("button", { name: /^Model: / })).toBeVisible({ timeout: TIMEOUT })
  expect(requests.some((u) => u.includes("/api/v1/models"))).toBe(false)
  if (!snapshot.model) {
    // An agent with no llm_model: say so, do not invent a model.
    await page.getByRole("button", { name: "Model: not set" }).click()
    await expect(page.getByText("No model is set on the agent; the provider default may apply.")).toBeVisible()
    return
  }
  const catalogue = provider
    ? page.waitForResponse((r) => r.url().includes("/api/v1/models?"), { timeout: TIMEOUT })
    : null
  const trigger = page.getByRole("button", { name: `Model: ${snapshot.model}` })
  await expect(trigger).toBeVisible({ timeout: TIMEOUT })
  // Open with the keyboard, as a keyboard user does.
  await trigger.focus()
  await page.keyboard.press("Enter")
  await expect(page.getByText(`Configured for this chat: ${snapshot.model}`)).toBeVisible()

  if (provider && catalogue) {
    const listed = await catalogue
    expect(listed.url()).toContain(`/api/v1/models?provider=${encodeURIComponent(provider)}&`)
    expect(listed.status()).toBe(200)
    const body = await listed.json()
    // The label follows what the server said about the list's source.
    const label = body.source === "live" ? "Live list" : "Curated list"
    if (body.models.length) {
      await expect(page.getByText(new RegExp(`${label} from `))).toBeVisible({ timeout: TIMEOUT })
      // The list is a labelled, focusable scroll region the keyboard reaches.
      const list = page.getByRole("list", { name: `Models listed for ${provider}` })
      await expect(list).toBeVisible()
      await list.focus()
      await expect(list).toBeFocused()
    } else {
      await expect(page.getByText(/No models are listed for /)).toBeVisible({ timeout: TIMEOUT })
    }
  } else {
    await expect(page.getByText(/provider is not reported/)).toBeVisible()
    expect(requests.some((u) => u.includes("/api/v1/models"))).toBe(false)
  }
  await expect(page.getByText(/does not mean you may use it/)).toBeVisible()
  expect(requests.some((u) => u.includes("/llm/models"))).toBe(false)
  // Opening the list did not change the chat's model.
  await page.keyboard.press("Escape")
  await expect(page.getByText(/does not mean you may use it/)).toHaveCount(0)
  // Escape returns focus to the control and leaves the model unchanged.
  await expect(page.getByRole("button", { name: `Model: ${snapshot.model}` })).toBeFocused()
}

test("desktop: configured model is current and the catalogue is labelled", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await checkFlow(page, 0)
})

test("phone: the model control fits and opens inside the viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await checkFlow(page, 1)
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})
