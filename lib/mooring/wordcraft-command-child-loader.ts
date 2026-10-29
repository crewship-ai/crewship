/** C1 bundle entry only. No legacy SDK, parent credential or message-selected URL. */
import { startWordCraftCommandChild, type WordCraftCommandNative } from "./wordcraft-command-child"

async function boot() {
  const query = new URLSearchParams(location.search)
  if (window.parent === window || query.get("mooring") !== "1" || query.get("commands") !== "1") throw new Error("isolated command startup required")
  const bundle = /^\/(?:_editor\/)?b\/([0-9a-f]{64})\/index\.html$/.exec(location.pathname)?.[1]
  if (!bundle) throw new Error("invalid command bundle identity")
  const nativeURL = new URL("./wordcraft_web.js", import.meta.url)
  const native = await import(/* webpackIgnore: true */ nativeURL.href) as WordCraftCommandNative & { default(input: { module_or_path: URL }): Promise<unknown> }
  await native.default({ module_or_path: new URL("./wordcraft_web_bg.wasm", import.meta.url) })
  if (typeof native.start_c1_commands !== "function") throw new Error("unqualified command bundle")
  const controller = startWordCraftCommandChild(native, bundle, () => new Promise<void>((resolve, reject) => {
    const deadline = performance.now() + 30000
    const inspect = () => {
      if (!document.getElementById("wordcraft_loading")) { resolve(); return }
      if (performance.now() >= deadline) { reject(new Error("native startup timeout")); return }
      setTimeout(inspect, 25)
    }
    inspect()
  }))
  window.addEventListener("pagehide", () => controller.close(), { once: true })
}
void boot().catch(() => {
  const loading = document.getElementById("wordcraft_loading")
  if (loading) loading.textContent = "WordCraft could not start. No unconfirmed command has been marked saved."
})
