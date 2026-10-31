// PRD A: a full editor can be promised only after a round trip: open, edit,
// save natively, reload, reopen, with images, tables, comments and formatting.
//
// Full mode only. It drives the real WordCraft editor of the deployed origin
// in a browser (the editor is Czech; its tools are found by accessible name),
// saves with the editor's own save, then reloads the page and reopens the
// document with a fresh login, and checks the saved native state through the
// editor API. Needs the deployed native editor bundle.
import type { Frame, Page } from "@playwright/test"
import { apiAs, loginStorageState } from "./auth"
import { requireNative } from "./craft-lease/helpers"
import { NativePage } from "./craft-lease/native"
import * as native from "./drafts-crash/native"
import { expect, prdRequirement, test } from "./fixtures"

const REQ = prdRequirement("A.craft.roundtrip.rich", "Word: table, image, comment and formatting (bold, heading) survive edit, native save, reload and reopen")

// 1x1 PNG.
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==", "base64")

type Node = Record<string, unknown>

/** Every object in the native JSON, depth first. */
function* walk(value: unknown): Generator<Node> {
  if (Array.isArray(value)) for (const v of value) yield* walk(v)
  else if (value && typeof value === "object") {
    yield value as Node
    for (const v of Object.values(value)) yield* walk(v)
  }
}

interface RichFacts {
  heading: boolean
  bold: boolean
  table: boolean
  image: boolean
  comment: boolean
  text: boolean
}

/** What a reader of the saved native state would see; the markers are typed by this scenario. */
function richFacts(raw: string, m: { heading: string; bold: string; cell: string; comment: string }): RichFacts {
  const doc = JSON.parse(raw) as { body?: Node[]; comments?: unknown }
  const nodes = [...walk(doc.body ?? [])]
  const paras = nodes.filter((n) => n.kind === "para")
  return {
    heading: paras.some((p) => String(p.text ?? "").includes(m.heading) && /^Heading/.test(String((p.props as Node | undefined)?.style ?? ""))),
    bold: paras.some((p) => String(p.text ?? "").includes(m.bold) && ((p.runs as Node[] | undefined) ?? []).some((r) => (r.props as Node | undefined)?.bold === true)),
    table: nodes.some((n) => n.kind === "table") && nodes.some((n) => n.kind === "para" && String(n.text ?? "").includes(m.cell)),
    image: nodes.some((n) => /^(image|picture|drawing|inlineImage)$/i.test(String(n.kind ?? n.type ?? ""))) || /"(image|blip|drawing)[A-Za-z]*"/i.test(JSON.stringify(doc.body ?? [])),
    comment: JSON.stringify(doc.comments ?? {}).includes(m.comment),
    text: true,
  }
}

/** Finds an editor tool by accessible name; fails with the tools it did see. */
async function tool(frame: Frame, page: Page, name: RegExp, what: string) {
  for (const scope of [frame, page]) {
    for (const role of ["button", "menuitem", "tab"] as const) {
      const hit = scope.getByRole(role, { name }).first()
      if (await hit.count()) return hit
    }
  }
  const seen = await frame.getByRole("button").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label") || e.textContent || "").filter(Boolean)).catch(() => [])
  throw new Error(`the editor offers no "${what}" tool (${name}); buttons seen: ${JSON.stringify(seen).slice(0, 400)}`)
}

test.describe("Word rich round trip", () => {
  test("table, image, comment and formatting survive save, reload and reopen", { annotation: REQ }, async ({ prd, request }) => {
    prd.requireFull("needs the deployed native WordCraft editor bundle")
    test.setTimeout(10 * 60_000)
    const cleanup: Array<() => Promise<void>> = []
    try {
      const ws = await prd.qaWorkspace()
      const doc = await NativePage.create(request, ws.id, "wordcraft", "PRD rich round trip")
      await requireNative(prd, request, doc)
      const tag = Date.now().toString(36)
      const m = { heading: `PRDHEAD${tag}`, bold: `PRDBOLD${tag}`, cell: `PRDCELL${tag}`, comment: `PRDNOTE${tag}` }
      const log: Record<string, unknown> = { mode: prd.mode, appSha: prd.appSha, workspaceId: ws.id, pageId: doc.pageId }
      const owner = prd.env.accounts.owner
      expect(owner, "owner account").toBeTruthy()

      const context = await prd.browserContext(await loginStorageState(prd.origin, owner!))
      const page = await context.newPage()
      page.setDefaultTimeout(90_000)
      await native.openEditor(page, doc.pageId, ws.id)
      const frame = await native.acquire(page, "wordcraft")
      const kb = page.keyboard

      // Heading, then bold text.
      await kb.press("Control+End")
      await kb.type(m.heading)
      await (await tool(frame, page, /Nadpis 1|Heading 1/i, "heading")).click()
      await kb.press("Enter")
      await kb.press("Control+b")
      await kb.type(m.bold)
      await kb.press("Control+b")
      await kb.press("Enter")

      // Table.
      await (await tool(frame, page, /Tabulka|Table/i, "table")).click()
      const insertTable = frame.getByRole("menuitem", { name: /Vložit tabulku|Insert table|2\s*[x×]\s*2/i }).first()
      if (await insertTable.count()) await insertTable.click()
      await kb.type(m.cell)

      // Image.
      const chooser = page.waitForEvent("filechooser", { timeout: 30_000 })
      await (await tool(frame, page, /Obrázek|Image|Picture/i, "image")).click()
      await (await chooser).setFiles({ name: "prd.png", mimeType: "image/png", buffer: PNG })

      // Comment on the bold text.
      await kb.press("Control+Home")
      await kb.press("Control+Shift+End")
      await (await tool(frame, page, /Komentář|Comment/i, "comment")).click()
      await kb.type(m.comment)
      await (await tool(frame, page, /Přidat|Odeslat|Add|Post|Save/i, "post comment")).click()

      const saved = page.waitForResponse((r) => r.url().includes("/document/save") && r.request().method() === "POST" && r.status() === 200, { timeout: 60_000 })
      await kb.press("Control+s")
      await saved
      const version = await native.waitSaved(page)
      await prd.evidence("editor-after-save.png", await page.screenshot({ fullPage: true }))

      // Reload the same editor page: the server head is what comes back.
      await page.reload()
      await native.frameOf(page, "wordcraft")
      const afterReload = await native.readNative(request, ws.id, doc.pageId)
      expect(afterReload.version).toBe(version)
      const reloaded = richFacts(afterReload.raw, m)

      // Reopen with a fresh login.
      const reopenApi = await apiAs(prd.origin, await loginStorageState(prd.origin, owner!))
      cleanup.push(() => reopenApi.dispose())
      const reopened = await native.readNative(reopenApi, ws.id, doc.pageId)
      const facts = richFacts(reopened.raw, m)
      log.result = { savedVersion: version, reopenedVersion: reopened.version, afterReload: reloaded, afterReopen: facts }
      await prd.evidence("result.json", log)

      expect(reopened.version, "reopen sees the saved version").toBe(version)
      expect(reopened.hash, "reopen reads the same bytes as the reload").toBe(afterReload.hash)
      expect(facts, "everything inserted in the editor is in the reopened document").toEqual({ heading: true, bold: true, table: true, image: true, comment: true, text: true })
    } finally {
      for (const c of cleanup.reverse()) await c().catch(() => {})
    }
  })
})
