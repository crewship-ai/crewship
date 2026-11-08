import { test, expect, type Page } from "@playwright/test"
import { buildProfile } from "./build-profile"

// #5129: one address per artifact, /artifacts/<immutable page ID>. An old
// /pages/<slug> link from a chat, a notification or an e-mail resolves the
// slug with one page read and replaces itself with that address, so Back does
// not return to it. Every /api request is answered here; the spec needs only
// the static export (served by e2e/serve-restricted-ui.mjs).
//
// The checked-in main profile builds without the library, where the old link
// keeps opening on Pages. Set NEXT_PUBLIC_ARTIFACTS_LIBRARY=true for both the
// build and this run to verify the release behaviour.
const library = process.env.NEXT_PUBLIC_ARTIFACTS_LIBRARY === "true" || buildProfile().name === "release"

const WORKSPACE = { id: "ws-a", name: "Alpha", slug: "alpha", currentUserRole: "OWNER" }
const PAGE = { id: "page-brief", slug: "q4-brief", name: "Q4 brief", owner: "user/u-t", reach: ["owner"], updated_at: "2026-11-08T08:00:00Z" }

async function trustedSession(page: Page, reads: string[]) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    let body: unknown = []
    if (path === "/api/auth/session") body = { user: { id: "u-t", name: "Tom", email: "tom@example.test" }, expires: "2099-01-01T00:00:00Z" }
    else if (path === "/api/auth/csrf") body = { csrfToken: "t" }
    else if (path === "/api/v1/workspaces") body = [WORKSPACE]
    else if (path === "/api/v1/pages") body = [PAGE]
    else if (path === `/api/v1/pages/${PAGE.slug}` || path === `/api/v1/pages/%40${PAGE.id}` || path === `/api/v1/pages/@${PAGE.id}`) {
      reads.push(`${path}?${url.searchParams}`)
      body = PAGE
    } else if (path.startsWith("/api/v1/")) body = {}
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
  })
}

for (const [name, link] of [
  ["a slug link", `/pages/${PAGE.slug}?workspace_id=${WORKSPACE.id}`],
  ["a slug link without its workspace", `/pages/${PAGE.slug}`],
  ["an @<id> link", `/pages/%40${PAGE.id}?workspace_id=${WORKSPACE.id}`],
  ["a reference link", `/pages?ref=${PAGE.id}&workspace_id=${WORKSPACE.id}`],
] as const) {
  test(`${name} ends on the artifact's one address`, async ({ page }) => {
    const reads: string[] = []
    await trustedSession(page, reads)
    await page.goto("/routines")
    await page.goto(link)
    if (library) {
      await page.waitForURL(url => url.pathname === `/artifacts/${PAGE.id}`, { timeout: 20_000 })
      expect(new URL(page.url()).searchParams.get("workspace_id")).toBe(WORKSPACE.id)
      expect(reads.length).toBeGreaterThan(0)
      // Replaced, not pushed: Back returns to where the person was before the link.
      await page.goBack()
      await page.waitForURL(url => url.pathname === "/routines", { timeout: 10_000 })
    } else {
      await page.waitForTimeout(2_000)
      expect(new URL(page.url()).pathname).toMatch(/^\/pages(\/|$)/)
    }
  })
}

test("the public share address is not touched", async ({ page }) => {
  await trustedSession(page, [])
  await page.goto("/p/_")
  await page.waitForTimeout(1_000)
  expect(new URL(page.url()).pathname).toMatch(/^\/p\//)
})
