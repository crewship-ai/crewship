// @vitest-environment node
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium, type Page } from "@playwright/test"
import { installLocalDraftKeyFixture } from "./local-draft-seal.test-support"
import { expect, it } from "vitest"

// G4 #4682: a WordCraft storage_full stop merges its unconfirmed changes into
// the recovery draft. The merge is admitted when it is called; an identity
// change that begins while it waits must not lose it, must not hand it to the
// next person, and a late result must not overwrite a newer draft.

const executable = chromium.executablePath()
type Api = typeof import("./artifact-drafts") & Pick<typeof import("./local-draft-seal"), "beginLocalDraftIdentity" | "finishLocalDraftIdentity">
type W = Window & { artifactDrafts: Api; pending?: Promise<string>; release?: () => void }

async function tab(page: Page, human: () => string) {
  await installLocalDraftKeyFixture(page, { human })
  await page.goto("http://localhost/drafts")
  // One bundle, so the identity transition and the draft lanes share the module state of a real page.
  const code = buildSync({ stdin: { contents: 'export * from "./artifact-drafts"; export { beginLocalDraftIdentity, finishLocalDraftIdentity } from "./local-draft-seal"', resolveDir: `${process.cwd()}/lib`, loader: "ts" }, bundle: true, write: false, format: "iife", globalName: "artifactDrafts", platform: "browser" }).outputFiles[0].text
  await page.addScriptTag({ content: code })
}

it.skipIf(!existsSync(executable))("an identity change while a stop's merge waits keeps the changes for their owner only, and a late merge never drops a newer one", async () => {
  const browser = await chromium.launch({ headless: true })
  try {
    const context = await browser.newContext()
    await context.route("http://localhost/**", route => route.fulfill({ body: "<!doctype html><title>Drafts</title>", contentType: "text/html" }))
    let humanA = "alice"
    const a = await context.newPage(), b = await context.newPage()
    await tab(a, () => humanA)
    await tab(b, () => "alice")
    const key = JSON.stringify(["alice", "wordcraft-pending", "w", "page_1"])

    // Tab A opened the editor: the recovery draft holds A and its cipher is admitted (mount-time load).
    await a.evaluate(async ({ key, first }) => {
      const api = (window as unknown as W).artifactDrafts
      await api.writeArtifactDraft(key, first)
      await api.loadArtifactDraft(key)
    }, { key, first: JSON.stringify(["A"]) })
    await b.evaluate(async key => { await (window as unknown as W).artifactDrafts.loadArtifactDraft(key) }, key)

    // Tab B holds the cross-tab draft lock, so A's merge is admitted but waits.
    await b.evaluate(name => new Promise<void>(held => {
      void navigator.locks.request(name, () => new Promise<void>(release => { (window as unknown as W).release = release; held() }))
    }), `crewship-artifact-draft:${key}`)
    await a.evaluate(({ key }) => {
      const w = window as unknown as W
      w.pending = w.artifactDrafts.mergeArtifactDraft(key, existing => JSON.stringify([...(existing ? JSON.parse(existing) as string[] : []), "B (unconfirmed, stopped)"]))
      void w.pending.catch(() => {})
    }, { key })

    // While A's merge waits, another person signs in on tab A.
    humanA = "bob"
    // Like hooks/use-auth preserveDraftsForIdentity: plaintext reads are blocked first, then lanes drain.
    const suspended = a.evaluate(() => { const api = (window as unknown as W).artifactDrafts; api.beginLocalDraftIdentity("bob"); return api.suspendOtherArtifactDrafts("bob") })
    // A newer change from tab B lands first, under the lock B holds.
    await b.evaluate(async ({ key }) => {
      const w = window as unknown as W
      const later = w.artifactDrafts.mergeArtifactDraft(key, existing => JSON.stringify([...(existing ? JSON.parse(existing) as string[] : []), "C (newer, tab B)"]))
      w.release!()
      await later
    }, { key })
    const merged = await a.evaluate(async () => {
      try { return { ok: true, text: await (window as unknown as W).pending! } } catch (error) { return { ok: false, text: String(error) } }
    })
    await suspended

    // The stop's merge was stored (not reported as saved when it was not).
    expect(merged.ok).toBe(true)
    // The next person on tab A sees nothing of alice's draft.
    const seenByBob = await a.evaluate(async key => (window as unknown as W).artifactDrafts.readArtifactDraft(key), key)
    expect(seenByBob).toBe("")
    // The retired caller is not handed alice's merged plaintext either.
    expect(merged.text).toBe("")
    // The owner finds every change, the late merge kept the newer one.
    const owner = JSON.parse(await b.evaluate(async key => (window as unknown as W).artifactDrafts.loadArtifactDraft(key), key)) as string[]
    expect(owner.sort()).toEqual(["A", "B (unconfirmed, stopped)", "C (newer, tab B)"].sort())
  } finally { await browser.close() }
}, 60000)

it.skipIf(!existsSync(executable))("a merge that cannot be stored rejects instead of reporting a saved draft", async () => {
  const browser = await chromium.launch({ headless: true })
  try {
    const context = await browser.newContext()
    await context.route("http://localhost/**", route => route.fulfill({ body: "<!doctype html><title>Drafts</title>", contentType: "text/html" }))
    const page = await context.newPage()
    // The server already answers for another person: no draft key for alice.
    await tab(page, () => "bob")
    const result = await page.evaluate(async () => {
      const api = (window as unknown as W).artifactDrafts
      // A missing function must not pass as the expected rejection.
      if (typeof api.mergeArtifactDraft !== "function") return "missing"
      const key = JSON.stringify(["alice", "wordcraft-pending", "w", "page_2"])
      try { await api.mergeArtifactDraft(key, () => JSON.stringify(["B"])); return "stored" } catch { return "rejected" }
    })
    expect(result).toBe("rejected")
  } finally { await browser.close() }
}, 60000)

it.skipIf(!existsSync(executable))("G4 r4 blocker 1: an identity change while a stop's merge waits keeps the stop's originals for their owner", async () => {
  const browser = await chromium.launch({ headless: true })
  try {
    const context = await browser.newContext()
    await context.route("http://localhost/**", route => route.fulfill({ body: "<!doctype html><title>Drafts</title>", contentType: "text/html" }))
    let human = "alice"
    const page = await context.newPage(), owner = await context.newPage()
    await tab(page, () => human)
    await tab(owner, () => "alice")
    const key = JSON.stringify(["alice", "wordcraft-pending", "w", "page_3"])
    // The editor opened: an earlier stop's recovery draft [A] is loaded (cipher admitted).
    await page.evaluate(async key => {
      const api = (window as unknown as W).artifactDrafts
      await api.writeArtifactDraft(key, JSON.stringify(["A"]))
      await api.loadArtifactDraft(key)
    }, key)
    // The stop's merge is admitted while its execution is held behind the cross-tab draft lock.
    await page.evaluate(({ key }) => new Promise<void>(held => {
      const w = window as unknown as W
      void navigator.locks.request(`crewship-artifact-draft:${key}`, () => new Promise<void>(release => { w.release = release; held() }))
      w.pending = w.artifactDrafts.mergeArtifactDraft(key, existing => JSON.stringify([...(existing ? JSON.parse(existing) as string[] : []), "B (stopped)"]))
      void w.pending.catch(() => {})
    }), { key })
    // Another person signs in on this tab, exactly as hooks/use-auth does it.
    human = "bob"
    const suspended = page.evaluate(() => { const api = (window as unknown as W).artifactDrafts; api.beginLocalDraftIdentity("bob"); return api.suspendOtherArtifactDrafts("bob") })
    await page.evaluate(() => (window as unknown as W).release!())
    const merged = await page.evaluate(async () => {
      try { return { ok: true, text: await (window as unknown as W).pending! } } catch (error) { return { ok: false, text: String(error) } }
    })
    await suspended
    await page.evaluate(() => (window as unknown as W).artifactDrafts.finishLocalDraftIdentity("bob"))
    expect(merged).toEqual({ ok: true, text: "" })
    expect(await page.evaluate(async key => (window as unknown as W).artifactDrafts.readArtifactDraft(key), key)).toBe("")
    expect(JSON.parse(await owner.evaluate(async key => (window as unknown as W).artifactDrafts.loadArtifactDraft(key), key))).toEqual(["A", "B (stopped)"])
  } finally { await browser.close() }
}, 60000)
