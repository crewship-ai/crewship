import { chromium, devices } from "@playwright/test"
import { writeFile } from "node:fs/promises"
import { homedir } from "node:os"
import { join } from "node:path"

const baseURL = process.env.CREWSHIP_SERVER
const route = process.argv[2]
const state = join(homedir(), ".crewship", "e2e-auth", "crewship-e2e-auth-" + process.env.CREWSHIP_INSTANCE_ID + ".json")
const browser = await chromium.launch()
try {
  for (const [name, env] of Object.entries({ desktop: devices["Desktop Chrome"], mobile: devices["Pixel 7"] })) {
    const context = await browser.newContext({ ...env, storageState: state, baseURL })
    try {
      const page = await context.newPage()
      if (route === "/crews" || route.startsWith("/chat/")) {
        await page.route("**/api/v1/system/runtime", route => route.fulfill({ json: { available: false } }))
      }
      await page.goto(route)
      await page.getByRole("main").first().waitFor({ state: "visible", timeout: 15000 })
      if (new URL(page.url()).pathname === "/login") throw Error("Screenshot was redirected to login")
      if (route === "/crews") {
        await page.getByText("Docker is not running.", { exact: true }).waitFor()
        await page.getByText("Finance & Marketing", { exact: true }).first().waitFor()
      } else if (route.startsWith("/chat/")) {
        await page.getByText("Docker is not running.", { exact: true }).waitFor()
        await page.locator("textarea[data-slot=input-group-control]").waitFor()
      } else if (route === "/settings/members") {
        await page.locator("tr[data-member]").first().waitFor()
      }
      const screenshot = await page.screenshot({ animations: "disabled", type: "jpeg", quality: 65 })
      await writeFile(join(process.env.BROWSER_EVIDENCE, name + ".jpg"), screenshot)
    } finally {
      await context.close()
    }
  }
} finally {
  await browser.close()
}
