import { test, expect, type Page, type Locator } from "@playwright/test"
import { createHash } from "node:crypto"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { Avatar, Style } from "@dicebear/core-v10"

// #3789 — animated Voxel avatars.
//
// 1. The premise, checked in the real browser engine: CSS @keyframes inside an
//    SVG that is loaded as an <img> data URI run in Chromium. Nothing else in
//    the feature would work if that were false, and no unit test can see it.
//
//    The first run of this spec also found that Chromium's reduced-motion
//    EMULATION does not reach the SVG image's own document: with
//    `reducedMotion: "reduce"` the SVG still moved. Whether a real OS setting
//    does is not observable headless, so the app does not rely on the SVG's
//    media query alone — it reads the OS setting itself and draws the still
//    render (the "OS asks for reduced motion" test below).
// 2. The picker offers Voxel Bot and Voxel Art marked New, and says how an
//    animated style behaves.
// 3. An agent page draws a voxel agent moving, and still under Reduce motion On.
//
// Read-only: the agent rows are rewritten in flight (page.route), nothing is
// saved, and the picker is closed with Cancel.

const require_ = createRequire(import.meta.url)

function voxelDataUri(animated: boolean): string {
  const definition = JSON.parse(readFileSync(require_.resolve("@dicebear/styles-v10/voxel-bot.json"), "utf8"))
  return new Avatar(new Style(definition), { seed: "e2e-voxel", size: 128, animationVariant: animated ? "slow" : "none" }).toDataUri()
}

/** Distinct frames of one element over `ms`, sampled every 100 ms. */
async function distinctFrames(el: Locator, ms: number): Promise<number> {
  const seen = new Set<string>()
  const until = Date.now() + ms
  while (Date.now() < until) {
    seen.add(createHash("sha1").update(await el.screenshot({ animations: "allow" })).digest("hex"))
    await el.page().waitForTimeout(100)
  }
  return seen.size
}

async function showImg(page: Page, src: string): Promise<Locator> {
  await page.setContent(`<!doctype html><body style="margin:0;background:#fff"><img id="a" width="128" height="128" src="${src}"></body>`)
  const img = page.locator("#a")
  await expect(img).toHaveJSProperty("complete", true)
  return img
}

test.describe("Chromium and animated SVG avatars", () => {
  // The slow variant blinks once every 6 s and stretches once every 9 s.
  test.setTimeout(60_000)

  test("animates CSS inside an SVG <img> data URI", async ({ page }) => {
    const img = await showImg(page, voxelDataUri(true))
    expect(await distinctFrames(img, 9_500)).toBeGreaterThan(1)
  })

  test("the still render never moves", async ({ page }) => {
    const img = await showImg(page, voxelDataUri(false))
    expect(await distinctFrames(img, 7_000)).toBe(1)
  })
})

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page, extra: Record<string, string> = {}) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, { ...PINNED, ...extra })
}

/** Every agent the app reads is drawn as Voxel Bot with no stored render. */
async function voxelAgents(page: Page) {
  await page.route(/\/api\/v1\/agents(\/[^/?]+)?(\?.*)?$/, async (route) => {
    if (route.request().method() !== "GET") return route.continue()
    const res = await route.fetch()
    const body = await res.json().catch(() => null)
    const patch = (a: Record<string, unknown>) =>
      a && typeof a === "object" && "slug" in a ? { ...a, avatar_style: "voxel-bot", avatar_url: null } : a
    const out = Array.isArray(body) ? body.map(patch) : patch(body)
    await route.fulfill({ response: res, json: out })
  })
}

async function firstAgent(page: Page): Promise<string> {
  await page.goto("/crews")
  const crewRow = page.locator("[data-crew-row]").first()
  await expect(crewRow).toBeVisible()
  const crew = (await crewRow.getAttribute("data-crew-row"))!
  await page.goto(`/crews?crew=${crew}&tab=team`)
  const row = page.locator("[data-agent-row]").first()
  await expect(row).toBeVisible()
  return (await row.getAttribute("data-agent-row"))!
}

test.describe("voxel avatars in the app", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("an agent page draws a voxel agent moving, with its status beside it", async ({ page }) => {
    await seed(page)
    await voxelAgents(page)
    const agent = await firstAgent(page)
    await page.goto(`/crews?agent=${agent}`)
    const header = page.getByTestId("agent-header")
    await expect(header.locator('img[data-animated="true"]').first()).toBeVisible()
    // Motion is never the only signal: the state is written out.
    await expect(header.locator('[data-slot="status-pill"], [role="status"]').first()).toBeVisible()
  })

  test("the same page keeps it still under Reduce motion On", async ({ page }) => {
    await seed(page, { "crewship-motion": "on" })
    await voxelAgents(page)
    const agent = await firstAgent(page)
    await page.goto(`/crews?agent=${agent}`)
    const header = page.getByTestId("agent-header")
    await expect(header.locator("img").first()).toBeVisible()
    await expect(header.locator('img[data-animated="true"]')).toHaveCount(0)
  })

  test("and when the OS asks for reduced motion (Reduce motion: System)", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" })
    await seed(page)
    await voxelAgents(page)
    const agent = await firstAgent(page)
    await page.goto(`/crews?agent=${agent}`)
    const header = page.getByTestId("agent-header")
    await expect(header.locator("img").first()).toBeVisible()
    await expect(header.locator('img[data-animated="true"]')).toHaveCount(0)
  })

  test("and with Animated avatars off", async ({ page }) => {
    await seed(page, { "crewship-avatar-animation": "off" })
    await voxelAgents(page)
    const agent = await firstAgent(page)
    await page.goto(`/crews?agent=${agent}`)
    const header = page.getByTestId("agent-header")
    await expect(header.locator("img").first()).toBeVisible()
    await expect(header.locator('img[data-animated="true"]')).toHaveCount(0)
  })

  test("the picker offers Voxel Bot and Voxel Art, marked New, and says how they move", async ({ page }) => {
    await seed(page)
    const agent = await firstAgent(page)
    await page.goto(`/crews?agent=${agent}`)
    await page.getByRole("button", { name: "Change avatar" }).click()
    const dialog = page.getByRole("dialog", { name: /Avatar/ })
    await expect(dialog).toBeVisible()
    const grid = dialog.getByTestId("avatar-style-grid")
    for (const label of ["Voxel Bot", "Voxel Art"]) {
      await expect(grid.getByTitle(label).getByText("New", { exact: true })).toBeVisible()
    }
    const hint = dialog.getByText("Moves gently; still when Reduce motion is on")
    await grid.getByTitle("Adventurer", { exact: true }).click()
    await expect(hint).toHaveCount(0)
    await grid.getByTitle("Voxel Art").click()
    await expect(hint).toBeVisible()
    await expect(dialog.getByTestId("avatar-preview")).toHaveAttribute("src", /^data:image\/svg\+xml/)
    await dialog.getByRole("button", { name: "Cancel" }).click()
    await expect(dialog).toHaveCount(0)
  })
})
