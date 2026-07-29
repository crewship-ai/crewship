import { chromium } from "playwright"
const B = "https://crewship-dev3.unifylab.cz"
const b = await chromium.launch()
const p = await (await b.newContext({ viewport: { width: 1600, height: 1150 } })).newPage()
const seen = []
p.on("response", (r) => {
  if (r.url().endsWith(".js")) seen.push({ url: r.url(), len: Number(r.headers()["content-length"] || 0) })
})
await p.goto(B + "/login", { waitUntil: "domcontentloaded" })
try {
  await p.fill('input[type="email"]', "demo@crewship.ai")
  await p.fill('input[type="password"]', "password123")
  await p.click('button[type="submit"]')
  await p.waitForURL((u) => !u.pathname.includes("login"), { timeout: 30000 })
} catch (e) {}
await p.goto(B + "/crews?agent=sam", { waitUntil: "networkidle" })
await p.waitForTimeout(4000)
// which of the downloaded chunks actually contain a dicebear collection?
let dice = 0, diceN = 0, total = 0
for (const s of seen) {
  total += s.len
  const body = await p.evaluate(async (u) => {
    try { const r = await fetch(u); const t = await r.text(); return /openPeeps|toonHead|croodles|botttsNeutral|miniavs|notionists/.test(t) } catch { return false }
  }, s.url)
  if (body) { dice += s.len; diceN++ }
}
console.log("chunks downloaded on page load:", seen.length, "=", Math.round(total/1024), "KB")
console.log("of which contain a dicebear collection:", diceN, "=", Math.round(dice/1024), "KB")
await b.close()
