// Craft package gate (tools/craft-components/README.md, "Browser gate"):
// a Word or Sheet package may be pinned only when its editor works with the real
// host. scripts/ci/craft-package-gate.sh starts a throwaway server that trusts
// the freshly built packages, installs them through the CLI and runs this spec in
// full Chromium. Each application goes through: open, edit, save, reopen, edit,
// save. The server's head must hold both markers, so the reopened editor started
// from the saved document rather than a blank one.
import { execFileSync } from "node:child_process"
import { readFile } from "node:fs/promises"
import { fileURLToPath } from "node:url"
import { expect, test, type Browser, type Page } from "@playwright/test"
import { emptyGridCraftNativeDocument } from "../../lib/mooring/gridcraft-empty"
import { emptyGridCraftWorkbook } from "../../lib/mooring/gridcraft-empty-workbook"
import { apiAs, loginStorageState, type StorageState } from "../acceptance/prd/auth"
import * as N from "../acceptance/prd/outage-lostack/native"

const ORIGIN = process.env.CRAFT_GATE_ORIGIN ?? ""
const WORKSPACE = process.env.CRAFT_GATE_WORKSPACE ?? ""
const ACCOUNT = { email: process.env.CRAFT_GATE_EMAIL ?? "", password: process.env.CRAFT_GATE_PASSWORD ?? "" }
const CONNECT_MS = 60_000
const SAVE_MS = 45_000
// The host's own refusals. Any of them means the package must not be pinned.
const REFUSED = /did not connect safely|cannot be opened safely|connection to the editor ended|only available at|could not be converted/

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

/** Read the cell from the persisted XLSX. A shared string elsewhere is insufficient. */
async function expectMarker(page: Page, app: N.NativeApp, head: N.Head, cell: string, marker: string) {
  if (app === "wordcraft") {
    expect(holds(head.text, marker), "the saved document holds " + marker).toBe(true)
    return
  }
  const sheet = head.workbookParts?.["xl/worksheets/sheet1.xml"]
  expect(sheet, "the saved XLSX contains Sheet1").toBeTruthy()
  const actual = await page.evaluate(({ sheet, strings, cell }) => {
    const parser = new DOMParser()
    const sheetDoc = parser.parseFromString(sheet, "application/xml")
    const item = Array.from(sheetDoc.getElementsByTagNameNS("*", "c")).find(node => node.getAttribute("r") === cell)
    const value = item?.getElementsByTagNameNS("*", "v")[0]?.textContent
    const text = (node?: Element) => node && Array.from(node.getElementsByTagNameNS("*", "t"), tag => tag.textContent).join("")
    if (item?.getAttribute("t") === "s" && value != null && strings) {
      const lookup = parser.parseFromString(strings, "application/xml")
      return text(lookup.getElementsByTagNameNS("*", "si")[Number(value)])
    }
    return item?.getAttribute("t") === "inlineStr" ? text(item) : value
  }, { sheet: sheet!, strings: head.workbookParts?.["xl/sharedStrings.xml"], cell })
  expect(actual, "the saved value at " + cell).toBe(marker)
}

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

test("WordCraft upgrade: old editor stays on its profile across live default activation", async ({ browser }, info) => {
  const oldBundle = process.env.CRAFT_GATE_RETAINED_WORD_BUNDLE
  test.skip(!oldBundle, "this run uses the historical WordCraft package as its default")
  const nextArchive = process.env.CRAFT_GATE_NEW_WORD_PACKAGE
  if (!nextArchive) throw new Error("the two-profile gate requires the next WordCraft package")

  const log: string[] = []
  const api = await apiAs(ORIGIN, owner)
  try {
    // This fixture is a synthetic demo document made with the historical model.
    const fixture = new URL("../../cmd/crewship/seeddata/showcase/word/02-zapis-z-porady.json", import.meta.url)
    const legacy = JSON.parse(await readFile(fixture, "utf8"))
    const heading = legacy.body[0]
    const table = legacy.body.find((block: { kind: string }) => block.kind === "table")
    expect(heading.props.style).toBe("Heading1")
    expect(table).toBeTruthy()
    const media = { "image1.png": "iVBORw0KGgoAAAANSUhEUgAAACAAAAAQCAYAAAB3AH1ZAAAAKElEQVR4nGMQibrznxJ8R0ODIsww6oBRB4w6YNQBow4YdcCoAwbaAQCfenQ9EOdSfAAAAABJRU5ErkJggg==" }
    legacy.media = media
    legacy.body.push({ kind: "para", text: "\uFFFC", runs: [{ len: 3 }], objects: [{ type: "image", media: "image1.png", w: 32, h: 16, alt: "Synthetic picture" }] })

    const original = Buffer.from(JSON.stringify(legacy))
    const ref = await N.createNative(api, WORKSPACE, "wordcraft", "Retained Word profile", original)
    expect((await N.readHead(api, ref)).hash).toBe(N.sha256(original))
    const page = await open(browser, ref, log)
    const frame = await N.acquire(page, "wordcraft")
    expect(frame.url()).toContain(`/b/${oldBundle}/index.html`)
    const previewURL = `/api/v1/pages/${encodeURIComponent("@" + ref.pageId)}/craft/preview?workspace_id=${encodeURIComponent(WORKSPACE)}`
    await page.getByRole("button", { name: "Publish preview" }).click()
    await expect.poll(async () => (await api.get(previewURL)).status(), { timeout: 30_000 }).toBe(200)

    const install = await api.post("/api/v1/admin/instance/artifacts/applications/wordcraft/install", {
      data: await readFile(nextArchive), headers: { "Content-Type": "application/octet-stream" }, timeout: 120_000,
    })
    expect(install.status(), (await install.text()).slice(0, 400)).toBe(200)
    expect(frame.url()).toContain(`/b/${oldBundle}/index.html`)
    const marker = "edit-through-retained-word-profile"
    await frame.locator("canvas").first().click({ position: { x: 520, y: 420 } })
    await N.typeMarker(page, frame, "wordcraft", marker, "B2")
    await expect.poll(async () => edited(await N.status(page), ref.initVersion), { timeout: SAVE_MS }).toBe(true)
    await page.keyboard.press("Control+s")
    const saved = await N.waitSaved(page, SAVE_MS, ref.initVersion)
    expect(saved).toBeTruthy()
    await N.release(page)
    await page.context().close()

    const again = await open(browser, ref, log)
    expect((await N.frameOf(again, "wordcraft")).url()).toContain(`/b/${oldBundle}/index.html`)
    const head = await N.readHead(api, ref)
    expect(head.version).toBe(saved)
    expect(holds(head.text, marker)).toBe(true)
    const current = JSON.parse(head.text)
    expect(current.body[0]).toEqual(heading)
    expect(current.body.find((block: { kind: string }) => block.kind === "table")).toEqual(table)
    expect(current.media).toEqual(media)
    const shared = await api.get(previewURL)
    expect(shared.status()).toBe(200)
    const pinned = await shared.json() as { version: string; bytes: string; editorBundle: string }
    expect(pinned.version).toBe(ref.initVersion)
    expect(pinned.editorBundle).toBe(oldBundle)
    expect(N.sha256(Buffer.from(pinned.bytes, "base64"))).toBe(N.sha256(original))
    await again.context().close()
  } finally {
    await info.attach("console", { body: log.join("\n") || "(none)", contentType: "text/plain" })
    await api.dispose()
  }
})

for (const app of ["wordcraft", "gridcraft"] as const) {
  test(`${N.APP_LABEL[app]} package: open, edit, save, reopen`, async ({ browser }, info) => {
    test.setTimeout(6 * 60_000)
    const log: string[] = []
    const api = await apiAs(ORIGIN, owner)
    try {
      const ref = await N.createNative(api, WORKSPACE, app, `Package gate ${N.APP_LABEL[app]}`, app === "gridcraft" ? emptyGridCraftWorkbook() : undefined)
      const first = `gate-${app}-one-${Date.now().toString(36)}`, second = `gate-${app}-two-${Date.now().toString(36)}`

      const page = await open(browser, ref, log)
      if (app === "wordcraft" && process.env.CRAFT_GATE_RETAINED_WORD_BUNDLE) {
        expect((await N.frameOf(page, app)).url()).not.toContain(process.env.CRAFT_GATE_RETAINED_WORD_BUNDLE)
      }
      const v1 = await editAndSave(page, app, first, "B2", ref.initVersion)
      const head1 = await N.readHead(api, ref)
      expect(head1.version).toBe(v1)
      await expectMarker(page, app, head1, "B2", first)
      await page.context().close()

      // Reopen in a fresh browser context: a new frame, a new bootstrap from the saved version.
      const again = await open(browser, ref, log)
      const v2 = await editAndSave(again, app, second, "B3", v1)
      const head2 = await N.readHead(api, ref)
      expect(head2.version).toBe(v2)
      await expectMarker(again, app, head2, "B2", first)
      await expectMarker(again, app, head2, "B3", second)
      await again.context().close()
    } finally {
      await info.attach("console", { body: log.join("\n") || "(none)", contentType: "text/plain" })
      await api.dispose()
    }
  })
}

test("Legacy Sheet: convert nonempty JSON, edit, reload and download the original", async ({ browser }, info) => {
  const database = process.env.CRAFT_GATE_LEGACY_DB
  if (!database || new URL(ORIGIN).hostname !== "127.0.0.1") {
    throw new Error("the legacy fixture requires the disposable Craft browser gate server")
  }
  const log: string[] = []
  const api = await apiAs(ORIGIN, owner)
  try {
    const created = await api.post(`/api/v1/pages?workspace_id=${encodeURIComponent(WORKSPACE)}`, {
      data: { name: "Legacy package gate Sheet", slug: `craft-gate-legacy-${Date.now()}`, panels: [], allow_empty: true, created_by_test: true },
    })
    expect(created.status(), `create legacy fixture Page: ${(await created.text()).slice(0, 300)}`).toBe(201)
    const { id: pageId } = await created.json() as { id: string }
    const ref = { pageId, workspaceId: WORKSPACE, initVersion: "1" }

    const legacyMarker = "legacy-cell-before-conversion"
    const editMarker = "edited-after-conversion"
    const reloadMarker = "edited-after-reload"
    const legacy = JSON.parse(new TextDecoder().decode(emptyGridCraftNativeDocument()))
    legacy.sheets[0].cells = {
      bands: { 0: { rows: { 1: { 1: { value: { t: "Text", v: legacyMarker }, formula: null, style: 0 } } } } },
      len: 1,
    }
    const original = Buffer.from(JSON.stringify(legacy, null, 2) + "\n")
    execFileSync("python3", [
      fileURLToPath(new URL("./seed-legacy-sheet.py", import.meta.url)), database, WORKSPACE, pageId,
    ], { input: original, timeout: 15_000 })

    const page = await open(browser, ref, log)
    const converted = await N.readHead(api, ref)
    expect(converted.version).toBe("2")
    await expectMarker(page, "gridcraft", converted, "B2", legacyMarker)
    const saved = await editAndSave(page, "gridcraft", editMarker, "B3", converted.version)
    const editedHead = await N.readHead(api, ref)
    expect(editedHead.version).toBe(saved)
    await expectMarker(page, "gridcraft", editedHead, "B2", legacyMarker)
    await expectMarker(page, "gridcraft", editedHead, "B3", editMarker)
    await page.context().close()

    const reopened = await open(browser, ref, log)
    const final = await editAndSave(reopened, "gridcraft", reloadMarker, "B4", saved)
    const head = await N.readHead(api, ref)
    expect(head.version).toBe(final)
    await expectMarker(reopened, "gridcraft", head, "B2", legacyMarker)
    await expectMarker(reopened, "gridcraft", head, "B3", editMarker)
    await expectMarker(reopened, "gridcraft", head, "B4", reloadMarker)

    const [download] = await Promise.all([
      reopened.waitForEvent("download", { timeout: 30_000 }),
      reopened.getByRole("button", { name: "Download original Sheet" }).click(),
    ])
    expect(download.suggestedFilename()).toBe("sheet-original.json")
    const actual = await readFile(await download.path())
    expect(actual.equals(original), "the downloaded JSON matches the original byte for byte").toBe(true)
    await reopened.context().close()
  } finally {
    await info.attach("console", { body: log.join("\n") || "(none)", contentType: "text/plain" })
    await api.dispose()
  }
})
