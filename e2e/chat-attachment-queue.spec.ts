import { test, expect, type Locator, type Page, type Route } from "@playwright/test"

// Chat attachment queue, against a REAL backend.
//
// Only the upload request is intercepted, and only to (a) hold it open so the
// waiting/uploading states can be observed and (b) force one named file to
// fail. Every other request - login, agents, chat creation, the message send,
// the reload that proves persistence - goes to the real server, and every
// upload that is not forced to fail is continued to it and stored by it.
//
// Serial: the tests share one seeded agent and one browser profile.
test.describe.configure({ mode: "serial" })

const TIMEOUT = 20_000
// Each flow uploads, sends and reloads against a real server.
test.setTimeout(120_000)

interface Gate {
  /** Conversation id taken from the upload URL. */
  chatId: string
  /** Every upload request seen, by file name, in the order it was issued. */
  seen: string[]
  /** Hold the next request for `name` until `release(name)` is called. */
  hold(name: string): void
  release(name: string): void
  /** Answer requests for `name` with a 500 instead of continuing them. */
  failing: Set<string>
}

function fileNameOf(body: Buffer | null): string {
  const m = /filename="([^"]+)"/.exec(body?.toString("latin1") ?? "")
  return m?.[1] ?? "?"
}

/** Intercepts ONLY the attachment upload POST. */
async function gateUploads(page: Page): Promise<Gate> {
  const held = new Map<string, Promise<void>>()
  const releasers = new Map<string, () => void>()
  const gate: Gate = {
    chatId: "",
    seen: [],
    failing: new Set(),
    hold(name) {
      held.set(name, new Promise<void>((res) => releasers.set(name, res)))
    },
    release(name) {
      releasers.get(name)?.()
    },
  }
  await page.route(/\/api\/v1\/agents\/[^/]+\/chats\/[^/]+\/attachments\?/, async (route: Route) => {
    const req = route.request()
    if (req.method() !== "POST") return route.continue()
    const name = fileNameOf(req.postDataBuffer())
    gate.chatId = /\/chats\/([^/?]+)\/attachments/.exec(req.url())?.[1] ?? gate.chatId
    gate.seen.push(name)
    await held.get(name)
    if (gate.failing.has(name)) {
      return route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "forced failure (e2e)" }),
      })
    }
    return route.continue()
  })
  return gate
}

function file(name: string, body = name) {
  return { name, mimeType: "text/plain", buffer: Buffer.from(`${body}\n`) }
}

/** The nth agent with a slug. Each flow that sends uses its own agent: a send
 *  to a conversation whose agent is still running an earlier turn is queued
 *  behind it, which is not what these tests are about. */
async function agentSlug(page: Page, nth: number): Promise<string> {
  await page.goto("/")
  const slug = await page.evaluate(async (nth) => {
    const workspaces = await (await fetch("/api/v1/workspaces")).json()
    for (const ws of Array.isArray(workspaces) ? workspaces : []) {
      const agents = await (await fetch(`/api/v1/agents?workspace_id=${ws.id}`)).json()
      const list = (Array.isArray(agents) ? agents : []).filter((x: { slug?: string }) => x?.slug)
      if (list.length > nth) return list[nth].slug as string
    }
    return null
  }, nth)
  expect(slug, "no agent with a slug on this instance - seed one first").not.toBeNull()
  return slug!
}

async function openChat(page: Page, slug: string) {
  await page.goto(`/chat/${slug}`)
  await expect(page.getByPlaceholder(/^Message /)).toBeVisible({ timeout: TIMEOUT })
}

/** Through the paperclip, as a person does: its click opens the file chooser. */
async function pick(page: Page, files: ReturnType<typeof file>[]) {
  const chooser = page.waitForEvent("filechooser")
  await page.getByRole("button", { name: "Attach files" }).click()
  await (await chooser).setFiles(files)
}
const chip = (page: Page, name: string) => page.locator("[data-status]", { hasText: name })
const sendButton = (page: Page) => page.locator('button[type="submit"]').last()
/** Text on the page that is not the composer's own textarea. */
const shown = (page: Page, text: string) => page.getByText(text).locator("xpath=self::*[not(self::textarea)]")
const notice = (page: Page) => page.getByRole("note")

async function noHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow).toBeLessThanOrEqual(0)
}

/** The failure-then-retry-then-send flow; shared by desktop and phone. */
async function failRetrySendReload(page: Page, tag: string) {
  const a = `${tag}-a.txt`
  const bad = `${tag}-bad.txt`
  const c = `${tag}-c.txt`
  const gate = await gateUploads(page)
  gate.failing.add(bad)
  gate.hold(a)

  await pick(page, [file(a), file(bad), file(c)])

  // a is on the wire (held), the other two have not been sent yet.
  await expect(chip(page, a)).toHaveAttribute("data-status", "uploading", { timeout: TIMEOUT })
  await expect(chip(page, bad)).toHaveAttribute("data-status", "validating")
  await expect(chip(page, c)).toHaveAttribute("data-status", "validating")
  // (the draft conversation is created first, so the request follows the chip)
  await expect.poll(() => gate.seen, { timeout: TIMEOUT }).toEqual([a])

  gate.release(a)
  await expect(chip(page, a)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
  await expect(chip(page, bad)).toHaveAttribute("data-status", "error", { timeout: TIMEOUT })
  await expect(chip(page, c)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
  // One at a time, in the order they were added.
  expect(gate.seen).toEqual([a, bad, c])

  // The failed file blocks Send and says why on screen.
  const text = page.getByPlaceholder(/^Message /)
  await text.fill(`files from ${tag}`)
  await expect(sendButton(page)).toHaveAttribute("aria-disabled", "true")
  await expect(notice(page)).toBeVisible()
  await expect(notice(page)).toContainText(`${bad} is not attached`)
  await noHorizontalScroll(page)
  // aria-disabled, not disabled: the button is still clickable, and refuses.
  await sendButton(page).click({ force: true })
  // Blocked attempt: nothing was sent, the draft is kept.
  await expect(text).toHaveValue(`files from ${tag}`)
  await expect(shown(page, `files from ${tag}`)).toHaveCount(0)

  // Retry succeeds (the real server this time).
  gate.failing.delete(bad)
  await page.getByRole("button", { name: `Retry ${bad}` }).click()
  await expect(chip(page, bad)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
  expect(gate.seen.filter((n) => n === bad)).toHaveLength(2)
  await expect(notice(page)).toHaveCount(0)
  await expect(sendButton(page)).not.toHaveAttribute("aria-disabled", "true")

  await sendButton(page).click()
  const sent = shown(page, `files from ${tag}`).first()
  await expect(sent).toBeVisible({ timeout: TIMEOUT })
  for (const n of [a, bad, c]) await expect(shown(page, n).first()).toBeVisible()

  // The server has the message (asked of the real API, not the screen).
  await expect
    .poll(
      () =>
        page.evaluate(async (id) => {
          const ws = await (await fetch("/api/v1/workspaces")).json()
          const r = await fetch(`/api/v1/chats/${id}/messages?workspace_id=${ws[0].id}`)
          return r.ok ? JSON.stringify(await r.json()) : `HTTP ${r.status}`
        }, gate.chatId),
      { timeout: TIMEOUT, message: "message never reached the server" },
    )
    .toContain(`files from ${tag}`)

  // Persisted by the server: still there after a reload.
  await page.reload()
  // The list opens at the newest message and renders only what is near the
  // viewport, so on a phone the typed text is above the fold: scroll up to it.
  const vp = page.viewportSize()!
  await page.mouse.move(vp.width / 2, vp.height / 3)
  await expect
    .poll(
      async () => {
        if ((await shown(page, `files from ${tag}`).count()) === 0) await page.mouse.wheel(0, -500)
        return shown(page, `files from ${tag}`).count()
      },
      { timeout: TIMEOUT },
    )
    .toBeGreaterThan(0)
  await expect(shown(page, `files from ${tag}`).first()).toBeVisible()
  for (const n of [a, bad, c]) await expect(shown(page, n).first()).toBeAttached()
  return gate
}

test.describe("chat attachment queue (real backend)", () => {
  let slugs: string[]
  test.beforeAll(async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: test.info().project.use.storageState as string })
    const page = await ctx.newPage()
    slugs = [await agentSlug(page, 0), await agentSlug(page, 1), await agentSlug(page, 2)]
    await ctx.close()
  })

  test("desktop: queue, forced failure blocks Send, Retry, send, persists across reload", async ({ page }) => {
    await openChat(page, slugs[0])
    await failRetrySendReload(page, `d${Date.now().toString(36)}`)
  })

  test("drop and paste join the same queue, in order", async ({ page }) => {
    await openChat(page, slugs[2])
    const tag = `q${Date.now().toString(36)}`
    const first = `${tag}-first.txt`
    const dropped = `${tag}-dropped.txt`
    const pasted = `${tag}-pasted.txt`
    const gate = await gateUploads(page)
    gate.hold(first)
    await pick(page, [file(first)])
    await expect(chip(page, first)).toHaveAttribute("data-status", "uploading", { timeout: TIMEOUT })

    const textarea = page.getByPlaceholder(/^Message /)
    await dispatchFiles(page, textarea, "drop", [dropped])
    await dispatchFiles(page, textarea, "paste", [pasted])
    await expect(chip(page, dropped)).toHaveAttribute("data-status", "validating", { timeout: TIMEOUT })
    await expect(chip(page, pasted)).toHaveAttribute("data-status", "validating", { timeout: TIMEOUT })
    await expect.poll(() => gate.seen, { timeout: TIMEOUT }).toEqual([first])

    gate.release(first)
    for (const n of [first, dropped, pasted]) {
      await expect(chip(page, n)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
    }
    expect(gate.seen).toEqual([first, dropped, pasted])
  })

  test("Cancel then Retry uploads once", async ({ page }) => {
    await openChat(page, slugs[2])
    const tag = `c${Date.now().toString(36)}`
    const first = `${tag}-first.txt`
    const second = `${tag}-second.txt`
    const gate = await gateUploads(page)
    gate.hold(first)
    await pick(page, [file(first), file(second)])
    await expect(chip(page, first)).toHaveAttribute("data-status", "uploading", { timeout: TIMEOUT })
    await expect(chip(page, second)).toHaveAttribute("data-status", "validating")

    await page.getByRole("button", { name: `Cancel ${second}` }).click()
    await expect(chip(page, second)).toHaveAttribute("data-status", "cancelled")
    // Cancelled keeps Send off with the reason on screen.
    await page.getByPlaceholder(/^Message /).fill("cancel check")
    await expect(sendButton(page)).toHaveAttribute("aria-disabled", "true")
    await expect(notice(page)).toContainText(`${second} is not attached`)

    await page.getByRole("button", { name: `Retry ${second}` }).click()
    gate.release(first)
    await expect(chip(page, first)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
    await expect(chip(page, second)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
    // The cancelled attempt never reached the network; the retry did, once.
    expect(gate.seen.filter((n) => n === second)).toHaveLength(1)
    expect(gate.seen.filter((n) => n === first)).toHaveLength(1)
    await expect(sendButton(page)).not.toHaveAttribute("aria-disabled", "true")
  })

  test("phone width: failure notice, Retry and send fit and work", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await openChat(page, slugs[1])
    await failRetrySendReload(page, `p${Date.now().toString(36)}`)
    await noHorizontalScroll(page)
  })

  test("phone: switching to Artifacts and back mid-upload does not cancel it", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await openChat(page, slugs[2])
    const tag = `t${Date.now().toString(36)}`
    const first = `${tag}-first.txt`
    const second = `${tag}-second.txt`
    const gate = await gateUploads(page)
    gate.hold(first)
    await pick(page, [file(first), file(second)])
    await expect(chip(page, first)).toHaveAttribute("data-status", "uploading", { timeout: TIMEOUT })
    await expect(chip(page, second)).toHaveAttribute("data-status", "validating")
    await expect.poll(() => gate.seen, { timeout: TIMEOUT }).toEqual([first])

    // The composer unmounts on the Artifacts tab and mounts again on Chat.
    const tabs = page.getByRole("tablist", { name: "Chat panel" })
    await tabs.getByRole("tab", { name: "Artifacts" }).click()
    await expect(page.getByPlaceholder(/^Message /)).toHaveCount(0)
    await tabs.getByRole("tab", { name: "Work" }).click()
    await tabs.getByRole("tab", { name: "Chat" }).click()
    await expect(page.getByPlaceholder(/^Message /)).toBeVisible({ timeout: TIMEOUT })

    // Nothing was cancelled: the held upload is still running, the second waits.
    await expect(chip(page, first)).toHaveAttribute("data-status", "uploading")
    await expect(chip(page, second)).toHaveAttribute("data-status", "validating")
    gate.release(first)
    await expect(chip(page, first)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
    await expect(chip(page, second)).toHaveAttribute("data-status", "ready", { timeout: TIMEOUT })
    expect(gate.seen).toEqual([first, second])
    // Cancelled chips would have blocked Send.
    await page.getByPlaceholder(/^Message /).fill("tab switch check")
    await expect(sendButton(page)).not.toHaveAttribute("aria-disabled", "true")
    await expect(notice(page)).toHaveCount(0)
    await noHorizontalScroll(page)
  })
})

/** Synthesize a drop or paste carrying real File objects. */
async function dispatchFiles(page: Page, target: Locator, kind: "drop" | "paste", names: string[]) {
  const handle = await target.elementHandle()
  await page.evaluate(
    ({ el, kind, names }) => {
      const dt = new DataTransfer()
      for (const n of names) dt.items.add(new File([`${n}\n`], n, { type: "text/plain" }))
      if (kind === "paste") {
        el!.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }))
      } else {
        for (const type of ["dragenter", "dragover", "drop"]) {
          el!.dispatchEvent(new DragEvent(type, { dataTransfer: dt, bubbles: true, cancelable: true }))
        }
      }
    },
    { el: handle, kind, names },
  )
}
