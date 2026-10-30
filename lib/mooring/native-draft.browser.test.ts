// @vitest-environment node
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { installLocalDraftKeyFixture } from "../local-draft-seal.test-support"
import { expect, it } from "vitest"

it.skipIf(!existsSync(chromium.executablePath()))("roundtrips a 5 MiB native recovery through real Chromium IndexedDB and excludes another tab", async () => {
  const browser = await chromium.launch({ headless: true })
  try {
    const context = await browser.newContext()
    await context.route("http://localhost/**", route => route.fulfill({ body: "<!doctype html><title>Native recovery journal</title>", contentType: "text/html" }))
    const a = await context.newPage(), b = await context.newPage()
    const code = buildSync({ entryPoints: [`${process.cwd()}/lib/mooring/native-draft.ts`], bundle: true, write: false, format: "iife", globalName: "nativeDraft", platform: "browser" }).outputFiles[0].text
    for (const page of [a, b]) { await installLocalDraftKeyFixture(page, {human: () => "alice"}); await page.goto("http://localhost/native-recovery"); await page.addScriptTag({ content: code }) }
    await a.evaluate(async () => {
      const api = (window as unknown as { nativeDraft: typeof import("./native-draft") }).nativeDraft
      const journal = await api.openNativeDraftJournal({ human: "alice", workspace: "w", page: "p", applicationId: "wordcraft" })
      const bytes = new Uint8Array(5 * 1024 * 1024).fill(73); bytes[0] = 0; bytes[1] = 255; bytes[bytes.length - 1] = 41
      await journal.persist({ applicationId: "wordcraft", enginePin: "fixture-engine", sessionId: "original-tab", generation: "7", baseVersion: "12", editorRevision: "doc:123:0", bytes })
      ;(window as unknown as { journal: typeof journal }).journal = journal
    })
    const refused = await b.evaluate(async () => {
      const api = (window as unknown as { nativeDraft: typeof import("./native-draft") }).nativeDraft
      try { await api.openNativeDraftJournal({ human: "alice", workspace: "w", page: "p", applicationId: "wordcraft" }); return false } catch { return true }
    })
    expect(refused).toBe(true)
    await a.evaluate(async () => (window as unknown as { journal: import("./native-draft").NativeDraftJournal }).journal.close())
    const result = await b.evaluate(async () => {
      const api = (window as unknown as { nativeDraft: typeof import("./native-draft") }).nativeDraft
      const scope = { human: "alice", workspace: "w", page: "p", applicationId: "wordcraft" }
      const journal = await api.openNativeDraftJournal(scope)
      const recovered = await journal.recovery()
      const draft = recovered[0].draft
      const expected = new Uint8Array(5 * 1024 * 1024).fill(73); expected[0] = 0; expected[1] = 255; expected[expected.length - 1] = 41
      const digest = async (bytes: Uint8Array) => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new Uint8Array(bytes)))).join(",")
      const same = await digest(expected) === await digest(draft.bytes)
      await journal.confirm(draft) // this mount cannot clear an orphan via an unrelated ACK
      const retained = (await journal.recovery()).length
      await journal.revokeRead()
      const empty = await api.openNativeDraftJournal(scope)
      const purged = (await empty.recovery()).length
      await empty.close()
      return { same, retained, purged, actions: recovered[0].actions, pin: draft.enginePin, generation: draft.generation, baseVersion: draft.baseVersion, editorRevision: draft.editorRevision, sessionId: draft.sessionId }
    })
    expect(result).toEqual({ same: true, retained: 1, purged: 0, actions: ["compare", "copy"], pin: "fixture-engine", generation: "7", baseVersion: "12", editorRevision: "doc:123:0", sessionId: "original-tab" })
  } finally { await browser.close() }
})
