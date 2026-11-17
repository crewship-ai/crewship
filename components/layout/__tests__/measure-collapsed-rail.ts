import { spawn } from "node:child_process"
import { resolve } from "node:path"

/** Transplant the actual rendered rail or phone views into Chromium with production
 * CSS. React callers also check the live collapse/expand callbacks. No server,
 * duplicated CSS or hand-built rail fixture is involved. */
function measureBrowserMarkup(html: string, views: boolean): Promise<void> {
  return new Promise((resolveCheck, reject) => {
    const child = spawn(process.execPath, [resolve("components/layout/__tests__/run-collapsed-rail-browser.mjs"), ...(views ? ["--views"] : [])], { stdio: ["pipe", "pipe", "pipe"] })
    let output = ""
    child.stdout.on("data", data => { output += String(data) })
    child.stderr.on("data", data => { output += String(data) })
    child.on("error", reject)
    child.on("exit", code => code === 0 ? resolveCheck() : reject(new Error(output || `Browser geometry exited ${code}`)))
    child.stdin.end(html)
  })
}

export function measureCollapsedRail(html: string): Promise<void> {
  return measureBrowserMarkup(html, false)
}

export function measurePhoneViews(html: string): Promise<void> {
  return measureBrowserMarkup(html, true)
}
