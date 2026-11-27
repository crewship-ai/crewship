// When the Sheet from an attached workbook is not created, the message does not
// leave on its own. It stays in the composer, the
// attachment row shows the error with "Try again" and "Send as a plain file", and
// only the person's explicit choice sends the workbook as a file. The same holds
// after the 10 s send hold while the Sheet is still opening: the composer keeps
// waiting with "Opening the Sheet…" and a way to cancel, and never sends the file.
//
// The failure is made in the browser (a Playwright route on craft/initialize):
// STUB, so the test does not depend on a server bug. Everything else is real.
import { expect, test, type Page } from "@playwright/test"
import * as H from "./helpers"
import { twoSheetWorkbook } from "./workbooks"

const INITIALIZE = /\/craft\/initialize(\?|$)/

function sentMessages(page: Page): string[] {
  const sent: string[] = []
  page.on("websocket", ws => ws.on("framesent", frame => {
    const text = typeof frame.payload === "string" ? frame.payload : frame.payload.toString("utf8")
    if (text.includes('"send_message"')) sent.push(text.slice(0, 600))
  }))
  return sent
}

test("a Sheet that could not be created keeps the message in the composer until the person chooses", async ({ browser }) => {
  H.requireAgent()
  test.setTimeout(8 * 60_000)
  const book = twoSheetWorkbook(1, 2, "selhany-sheet-syntetika.xlsx")
  const ctx = await H.clientContext(browser)
  const page = await ctx.newPage()
  const sent = sentMessages(page)
  try {
    // STUB: the server's answer to initialize is a refusal.
    await page.route(INITIALIZE, route => route.fulfill({ status: 403, contentType: "application/json", body: JSON.stringify({ error: "access", code: "access" }) }))
    await H.newChat(page)
    await H.attach(page, book.name, book.bytes)
    const field = page.getByPlaceholder(/^Message /)
    const words = `Podívej se na ${book.name}`
    await field.fill(words)
    await field.press("Enter")

    const row = page.getByRole("group", { name: `${book.name} as a Sheet` })
    // "Try again" is labelled for what it does.
    const retry = row.getByRole("button", { name: `Open ${book.name} as a Sheet` })
    await expect(retry, "the row offers Try again").toBeVisible({ timeout: 60_000 })
    await expect(retry).toHaveText(/Try again/)
    await expect(row.getByRole("alert"), "the row says what went wrong").toBeVisible()
    await expect.soft(page.locator("[data-sonner-toast]").filter({ hasText: "The Sheet could not be created, so the message was not sent." }), "the toast says the message was not sent").toBeVisible()
    await page.waitForTimeout(12_000) // past the 10 s send hold
    await H.shot(page, "70-failed-sheet-held")
    test.info().annotations.push({ type: "frames sent", description: sent.join(" || ").slice(0, 1200) })
    expect(sent, "nothing was sent on its own").toEqual([])
    await expect(field, "the words stay in the composer").toHaveValue(words)
    const plain = row.getByRole("button", { name: /Send as a plain file/ })
    await expect(plain, "the person may choose to send the workbook as a plain file").toBeVisible()

    // The explicit choice sends it, as a file.
    await plain.click()
    await expect.poll(() => sent.length, { timeout: 30_000, message: "the person's choice sends the message" }).toBe(1)
    expect(sent[0], "sent as a file for the agent").toContain(book.name)
    await H.shot(page, "71-sent-as-plain-file")
  } finally {
    await ctx.close()
  }
})

test("a Sheet still opening after the send hold keeps waiting, and can be cancelled, without sending the file", async ({ browser }) => {
  H.requireAgent()
  test.setTimeout(8 * 60_000)
  const book = twoSheetWorkbook(1, 2, "pomaly-sheet-syntetika.xlsx")
  const ctx = await H.clientContext(browser)
  const page = await ctx.newPage()
  const sent = sentMessages(page)
  let release: () => void = () => undefined
  const held = new Promise<void>(resolve => { release = resolve })
  try {
    // STUB: initialize is held 40 s in the browser before it goes on to the server.
    await page.route(INITIALIZE, async route => { await Promise.race([held, new Promise(r => setTimeout(r, 40_000))]); await route.continue() })
    await H.newChat(page)
    await H.attach(page, book.name, book.bytes)
    const field = page.getByPlaceholder(/^Message /)
    const words = `Podívej se na ${book.name}`
    await field.fill(words)
    await field.press("Enter")

    await page.waitForTimeout(15_000) // past the 10 s send hold, the Sheet still opening
    await H.shot(page, "72-still-opening")
    test.info().annotations.push({ type: "frames sent while opening", description: sent.join(" || ").slice(0, 1200) })
    expect(sent, "nothing sent while the Sheet is opening, not even as a file").toEqual([])
    const toast = page.locator("[data-sonner-toast]").filter({ hasText: "Opening the Sheet…" })
    await expect(toast, "the toast says the Sheet is opening and the message goes once it is open").toContainText("the message goes once it is open")
    await expect(field, "the words stay in the composer").toHaveValue(words)
    // Cancel in the toast (the row has its own Cancel for the upload): nothing goes, the words stay.
    await toast.getByRole("button", { name: "Cancel" }).click()
    release()
    await page.waitForTimeout(10_000)
    expect(sent, "a cancelled wait sends nothing, also once the Sheet opens").toEqual([])
    await expect(field, "the words stay after Cancel").toHaveValue(words)
  } finally {
    release()
    await ctx.close()
  }
})
