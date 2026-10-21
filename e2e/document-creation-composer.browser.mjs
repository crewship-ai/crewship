// Isolated production-component browser test, not a provider or dev4 acceptance.
// Run: crewship-heavy node e2e/document-creation-composer.browser.mjs
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const output = await mkdtemp(join(tmpdir(), "crewship-document-composer-"))
let server, browser
try {
  await build({ entryPoints: ["e2e/document-creation-composer.fixture.tsx"], bundle: true,
    outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic",
    alias: { "@": process.cwd() }, define: { "process.env": '{"NODE_ENV":"test"}' },
    plugins: [{ name: "synthetic-composer-neighbours", setup(build) {
      build.onResolve({ filter: /(?:^@\/hooks\/use-auth$|\/hooks\/use-auth(?:\.tsx)?$|^\.\/attachment-zone$|^\.\/mention-autocomplete$)/ }, args => ({ path: args.path, namespace: "fixture" }))
      build.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ loader: "jsx", contents:
        args.path.includes("use-auth") ? 'export const useSessionSafe = () => ({data:{user:{id:"fixture-human"}}});' :
        args.path.includes("mention-autocomplete") ? 'export const MentionAutocomplete = () => null;' :
        'export const AttachmentZone = ({children}) => children; export const EnsureChatSessionProvider = ({children}) => children; export const AttachmentButton = () => null; export const CameraButton = () => null;'
      }))
    } }],
  })
  server = createServer(async (request, response) => {
    if (request.url === "/bundle.js") { response.setHeader("Content-Type", "text/javascript"); response.end(await readFile(join(output,"bundle.js"))); return }
    response.setHeader("Content-Type", "text/html"); response.end('<meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/bundle.js"></script>')
  })
  await new Promise(resolve => server.listen(0,"127.0.0.1",resolve))
  browser = await chromium.launch({headless:true})
  for (const width of [1200,390]) {
    const page = await browser.newPage({viewport:{width,height:844},hasTouch:width<768,isMobile:width<768})
    const errors=[]; page.on("pageerror",error => { errors.push(error.message); console.error("Fixture page error:", error.message) })
    await page.goto(`http://127.0.0.1:${server.address().port}`)
    const selection = page.getByRole("button",{name:"Create document",exact:true})
    await selection.focus(); await page.keyboard.press("Space")
    assert.equal(await selection.getAttribute("aria-pressed"),"true")
    const text = width<768 ? "Napiš mi plán zahrady.\nPřidej tři části!" : "Write a garden plan. Include 3 sections!"
    await page.getByRole("textbox").fill(text)
    await page.getByRole("button",{name:"Toggle send refusal"}).click()
    await page.getByRole("textbox").press("Enter")
    const messages = page.getByLabel("Sent fixture messages")
    await page.waitForFunction(() => JSON.parse(document.querySelector("output").textContent).length===1)
    assert.equal(await page.getByRole("textbox").inputValue(),text)
    await page.getByRole("button",{name:"Toggle send refusal"}).click()
    await page.getByRole("textbox").press("Enter")
    await page.waitForFunction(() => JSON.parse(document.querySelector("output").textContent).length===2)
    const sent=JSON.parse(await messages.textContent())
    assert.equal(sent[0].text,text); assert.deepEqual(sent[1],sent[0])
    assert.equal(sent[1].metadata.document_creation_intent.drafting,true)
    assert.equal(await selection.getAttribute("aria-pressed"),"false")
    await selection.click(); await page.reload()
    assert.equal(await selection.getAttribute("aria-pressed"),"false")
    assert.deepEqual(errors,[])
    await page.close()
    console.log(JSON.stringify({width,explicitConsent:true,language:width<768?"cs":"en",sameRetry:true,ephemeral:true,providerInvoked:false}))
  }
} finally {
  await browser?.close()
  await new Promise(resolve => server ? server.close(resolve) : resolve())
  await rm(output,{recursive:true,force:true})
}
