// Targeted check: does creating a Sheet from New artifact end the session?
// Records only status codes and paths of failing API calls (no bodies, no headers).
import { test } from "@playwright/test"
import { note, pause, shot } from "./audit"
import { createFromLibrary } from "./native-helpers"
import { browserContext, freshOwnerStorage, ownerApi, walkWorkspace } from "./session"

test("sheet create session", async ({ browser }) => {
  test.skip(process.env.UX_TARGET !== "sheetsignout")
  const api = await ownerApi()
  const ws = await walkWorkspace(api)
  const ctx = await browserContext(browser, "desktop", await freshOwnerStorage())
  const page = await ctx.newPage()
  const failures: Array<{ t: number; status: number; method: string; path: string }> = []
  const t0 = Date.now()
  page.on("response", (r) => {
    const u = new URL(r.url())
    if (r.status() >= 400 && u.pathname.startsWith("/api/")) failures.push({ t: Date.now() - t0, status: r.status(), method: r.request().method(), path: u.pathname })
  })
  // Control: the same session stays signed in on the library for ~3 min (reload every 30 s) without creating anything.
  await page.goto(`/artifacts?workspace_id=${ws.id}`)
  const controlFailuresBefore = failures.length
  for (let i = 0; i < 6; i++) {
    await pause(30_000)
    await page.reload()
    await pause(2000)
  }
  note("X-control-failures", failures.slice(controlFailuresBefore))
  const controlSignedIn = !/\/login|signin/.test(page.url()) && (await page.getByRole("button", { name: "New artifact" }).first().isVisible().catch(() => false))
  await shot(page, "X01-control-after-60s-desktop")
  let id = ""
  let err = ""
  try {
    id = await createFromLibrary(page, ws.id, "Sheet", `PRD UX walkthrough Sheet signout ${Date.now().toString(36)}`, "desktop", "X02-sheet")
  } catch (e) {
    err = String(e).split("\n")[0]
  }
  await pause(20_000)
  await shot(page, "X02-after-sheet-create-desktop")
  const signedOut = await page.getByText(/Signed out on this device|Sign in to Crewship/).first().isVisible().catch(() => false)
  const me = await api.get("/api/v1/auth/me").then((r) => r.status()).catch(() => -1)
  note("X-sheet-signout", { controlSignedIn, id, err, signedOut, url: page.url().replace(/workspace_id=[^&]+/, "workspace_id=…"), failures, apiContextStillAuthenticated: me })
  await ctx.close()
  await api.dispose()
})
