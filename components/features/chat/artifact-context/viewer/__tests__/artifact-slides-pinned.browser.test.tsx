// @vitest-environment node
import { existsSync, readFileSync } from "node:fs"
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"
import { withPinnedTheme } from "@/components/features/artifacts/design-system/pinned-theme"

it.skipIf(!existsSync(chromium.executablePath()))("renders pinned tokens and a real licensed face in admitted Slides and Present", async () => {
  const goFont = readFileSync("public/fonts/go-regular/Go-Regular.ttf").toString("base64")
  const pinnedFont = readFileSync("public/fonts/inter/InterVariable.woff2").toString("base64")
  const bundle = await build({
    stdin: { resolveDir: process.cwd(), loader: "tsx", contents: `
      import { createRoot } from "react-dom/client"
      import { parseArtifactSlides } from "./lib/pages/artifact-slides"
      import fixture from "./testdata/artifact-slides-projection.json"
      import { ArtifactSlidesPreview } from "./components/features/chat/artifact-context/viewer/artifact-slides-preview"
      import { pinnedSlideTheme } from "./components/features/artifacts/design-system/pinned-theme"
      const projection = parseArtifactSlides(fixture, "page")
      if (!projection) throw new Error("Go projection is not admitted")
      window.pinnedProjection = projection
      const tokens = JSON.stringify({schema_version:1,colors:{canvas:"#1d4ed8",ink:"#111111"},typography:{body:{size:24,weight:400,font_family:"Inter, sans-serif"}},spacing:{},radii:{}})
      const pinnedTheme = pinnedSlideTheme(tokens,[{path:"slides-library.json",content:'{"schema_version":1,"background":"canvas","text_color":"ink","typography":"body"}'}])
      const pinnedFiles = [{path:"fonts/Inter.woff2",media_type:"font/woff2",status:"copied",family:"Inter",weight:400,style:"normal",content:${JSON.stringify(pinnedFont)}}]
      createRoot(document.getElementById("root")).render(<ArtifactSlidesPreview workspaceId="ws" pageId="page" codeVersion={2} artifactKind="slides" path="slides/intro.html" title="Pinned Slides" selectMode={false} onSelect={()=>{}} onNavigate={()=>{}} pinnedTheme={pinnedTheme} pinnedFiles={pinnedFiles}/>)
    ` },
    bundle: true, write: false, platform: "browser", format: "iife", jsx: "automatic",
    loader: { ".css": "empty" },
    define: { "process.env.NODE_ENV": '"production"' },
    plugins: [{ name: "projection-fixture", setup(builder) {
      builder.onResolve({ filter: /^\.\.\/projects\/use-artifact-slides$/ }, args => ({ path: args.path, namespace: "projection" }))
      builder.onLoad({ filter: /.*/, namespace: "projection" }, () => ({ contents: 'export const useArtifactSlides = () => ({enabled:true,projection:window.pinnedProjection,scope:"pin-fixture",downloadPDF:async()=>null})' }))
    } }],
  })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage()
    const staticPreview = withPinnedTheme('<!-- <head> -->\n<!doctype html><html><head><title>Preview</title></head><body><h1>Text</h1></body></html>', ':root{--ds-color-ink:#111111} h1{color:var(--ds-color-ink)}')
    await page.setContent(staticPreview)
    const staticAppearance = await page.evaluate(() => ({
      standards: document.compatMode,
      styleInHead: Boolean(document.head.querySelector("style[data-library-pin]")),
      color: getComputedStyle(document.querySelector("h1")!).color,
    }))
    expect(staticAppearance).toEqual({ standards: "CSS1Compat", styleInHead: true, color: "rgb(17, 17, 17)" })

    await page.setContent(`<!doctype html><style>@font-face{font-family:"Go Regular";src:url(data:font/ttf;base64,${goFont})}</style><div id="root"></div>`)
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const selector = '.artifact-slides-main [data-slide-content="title"]'
    await page.waitForFunction(sel => {
      const node = document.querySelector(sel)
      if (!node) return false
      const alias = getComputedStyle(node).fontFamily.replace(/^"|"$/g, "")
      return alias.startsWith("crewship-pin-") && [...document.fonts].some(face => face.family.replace(/^"|"$/g, "") === alias && face.status === "loaded")
    }, selector)
    const appearance = await page.locator(selector).evaluate(node => {
      const style = getComputedStyle(node)
      return { family: style.fontFamily, color: style.color, size: style.fontSize, background: getComputedStyle(node.closest("svg")!).backgroundColor }
    })
    expect(appearance).toMatchObject({ family: expect.stringContaining("crewship-pin-"), color: "rgb(17, 17, 17)", size: "24px", background: "rgb(29, 78, 216)" })
    await page.getByRole("button", { name: "Present" }).click()
    expect(await page.getByRole("dialog", { name: "Present slides" }).locator('[data-slide-content="title"]').evaluate(node => getComputedStyle(node).fontFamily)).toBe(appearance.family)
  } finally {
    await browser.close()
  }
}, 45_000)
