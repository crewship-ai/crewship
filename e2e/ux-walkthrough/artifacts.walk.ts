// Artifacts library walkthrough (dev2): create, find, open, share dialog,
// chat handoff and back. Desktop 1440×900 and phone 390×844.
import { expect, test } from "@playwright/test"
import { audit, focused, note, pause, shot, tabWalk, type Viewport } from "./audit"
import { browserContext, ownerApi, ownerStorage, walkWorkspace } from "./session"
import { step } from "./steps"

for (const vp of ["desktop", "mobile"] as Viewport[]) {
  test(`artifacts ${vp}`, async ({ browser }) => {
    test.skip(process.env.UX_TARGET !== "artifacts")
    const api = await ownerApi()
    const ws = await walkWorkspace(api)
    const ctx = await browserContext(browser, vp, await ownerStorage())
    const page = await ctx.newPage()
    const q = `workspace_id=${ws.id}`
    const name = `PRD UX walkthrough stránka ${vp} ${Date.now().toString(36)}`

    await step(page, `A01-library-load-${vp}`, async () => {
      const t0 = Date.now()
      await page.goto(`/artifacts?${q}`)
      const loading = await page.getByLabel("Loading artifacts").isVisible().catch(() => false)
      await page.getByRole("button", { name: "New artifact" }).first().waitFor({ timeout: 30_000 })
      await pause(1500)
      const a = await audit(page, `A01-library-${vp}`, vp)
      return { loadingSkeletonSeen: loading, ms: Date.now() - t0, unnamed: a.unnamed.length, small: a.small.length, overflow: a.horizontalOverflow }
    })

    await step(page, `A02-library-keyboard-${vp}`, async () => {
      await page.locator("body").click({ position: { x: 5, y: 5 } }).catch(() => {})
      await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
      const stops = await tabWalk(page, `A02-library-${vp}`, 30)
      const reachedNew = stops.findIndex((s) => /New artifact/.test(s.name))
      return { stops: stops.length, firstNewArtifactAt: reachedNew, invisibleFocus: stops.filter((s) => !s.visible).map((s) => `${s.i}:${s.tag}:${s.name}`) }
    })

    await step(page, `A03-new-dialog-keyboard-${vp}`, async () => {
      const opener = page.getByRole("button", { name: "New artifact" }).first()
      await opener.focus()
      await page.keyboard.press("Enter")
      const dialog = page.getByRole("dialog")
      await dialog.waitFor({ timeout: 10_000 })
      const dialogName = await dialog.getAttribute("aria-label").catch(() => null) ?? (await dialog.getAttribute("aria-labelledby").catch(() => null))
      const focusOnOpen = await focused(page)
      const a = await audit(page, `A03-new-dialog-${vp}`, vp)
      const stops = await tabWalk(page, `A03-new-dialog-${vp}`, 14)
      const escaped = stops.some((s) => s.tag !== "body" && !(s.name || "").length) // informational
      await page.keyboard.press("Escape")
      await expect(dialog).toBeHidden({ timeout: 5_000 })
      const after = await focused(page)
      return { dialogName, focusOnOpen, focusAfterEscape: after, returnedToOpener: /New artifact/.test(after), unnamed: a.unnamed, small: a.small.map((c) => `${c.name || c.html} ${c.w}x${c.h}`), escaped }
    })

    let created = false
    await step(page, `A04-create-page-${vp}`, async () => {
      await page.getByRole("button", { name: "New artifact" }).first().click()
      const dialog = page.getByRole("dialog")
      const tiles = await dialog.getByRole("group", { name: "Kind" }).getByRole("button").allInnerTexts()
      const disabled = await dialog.getByRole("group", { name: "Kind" }).locator("button[disabled]").allInnerTexts()
      await dialog.getByRole("group", { name: "Kind" }).getByRole("button", { name: /Page with panels/ }).click()
      await dialog.getByLabel("Name").fill(name)
      await shot(page, `A04-new-dialog-filled-${vp}`)
      const primary = dialog.getByRole("button", { name: /Create page|Create and edit/ })
      await primary.click()
      await page.waitForURL(/\/artifacts\/|item=|\/pages\//, { timeout: 30_000 })
      await pause(2500)
      created = true
      const a = await audit(page, `A04-created-open-${vp}`, vp)
      return { tiles: tiles.map((t) => t.replace(/\s+/g, " ")), disabled, url: page.url().replace(/workspace_id=[^&]+/, "workspace_id=…"), unnamed: a.unnamed.length, small: a.small.map((c) => `${c.name || c.html} ${c.w}x${c.h}`) }
    })

    await step(page, `A05-find-in-library-${vp}`, async () => {
      await page.goto(`/artifacts?${q}`)
      const search = page.getByRole("textbox", { name: "Search artifacts" })
      const visible = await search.isVisible().catch(() => false)
      if (!visible) {
        await shot(page, `A05-library-no-search-${vp}`)
      }
      await search.fill("UX walkthrough", { timeout: 10_000 })
      await pause(1500)
      const hit = page.getByRole("button", { name: new RegExp(`Open ${name}`) }).or(page.getByRole("link", { name: new RegExp(name) })).first()
      const found = await hit.isVisible().catch(() => false)
      const a = await audit(page, `A05-library-search-${vp}`, vp)
      const counts = await page.getByRole("tab").allInnerTexts().catch(() => [] as string[])
      // Where did it go? The panel's "Empty" view.
      const emptyView = page.getByRole("button", { name: /^Empty/ }).first()
      let inEmptyView = false
      if (await emptyView.isVisible().catch(() => false)) {
        await emptyView.click()
        await pause(1200)
        inEmptyView = await hit.isVisible().catch(() => false)
        await shot(page, `A05-library-empty-view-${vp}`)
      }
      return { searchVisibleWithoutAction: visible, foundInDefaultSearch: found, inEmptyView, tabCounts: counts.map((t) => t.replace(/\s+/g, " ")), small: a.small.map((c) => `${c.name || c.html} ${c.w}x${c.h}`), created }
    })

    await step(page, `A06-open-by-keyboard-${vp}`, async () => {
      if (!(await page.getByRole("button", { name: new RegExp(`Open ${name}`) }).first().isVisible().catch(() => false))) {
        await page.goto(`/artifacts?${q}&view=empty`)
        await pause(2000)
      }
      const hit = page.getByRole("button", { name: new RegExp(`Open ${name}`) }).first()
      await hit.focus()
      await page.keyboard.press("Enter")
      await pause(2500)
      const urlAfter = page.url().replace(/workspace_id=[^&]+/, "workspace_id=…")
      await audit(page, `A06-opened-by-keyboard-${vp}`, vp)
      // Back to the library and where focus lands.
      await page.goBack()
      await pause(2000)
      return { urlAfter, focusAfterBack: await focused(page), urlBack: page.url().replace(/workspace_id=[^&]+/, "workspace_id=…") }
    })

    await step(page, `A07-share-dialog-${vp}`, async () => {
      await page.goto(`/artifacts?${q}&view=empty`)
      await pause(2000)
      // Select the row/card, then use Share from the preview or the row.
      const open = page.getByRole("button", { name: new RegExp(`Open ${name}`) }).first()
      await open.click()
      await pause(2000)
      const share = page.getByRole("button", { name: /^Share/ }).first()
      await share.waitFor({ timeout: 10_000 })
      await share.focus()
      await page.keyboard.press("Enter")
      const dialog = page.getByRole("dialog")
      await dialog.waitFor({ timeout: 10_000 })
      const a = await audit(page, `A07-share-dialog-${vp}`, vp)
      const label = await dialog.getAttribute("aria-label") ?? (await dialog.getAttribute("aria-labelledby"))
      const heading = await dialog.getByRole("heading").first().innerText().catch(() => "")
      const stops = await tabWalk(page, `A07-share-dialog-${vp}`, 10)
      await page.keyboard.press("Escape")
      await pause(500)
      const after = await focused(page)
      return { label, heading, unnamed: a.unnamed, small: a.small.map((c) => `${c.name || c.html} ${c.w}x${c.h}`), focusAfterEscape: after, returnedToShare: /^button\[.*\] Share/.test(after), stops: stops.map((s) => `${s.tag}:${s.name}:${s.visible ? "ring" : "NO-RING"}`) }
    })

    await step(page, `A08-chat-handoff-${vp}`, async () => {
      await page.goto(`/artifacts?${q}`)
      await page.getByRole("button", { name: "New artifact" }).first().click()
      const dialog = page.getByRole("dialog")
      await dialog.getByRole("group", { name: "Kind" }).getByRole("button", { name: /Dashboard/ }).click()
      await pause(500)
      await shot(page, `A08-new-dashboard-dialog-${vp}`)
      const primary = await dialog.getByRole("button", { name: /Continue in chat|Create/ }).last().innerText()
      const startWith = await dialog.getByRole("radiogroup", { name: "Start with" }).or(dialog.getByRole("group", { name: "Start with" })).first().innerText().catch(() => "")
      await dialog.getByRole("textbox", { name: /What should/ }).fill("PRD UX walkthrough: přehled tří čísel (nepřeposílat agentovi)").catch(() => {})
      await dialog.getByRole("button", { name: /Continue in chat/ }).click()
      await page.waitForURL(/\/chat/, { timeout: 20_000 })
      await pause(3000)
      const a = await audit(page, `A08-chat-draft-${vp}`, vp)
      const composer = await page.getByRole("textbox").first().inputValue().catch(async () => page.getByRole("textbox").first().innerText().catch(() => ""))
      const artifactLinks = await page.getByRole("link", { name: /Artifacts/ }).count()
      return { primary, startWith, composerHasDraft: composer.includes("PRD UX walkthrough"), composerPreview: composer.slice(0, 120), linksBackToArtifacts: artifactLinks, small: a.small.length, unnamed: a.unnamed.map((c) => c.html) }
    })

    await step(page, `A09-chat-to-artifacts-${vp}`, async () => {
      // Without sending: is there a route from chat back to the artifact library?
      const link = page.getByRole("link", { name: "Artifacts" }).first()
      if (!(await link.isVisible().catch(() => false))) {
        // Phone: the navigation sits behind a menu/tab bar.
        const menu = page.getByRole("button", { name: /Menu|More|Navigation|Open sidebar|Toggle Sidebar/i }).first()
        await menu.click().catch(() => {})
        await pause(800)
        await shot(page, `A09-phone-nav-${vp}`)
      }
      await page.getByRole("link", { name: "Artifacts" }).first().click({ timeout: 10_000 })
      await page.waitForURL(/\/artifacts/, { timeout: 15_000 })
      await pause(1500)
      await shot(page, `A09-back-in-library-${vp}`)
      return { url: page.url().replace(/workspace_id=[^&]+/, "workspace_id=…") }
    })

    note(`done:artifacts-${vp}`, true)
    await ctx.close()
    await api.dispose()
  })
}
