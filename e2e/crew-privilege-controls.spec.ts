import type { Page } from "@playwright/test"
import { test, expect } from "./fixtures/auth"

// #1380 / #1377 — the container-privilege escape hatches and the egress
// ergonomics toggles only exist as *rendered* guardrails. A Vitest unit test
// proves the component serializes correctly; it can't prove the control is
// reachable from a real crew page behind three layers of tab + collapsible.
// This spec walks the path an operator actually walks.

const TIMEOUT = 15_000

async function openCrewSettings(page: Page) {
  await page.goto("/crews")
  const crewRow = page
    .locator("aside button")
    .filter({ hasText: /^(Research|DevOps|Engineering|Quality)/ })
    .first()
  await expect(crewRow).toBeVisible({ timeout: TIMEOUT })
  await crewRow.click()
  await expect(page).toHaveURL(/[?&]crew=/, { timeout: TIMEOUT })
  await page.getByRole("tab", { name: /Settings/i }).click()
  await expect(page.getByRole("heading", { name: /Runtime & security/i })).toBeVisible({
    timeout: TIMEOUT,
  })
}

async function openSecurityTab(page: Page) {
  // "Container image & features" is a <details> collapsible.
  const section = page.getByText(/Container image/i).first()
  await section.click()
  await page.getByRole("tab", { name: /^Security$/ }).click()
  await expect(page.getByRole("switch", { name: /Privileged mode/i })).toBeVisible({
    timeout: TIMEOUT,
  })
}

test.describe("crew runtime — container-privilege controls (#1380)", () => {
  test("privileged toggle is labeled, explained, and flips the isolation badge", async ({
    page,
  }) => {
    await openCrewSettings(page)
    await openSecurityTab(page)

    // The whole point of the issue: the escape hatch that deserves the most
    // friction must carry the explanation, not be a bare JSON key.
    await expect(page.getByText(/no-new-privileges/i)).toBeVisible()
    await expect(page.getByText(/read-only rootfs/i)).toBeVisible()

    const toggle = page.getByRole("switch", { name: /Privileged mode/i })
    const wasOn = (await toggle.getAttribute("aria-checked")) === "true"
    if (await toggle.isEnabled()) {
      await toggle.click()
      // Posture is surfaced immediately, before any save.
      if (!wasOn) {
        await expect(page.getByText(/Isolation reduced/i).first()).toBeVisible()
      }
      // Leave the crew exactly as we found it — this spec must not mutate
      // shared dev state.
      await toggle.click()
    } else {
      // Non-admin, or the workspace has not set allow_privileged_credentials.
      await expect(page.getByText(/allow_privileged_credentials/i)).toBeVisible()
    }
  })

  test("capability picker only offers what the save path accepts", async ({ page }) => {
    await openCrewSettings(page)
    await openSecurityTab(page)

    // NET_BIND_SERVICE is the sole directly-grantable cap
    // (internal/devcontainer/features.go allowedFeatureCapAdd).
    await expect(page.getByRole("checkbox", { name: "NET_BIND_SERVICE" })).toBeEnabled()
    // Everything broader would 400 on save — it must not be selectable.
    await expect(page.getByRole("checkbox", { name: "SYS_ADMIN" })).toBeDisabled()
    await expect(page.getByText(/privileged only/i).first()).toBeVisible()
  })

  test("mount editor rejects the docker socket inline", async ({ page }) => {
    await openCrewSettings(page)
    await openSecurityTab(page)

    await page.getByRole("button", { name: /Add mount/i }).click()
    await page.getByLabel(/Mount source/i).fill("/var/run/docker.sock")
    // Mirrors internal/devcontainer/mount_validate.go — flagged before save,
    // not after a confusing 400.
    await expect(page.getByText(/is not allowed/i)).toBeVisible()
    await page.getByRole("button", { name: /Remove mount 0/i }).click()
  })

  test("start hook editor names the /crew auto-exec risk", async ({ page }) => {
    await openCrewSettings(page)
    await openSecurityTab(page)

    await expect(page.getByLabel(/Start hook init script/i)).toBeVisible()
    await expect(page.getByText(/agent-writable host bind/i)).toBeVisible()
  })
})

test.describe("crew network policy — egress ergonomics (#1377)", () => {
  test("private-endpoints toggle is reachable and explains the instance ceiling", async ({
    page,
  }) => {
    await openCrewSettings(page)
    await page.getByText(/Network policy/i).first().click()

    const toggle = page.getByRole("switch", { name: /Private endpoints/i })
    await expect(toggle).toBeVisible({ timeout: TIMEOUT })
    // The #1 "why is my LAN model still blocked?" surprise must be on screen.
    await expect(page.getByText(/CREWSHIP_ALLOW_PRIVATE_ENDPOINTS/)).toBeVisible()
  })

  test("restricted mode advertises wildcards and the registry preset", async ({ page }) => {
    await openCrewSettings(page)
    await page.getByText(/Network policy/i).first().click()

    await page.getByRole("button", { name: /^Restricted$/ }).click()
    await expect(page.getByText(/\*\.github\.com/).first()).toBeVisible()
    await expect(page.getByRole("button", { name: /Allow package registries/i })).toBeVisible()
  })
})
