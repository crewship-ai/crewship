// Exploration pass: snapshots of a URL list so the scripted walk can target real labels.
import { test } from "@playwright/test"
import * as fs from "node:fs"
import * as path from "node:path"
import { audit, note, OUT } from "./audit"
import { browserContext, ownerApi, ownerStorage, walkWorkspace } from "./session"

test("probe", async ({ browser }) => {
  test.skip(process.env.UX_TARGET !== "probe")
  const api = await ownerApi()
  const ws = await walkWorkspace(api)
  note("probe:ws", ws.id)
  const status = await api.get(`/api/v1/documents/status?workspace_id=${ws.id}`)
  note("probe:documents-status", { s: status.status(), body: (await status.text()).slice(0, 400) })
  const ver = await api.get(`/api/v1/system/version`)
  note("probe:version", (await ver.text()).slice(0, 300))
  const ctx = await browserContext(browser, "desktop", await ownerStorage())
  const page = await ctx.newPage()
  for (const p of (fs.existsSync(path.join(OUT, "probe-paths.txt")) ? fs.readFileSync(path.join(OUT, "probe-paths.txt"), "utf8").trim() : "/artifacts").split(",")) {
    await page.goto(`${p}${p.includes("?") ? "&" : "?"}workspace_id=${ws.id}`)
    await page.waitForTimeout(6000)
    await audit(page, `probe${p.replace(/[^a-z0-9]+/gi, "-")}`, "desktop")
  }
  await ctx.close()
})
