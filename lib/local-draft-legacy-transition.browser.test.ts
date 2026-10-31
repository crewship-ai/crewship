// @vitest-environment node
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"
import { installLocalDraftKeyFixture } from "./local-draft-seal.test-support"

type API = typeof import("./artifact-drafts") & typeof import("./document-queue") & typeof import("./local-draft-seal")

it.skipIf(!existsSync(chromium.executablePath()))("removes legacy plaintext on expiry and account replacement while retaining sealed recovery rows", async () => {
  const browser = await chromium.launch({ headless: true })
  try {
    for (const next of [null, "bob"]) {
      const page = await browser.newPage()
      await page.route("https://legacy-draft.test/**", route => route.fulfill({ body: "<!doctype html>", contentType: "text/html" }))
      await installLocalDraftKeyFixture(page, { human: () => "alice" })
      await page.goto("https://legacy-draft.test/page")
      const bundle = buildSync({ stdin: { contents: 'export * from "./lib/artifact-drafts"; export * from "./lib/document-queue"; export * from "./lib/local-draft-seal";', resolveDir: process.cwd() }, bundle: true, write: false, platform: "browser", format: "iife", globalName: "draftAPI" })
      await page.addScriptTag({ content: bundle.outputFiles[0].text })
      const result = await page.evaluate(async nextHuman => {
        const api = (window as unknown as { draftAPI: API }).draftAPI
        const cipher = await api.createLocalDraftCipher({ human: "alice", workspace: "ws", page: "page" })
        const sealed = await cipher.seal("private retained copy", "test")
        const seed = (name: string, rows: unknown[]) => new Promise<void>((resolve, reject) => {
          const request = indexedDB.open(name, 1)
          request.onupgradeneeded = () => request.result.createObjectStore("drafts", { keyPath: "key" })
          request.onerror = () => reject(request.error)
          request.onsuccess = () => { const db = request.result, tx = db.transaction("drafts", "readwrite"); for (const row of rows) tx.objectStore("drafts").put(row); tx.onabort = () => reject(tx.error); tx.oncomplete = () => { db.close(); resolve() } }
        })
        const rows = (name: string) => new Promise<unknown[]>((resolve, reject) => {
          const request = indexedDB.open(name, 1)
          request.onerror = () => reject(request.error)
          request.onsuccess = () => { const db = request.result, tx = db.transaction("drafts", "readonly"), get = tx.objectStore("drafts").getAll(); tx.oncomplete = () => { db.close(); resolve(get.result) } }
        })
        const owner = JSON.stringify(["alice", "ws", "page"])
        await seed("crewship-document-drafts", [
          { key: [owner, "legacy"], draft: { id: "legacy", snapshot: "LEGACY DOCUMENT", update: "LEGACY UPDATE" } },
          { key: [owner, "sealed"], draft: { id: "sealed", snapshot: sealed, update: "" } },
        ])
        await seed("crewship-artifact-drafts", [
          { key: ["alice", "artifact-source", "ws", "old"], text: "LEGACY ARTIFACT" },
          { key: ["alice", "artifact-source", "ws", "sealed"], text: sealed },
        ])
        for (const prefix of ["crewship-design:v2:", "design-comment:v2:", "crewship-design:", "artifact-comment:"]) localStorage.setItem(prefix + "alice:ws:page", "LEGACY LOCAL")
        localStorage.setItem("design-comment:v2:alice:ws:sealed", sealed)
        localStorage.setItem("unrelated-setting", "keep")
        await api.suspendOtherDocumentDrafts(nextHuman)
        await api.suspendOtherArtifactDrafts(nextHuman)
        return { documents: await rows("crewship-document-drafts"), artifacts: await rows("crewship-artifact-drafts"), local: { ...localStorage }, sealed }
      }, next)
      expect(result.documents).toHaveLength(1)
      expect(result.artifacts).toHaveLength(1)
      expect(JSON.stringify(result)).not.toContain("LEGACY")
      expect(result.local).toEqual({ "design-comment:v2:alice:ws:sealed": result.sealed, "unrelated-setting": "keep" })
      await page.close()
    }
  } finally { await browser.close() }
}, 60_000)
