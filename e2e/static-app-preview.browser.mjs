// Component boundary qualification only; no Page persistence/ACL API claim.
// Missing browser binaries are failures, never skips; STATIC_APP_ENGINES=chromium narrows the run explicitly.
// An empty or unknown STATIC_APP_ENGINES, or a run that executes 0 browsers, fails.
// crewship-heavy env STATIC_APP_ENGINES=chromium node e2e/static-app-preview.browser.mjs
// (crewship-heavy drops the caller's environment; pass variables through `env`.)
//
// Integration limit: this component is NOT mounted. Any future mount needs a
// principal/authority epoch fence: when the user, workspace or permission
// changes, a preview loaded under the old identity is discarded (unmounted,
// late preparation results ignored) and remounted only under the new epoch.
import { build } from "esbuild"
import { chromium, firefox, webkit } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { requireBrowserRuns, selectEngines } from "./static-app-engines.mjs"


// One entry per sanitizer rule. Every vector points at /escape/* so a leaked
// request is visible, and tries to message the host if it ever executes.
const post = "parent.postMessage('author-executed','*')"
const vectors = {
  script: [`<script>${post}</script><SCRIPT src="/escape/s1"></SCRIPT><scr<script>ipt>${post}</scr</script>ipt>`, "script"],
  "on* handlers": [`<img src=x onerror="${post}"><div onclick="${post}" onmouseover="${post}" onfocus="${post}" autofocus tabindex=0>x</div><p onanimationstart="${post}">y</p><details open ontoggle="${post}">d</details>`, "[onerror],[onclick],[onmouseover],[onfocus],[onanimationstart],[ontoggle]"],
  svg: [`<svg onload="${post}"><script>${post}</script><foreignObject><iframe src="/escape/svg-frame"></iframe></foreignObject><a href="javascript:${post}"><text>t</text></a><image href="/escape/svg-img"/><animate onbegin="${post}"/></svg>`, "svg,foreignObject,animate"],
  mathml: [`<math><mtext><style><img src=x onerror="${post}"></style></mtext><maction actiontype="statusline#" href="javascript:${post}">m</maction></math>`, "math,maction,mtext"],
  "srcdoc nesting": [`<iframe srcdoc="<script>${post}</script><iframe srcdoc='&lt;script&gt;${post}&lt;/script&gt;'></iframe>"></iframe><object data="/escape/obj"></object><embed src="/escape/embed"><frameset><frame src="/escape/frame"></frameset>`, "iframe,object,embed,frame,frameset,[srcdoc]"],
  "meta refresh": [`<meta http-equiv="refresh" content="0;url=/escape/meta"><meta http-equiv="Set-Cookie" content="a=b"><p>hi</p>`, "meta[http-equiv='refresh'],meta[http-equiv='Set-Cookie']"],
  "base href": [`<base href="/escape/base/"><link rel="stylesheet" href="/escape/link.css"><link rel="prefetch" href="/escape/prefetch"><a href="/escape/a">x</a><area href="/escape/area"><a href="  jav&#x09;ascript:${post}">y</a>`, "base,link,[href]"],
  "css attr(type(<url>))": [`<p data-x="/escape/attr" style="background:attr(data-x type(<url>))">a</p><style>p{background-image:attr(title type(<url>))}</style><p title="/escape/attr2">b</p>`, "p[style]"],
  "css url/expression": [`<p style="background:url(/escape/p1)">a</p><p style="background-image:image-set('/escape/p2' 1x)">b</p><p style="width:expression(${post})">c</p><p style="background:\\75rl(/escape/p3)">d</p><style>@import url(/escape/imp);p{background:url(/escape/p4)}</style><style>@font-face{font-family:x;src:url(/escape/font)}</style>`, "p[style]"],
  "data: and script URLs": [`<img src="javascript:${post}"><img src="data:text/html;base64,PHNjcmlwdD5wYXJlbnQucG9zdE1lc3NhZ2UoJ2F1dGhvci1leGVjdXRlZCcsJyonKTwvc2NyaXB0Pg=="><img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9InBhcmVudC5wb3N0TWVzc2FnZSgnYXV0aG9yLWV4ZWN1dGVkJywnKicpIj48L3N2Zz4="><img src="/escape/img"><img srcset="/escape/set 2x"><video poster="/escape/poster"></video><div background="/escape/bg"></div>`, "[src],[srcset],[poster],[background],video"],
  "forms": [`<form action="/escape/form" method=post><input name=a><button formaction="/escape/fa">go</button><textarea>t</textarea><select><option>o</option></select></form><button type=submit formaction="/escape/b">b</button><input type=image src="/escape/inp">`, "form,input,button,textarea,select,option,[action],[formaction]"],
  "data-*, ping, template, noscript": [`<div data-x="1" data-onload="${post}">d</div><a ping="/escape/ping" download target="_blank">a</a><template><img src=x onerror="${post}"></template><noscript><p title="</noscript><img src=x onerror=${post}>"></noscript>`, "[data-x],[data-onload],[ping],[download],[target],template,noscript,img"],
}
const engines = [chromium, firefox, webkit]
const selected = selectEngines(process.env.STATIC_APP_ENGINES, engines.map(engine => engine.name()))
const output = await mkdtemp(join(tmpdir(), "crewship-static-app-"))
let server
try {
  await build({ entryPoints: ["e2e/static-app-preview.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic", alias: { "@": process.cwd() }, define: { "process.env.NODE_ENV": '"test"', "process.env.NEXT_PUBLIC_STATIC_APP_PREVIEW": '"true"' } })
  const escaped = []
  server = createServer(async (request, response) => {
    if (request.url.startsWith("/escape")) escaped.push(request.url)
    response.setHeader("Content-Type", request.url === "/bundle.js" ? "text/javascript" : "text/html")
    response.end(request.url === "/bundle.js" ? await readFile(join(output, "bundle.js")) : '<div id="root"></div><script type="module" src="/bundle.js"></script>')
  })
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  const failures = []
  let passed = 0
  for (const engine of engines.filter(candidate => selected.includes(candidate.name()))) {
    let browser
    try {
      browser = await engine.launch({ headless: true })
      const page = await browser.newPage()
      const requests = [], errors = []
      page.on("request", request => requests.push(request.url()))
      page.on("pageerror", error => errors.push(error.message))
      await page.addInitScript(() => {
        if (window !== window.top) return
        window.authorMessages = []
        window.addEventListener("message", event => window.authorMessages.push(event.data))
        window.hostSecret = "synthetic-host-credential"
      })
      const url = `http://127.0.0.1:${server.address().port}`
      await page.goto(url)
      const frame = page.frameLocator('iframe[title="App static preview"]')
      await frame.getByRole("heading", { name: "Static calculator" }).waitFor()
      assert.equal(await frame.locator("h1").evaluate(node => getComputedStyle(node).color), "rgb(1, 2, 3)")
      assert.equal(await page.locator("iframe").getAttribute("sandbox"), "")
      assert.equal(await page.locator("iframe").getAttribute("referrerpolicy"), "no-referrer")
      assert.equal(await frame.locator("script, iframe, object, embed, form, input, svg, math, video, base, link").count(), 0)
      assert.equal(await frame.locator("[href], [src]:not([alt='Local raster']), [srcset], [onerror], [onfocus], [ping]").count(), 0)
      assert.deepEqual(await frame.getByAltText("Local raster").evaluate(image => [image.naturalWidth, image.naturalHeight]), [2, 3])
      await frame.getByText("No navigation").click()
      assert.equal(page.url(), `${url}/`)
      assert.deepEqual(await page.evaluate(() => window.authorMessages), [])
      assert.equal(await frame.locator("body").evaluate(() => typeof window.authorExecuted), "undefined")
      assert.equal(await frame.locator("body").evaluate(() => typeof window.hostSecret), "undefined")
      assert.equal(await frame.locator("body").evaluate(() => {
        try { return parent.hostSecret } catch (error) { return error.name }
      }), "SecurityError")
      await page.getByRole("button", { name: "Publish second" }).click()
      await page.getByRole("button", { name: "Load version 2" }).waitFor()
      assert.equal(await frame.getByRole("heading", { name: "Static calculator" }).count(), 1)
      await page.getByRole("button", { name: "Load version 2" }).click()
      await frame.getByRole("heading", { name: "Version two" }).waitFor()
      await page.getByRole("button", { name: "Revoke access" }).click()
      await page.locator("iframe").waitFor({ state: "detached" })
      await page.getByRole("button", { name: "Replace Page" }).click()
      await page.getByRole("alert").waitFor()
      assert.equal(await page.locator("iframe").count(), 0)
      for (const [name, [html, banned]] of Object.entries(vectors)) {
        const prepared = await page.evaluate(source => window.prepareStatic(source), html)
        const survivors = await page.evaluate(([document, selector]) => new DOMParser().parseFromString(document, "text/html").querySelectorAll(selector).length, [prepared, banned])
        assert.equal(survivors, 0, `${name}: banned markup survived sanitizing`)
        assert.doesNotMatch(prepared, /url\s*\(|@import|@font-face|expression\s*\(|image-set|attr\s*\(/i, `${name}: loading CSS survived`)
        const metas = await page.evaluate(document => [...new DOMParser().parseFromString(document, "text/html").querySelectorAll("meta[http-equiv]")].map(node => node.getAttribute("http-equiv")), prepared)
        assert.deepEqual(metas, ["Content-Security-Policy"], `${name}: only the CSP meta may remain`)
        await page.evaluate(document => { const frame = window.document.createElement("iframe"); frame.title = "vector"; frame.setAttribute("sandbox", ""); frame.srcdoc = document; window.document.body.append(frame) }, prepared)
        await page.waitForTimeout(150)
        await page.evaluate(() => window.document.querySelector("iframe[title=vector]")?.remove())
      }
      assert.deepEqual(await page.evaluate(() => window.authorMessages), [], "a vector executed script")
      assert.equal(requests.filter(value => value.includes("/escape")).length, 0)
      assert.deepEqual(escaped, [])
      assert.deepEqual(errors, [])
      passed++
      console.log(`${engine.name()}: T-AX-05 static execution/network/navigation and T-AX-04 pinned revision/access removal PASS, ${Object.keys(vectors).length} sanitizer vector groups PASS`)
    } catch (error) {
      failures.push(error)
      console.error(`${engine.name()}: FAIL ${error.message}`)
    } finally { await browser?.close() }
  }
  requireBrowserRuns(passed + failures.length)
  if (failures.length) throw new AggregateError(failures, "Static App browser qualification failed")
} finally {
  await new Promise(resolve => server ? server.close(resolve) : resolve())
  await rm(output, { recursive: true, force: true })
}
