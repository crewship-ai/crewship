/** A non-Rust editor using only the public Mooring TypeScript SDK. */
import { createEditorSDK, type SaveStatus } from "../../../lib/mooring/editor-sdk"
import { capabilityPort, record } from "../../../lib/mooring/channel"

const input = document.querySelector<HTMLTextAreaElement>("textarea")!
const save = document.querySelector<HTMLButtonElement>("button")!
const status = document.querySelector<HTMLOutputElement>("output")!
const confirmed = document.querySelector<HTMLElement>("#confirmed")!
const revisionLabel = document.querySelector<HTMLElement>("#revision")!
let dirty = false, submitted: string | undefined
let draftVersion = 0, submittedVersion = 0, confirmedText = ""
const labels: Record<SaveStatus, string> = { connecting: "Connecting…", saved: "Saved", draft: "Not saved · draft", saving: "Saving…", offline: "Offline · changes are not confirmed", error: "Not saved" }
const config = new URL(location.href)
const parentOrigin = config.searchParams.get("parent_origin") ?? ""
const sdk = createEditorSDK({
  parentOrigin,
  bundle: location.pathname.split("/").at(-2) ?? "",
  environment: {
    parent: window.parent,
    listen(handler) {
      const listener = (event: MessageEvent) => {
        if (event.source !== window.parent || event.origin !== parentOrigin) return
        handler({ source: event.source, origin: event.origin, data: event.data, ports: event.ports.map(capabilityPort) })
      }
      window.addEventListener("message", listener)
      return () => window.removeEventListener("message", listener)
    },
    hello: (message, origin) => window.parent.postMessage(message, origin),
  },
  onState(state) {
    const text = record(state.snapshot) && typeof state.snapshot.text === "string" ? state.snapshot.text : ""
    confirmedText = text
    confirmed.textContent = text
    revisionLabel.textContent = `Confirmed revision ${state.rev}`
    // A push from an agent updates the confirmed view, never the person's draft.
    if (!dirty && submitted === undefined) input.value = text
  },
  onStatus(state, message) {
    status.textContent = message ? `${labels[state]} · ${message}` : labels[state]
    save.disabled = state === "connecting" || state === "saving" || state === "offline"
  },
  onReceipt(receipt) {
    dirty = draftVersion !== submittedVersion
    if (!dirty) input.value = confirmedText
    submitted = undefined
    sdk.draft(dirty)
    revisionLabel.textContent = `Saved · receipt revision ${receipt.rev}`
  },
})
input.addEventListener("input", () => { draftVersion++; dirty = true; sdk.draft(true) })
save.addEventListener("click", () => {
  submitted = input.value
  submittedVersion = draftVersion
  if (!sdk.command("set", { text: submitted })) submitted = undefined
})
// The child load event precedes the parent's iframe load notification. Posting
// hello in the next task lets the host bind it to the document it just loaded.
window.addEventListener("load", () => setTimeout(() => sdk.hello(), 0), { once: true })
window.addEventListener("pagehide", () => sdk.close(), { once: true })
