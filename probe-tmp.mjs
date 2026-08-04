import { chromium } from "@playwright/test"
const b = await chromium.launch()
const c = await b.newContext({ baseURL: "http://192.168.1.201:8082", viewport:{width:1440,height:900}, colorScheme:"dark" })
const p = await c.newPage()
await p.goto("/login"); await p.waitForLoadState("networkidle")
const csrf = await p.evaluate(async () => (await (await fetch("/api/auth/csrf")).json()).csrfToken)
await p.evaluate(async ({csrf}) => { await fetch("/api/auth/callback/credentials",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({email:"demo@crewship.ai",password:"password123",csrfToken:csrf,redirect:"false"})}) }, {csrf})
await p.goto("/")
await p.evaluate(() => localStorage.setItem("crewship.workspaceId","cmrxjalmy00025b1dad31"))
await p.goto("/"); await p.waitForLoadState("networkidle"); await p.waitForTimeout(2500)
// Kill the websocket so ONLY the poll can save it — the exact failure mode.
await p.evaluate(() => { for (const ws of (window.__wsAll ?? [])) ws.close?.() ; window.WebSocket = class { constructor(){ setTimeout(()=>this.onerror?.(new Event("error")),0) } close(){} send(){} } })
console.log("t=0   ", (await p.locator('[role="status"][aria-label^="Crews"]').innerText()).trim())
const t0 = Date.now()
const seen = []
const iv = setInterval(async () => {
  try { seen.push(`${String(Math.round((Date.now()-t0)/1000)).padStart(3)}s ${(await p.locator('[role="status"][aria-label^="Crews"]').innerText()).trim()}`) } catch {}
}, 2000)
setTimeout(() => {}, 0)
await new Promise(r => setTimeout(r, 45000))
clearInterval(iv)
console.log(seen.join("\n"))
await b.close()
