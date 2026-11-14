// Craft package gate (tools/craft-components/README.md, "Browser gate"):
// a Word or Sheet package may be pinned only when its editor works with the real
// host. scripts/ci/craft-package-gate.sh starts a throwaway server that trusts
// the freshly built packages, installs them through the CLI and runs this spec in
// full Chromium. Each application goes through: open, edit, save, reopen, edit,
// save. The server's head must hold both markers, so the reopened editor started
// from the saved document rather than a blank one.
import { expect, test, type Browser, type Page } from "@playwright/test"
import { apiAs, loginStorageState, type StorageState } from "../acceptance/prd/auth"
import * as N from "../acceptance/prd/outage-lostack/native"

const ORIGIN = process.env.CRAFT_GATE_ORIGIN ?? ""
const WORKSPACE = process.env.CRAFT_GATE_WORKSPACE ?? ""
const ACCOUNT = { email: process.env.CRAFT_GATE_EMAIL ?? "", password: process.env.CRAFT_GATE_PASSWORD ?? "" }
const CONNECT_MS = 60_000
const SAVE_MS = 45_000
// The host's own refusals. Any of them means the package must not be pinned.
const REFUSED = /did not connect safely|cannot be opened safely|connection to the editor ended|only available at/

let owner: StorageState
test.beforeAll(async () => {
  expect(ORIGIN && WORKSPACE && ACCOUNT.email && ACCOUNT.password, "CRAFT_GATE_ORIGIN, _WORKSPACE, _EMAIL and _PASSWORD are required").toBeTruthy()
  owner = await loginStorageState(ORIGIN, ACCOUNT)
})

/**
 * Opens the document in a new browser context with its own login and waits for the
 * editor's verified read-only start, failing at once on a host refusal. A login per
 * context: a context that refreshed its session rotates the refresh token, so a
 * second context started from the same cookies would be signed out.
 */
async function open(browser: Browser, ref: N.NativeRef, log: string[]): Promise<Page> {
  const context = await browser.newContext({ baseURL: ORIGIN, storageState: await loginStorageState(ORIGIN, ACCOUNT), viewport: N.VIEWPORT })
  const page = await context.newPage()
  page.on("console", (m) => { if (m.type() === "error" || m.type() === "warning") log.push(`${m.type()} ${m.text().slice(0, 300)}`) })
  page.on("pageerror", (e) => log.push(`pageerror ${e.message.slice(0, 300)}`))
  await page.goto(N.editorURL(ref))
  const ready = page.getByRole("status").filter({ hasText: /Read-only|Ready to edit/ }).first()
  const refused = page.getByText(REFUSED).first()
  await expect(ready.or(refused)).toBeVisible({ timeout: CONNECT_MS })
  if (await refused.isVisible()) throw new Error(`the editor did not start: ${await refused.innerText()}`)
  return page
}

// Word may capitalise a marker that starts a sentence ("Gate-wordcraft-…"). Markers are
// lower case and unique, so a case-insensitive match still identifies them.
const holds = (text: string, marker: string) => text.toLowerCase().includes(marker)

function edited(status: string, after: string): boolean {
  const saved = status.match(/^Saved · version (\d+)/)
  return saved ? BigInt(saved[1]) > BigInt(after) : /^(Unsaved draft|Saving)/.test(status)
}

/** Takes the lease, types a marker, saves with Ctrl+S and returns the new server version. */
async function editAndSave(page: Page, app: N.NativeApp, marker: string, cell: string, after: string): Promise<string> {
  const frame = await N.acquire(page, app)
  await N.typeMarker(page, frame, app, marker, cell)
  // Save only once the editor has told the host about the edit: the host shows an
  // unsaved draft, a save in progress, or a save newer than `after`.
  await expect.poll(async () => edited(await N.status(page), after), { timeout: SAVE_MS, message: `the editor never reported the ${marker} edit` }).toBe(true)
  await page.keyboard.press("Control+s")
  const version = await N.waitSaved(page, SAVE_MS, after)
  expect(version, `no "Saved · version" after ${marker}; status: ${await N.status(page)}`).toBeTruthy()
  await N.release(page)
  return version!
}

for (const app of ["wordcraft", "gridcraft"] as const) {
  test(`${N.APP_LABEL[app]} package: open, edit, save, reopen`, async ({ browser }, info) => {
    test.setTimeout(6 * 60_000)
    const log: string[] = []
    const api = await apiAs(ORIGIN, owner)
    try {
      const ref = await N.createNative(api, WORKSPACE, app, `Package gate ${N.APP_LABEL[app]}`)
      const first = `gate-${app}-one-${Date.now().toString(36)}`, second = `gate-${app}-two-${Date.now().toString(36)}`

      const page = await open(browser, ref, log)
      const v1 = await editAndSave(page, app, first, "B2", ref.initVersion)
      const head1 = await N.readHead(api, ref)
      expect(head1.version).toBe(v1)
      expect(holds(head1.text, first), "the saved document holds the first marker").toBe(true)
      await page.context().close()

      // Reopen in a fresh browser context: a new frame, a new bootstrap from the saved version.
      const again = await open(browser, ref, log)
      const v2 = await editAndSave(again, app, second, "B3", v1)
      const head2 = await N.readHead(api, ref)
      expect(head2.version).toBe(v2)
      expect(holds(head2.text, first), "the reopened editor kept the first marker").toBe(true)
      expect(holds(head2.text, second), "the saved document holds the second marker").toBe(true)
      await again.context().close()
    } finally {
      await info.attach("console", { body: log.join("\n") || "(none)", contentType: "text/plain" })
      await api.dispose()
    }
  })
}
