/** A non-Rust editor using only the public Mooring TypeScript SDK. */
import { createEditorSDK, type SaveStatus } from "../../../lib/mooring/editor-sdk"
import { record } from "../../../lib/mooring/channel"

import { createBrowserEditorEnvironment, helloAfterLoad } from "../../../lib/mooring/editor-connection"

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
  environment: createBrowserEditorEnvironment(parentOrigin),
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
const cancelHello = helloAfterLoad(() => sdk.hello())
window.addEventListener("pagehide", () => { cancelHello(); sdk.close() }, { once: true })
