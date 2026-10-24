/** Bundled child entry, compiled by the packaging step. No parent credentials. */
import { startWordCraftChild, type WordCraftChildSDK, type WordCraftNativeWeb } from "./wordcraft-child"

type InitializedModule = { default(input?: { module_or_path: URL }): Promise<unknown> }

async function boot() {
  if (window.parent === window || new URLSearchParams(location.search).get("mooring") !== "1") throw new Error("isolated startup required")
  const match = /^\/_editor\/b\/([0-9a-f]{64})\/index\.html$/.exec(location.pathname)
  if (!match) throw new Error("invalid bundle identity")
  // These URLs are constant relative paths INSIDE our own approved bundle.
  // No bootstrap/server/child message may choose a module or script URL.
  const sdkURL = new URL("./mooring_wordcraft_client.js", import.meta.url)
  const nativeURL = new URL("./wordcraft_web.js", import.meta.url)
  const sdk = await import(/* webpackIgnore: true */ sdkURL.href) as WordCraftChildSDK & InitializedModule
  const native = await import(/* webpackIgnore: true */ nativeURL.href) as WordCraftNativeWeb & InitializedModule
  await sdk.default({ module_or_path: new URL("./mooring_wordcraft_client_bg.wasm", import.meta.url) })
  await native.default({ module_or_path: new URL("./wordcraft_web_bg.wasm", import.meta.url) })
  if (typeof sdk.validate_host_bootstrap !== "function" || typeof sdk.validate_host_peer_document_update !== "function"
    || typeof native.start_collaborative !== "function") throw new Error("unqualified bundle API")
  const controller = startWordCraftChild(sdk, native, match[1], () => new Promise<void>((resolve, reject) => {
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
  if (loading) loading.textContent = "WordCraft could not start. The original draft has not been marked saved."
})
