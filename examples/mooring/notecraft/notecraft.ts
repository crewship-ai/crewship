/** NoteCraft: a minimal plain-text Craft application built only on the
 * Mooring SDK (docs/development/mooring-sdk.md). It shows who is editing,
 * request/hand-off/release, a truthful save status, local recovery drafts for
 * compare/copy, and version history with restore. The host supplies the
 * authorized transport and the private journal; NoteCraft owns only its UI. */
import {
  compareRecovery, createLeaseActions, createLeaseState, createSingleEditorSave, createVersionHistory, describeLease, ownsLease, recoveryOffers, saveLabel,
  type LeaseCustody, type LeaseTransport, type LeaseView, type RecoveryComparison, type RecoveryDraft, type RecoveryJournal, type RecoveryOffer, type SaveController, type SaveRequest, type SaveState, type VersionsTransport,
} from "../../../lib/mooring/sdk"

export interface NoteCraftHost {
  scope: { workspaceId: string; pageId: string; sessionId: string; enginePin: string }
  /** The viewer's edit right as the server reported it. Advisory only. */
  mayEdit: boolean
  document: LeaseTransport & {
    read(signal: AbortSignal): Promise<{ version: string; bytes: Uint8Array }>
    save(request: SaveRequest, signal: AbortSignal): Promise<unknown>
    reconcile(request: SaveRequest, signal: AbortSignal): Promise<unknown>
  }
  versions: VersionsTransport
  journal: RecoveryJournal
}
export const NOTECRAFT_APPLICATION = "notecraft"
const MAX_NOTE_BYTES = 256 * 1024
const encode = (text: string) => new TextEncoder().encode(text)
const decode = (bytes: Uint8Array) => new TextDecoder("utf-8", { fatal: true }).decode(bytes)

export function mountNoteCraft(root: HTMLElement, host: NoteCraftHost) {
  root.innerHTML = `<section class="notecraft" aria-label="NoteCraft">
<div role="toolbar" aria-label="Úpravy dokumentu"><span data-lease>Načítám…</span><span data-lease-actions></span></div>
<output role="status" data-save>Pouze čtení</output>
<label>Poznámka<textarea data-note readonly rows="10"></textarea></label>
<button type="button" data-save-now disabled>Uložit</button><button type="button" data-reconcile hidden>Ověřit výsledek</button>
<p role="alert" data-error hidden></p>
<section data-recovery hidden><h2>Místní koncepty</h2><ul data-offers></ul><p data-compare></p><label>Kopie konceptu<textarea data-copy readonly rows="4"></textarea></label></section>
<section><h2>Verze</h2><ol data-versions></ol></section>
</section>`
  const $ = <T extends HTMLElement>(name: string) => root.querySelector<T>(`[data-${name}]`)!
  const note = $<HTMLTextAreaElement>("note"), status = $<HTMLOutputElement>("save"), alert = $("error")
  const lifetime = new AbortController(), signal = lifetime.signal
  const lease = createLeaseState(), actions = createLeaseActions(host.document, signal), history = createVersionHistory(host.versions)
  // One journal per mount; each editing lifecycle borrows it. Only unmount closes it.
  const journal: RecoveryJournal = { ...host.journal, close: async () => {} }
  let server: { version: string; bytes: Uint8Array } = { version: "1", bytes: new Uint8Array() }
  let editing: { controller: SaveController; generation: string; state: SaveState } | undefined
  let edits = 0, tickets = 0, busy = false

  function fail(message: string) { alert.hidden = false; alert.textContent = message }
  async function run(task: () => Promise<void>, message: string) {
    if (busy || signal.aborted) return
    busy = true; alert.hidden = true
    try { await task() } catch { fail(message); await refresh().catch(() => {}) } finally { busy = false; render() }
  }
  function button(label: string, onClick: () => void) {
    const b = document.createElement("button"); b.type = "button"; b.textContent = label; b.addEventListener("click", onClick); return b
  }
  function render() {
    const view = lease.current
    if (view) {
      const c = describeLease(view, host.mayEdit)
      $("lease").textContent = c.label + (c.readOnly ? " · Pouze čtení" : "") + (c.draftKeptReadOnly ? " · Pouze čtení · místní koncept zachován" : "")
      const bar = $("lease-actions"); bar.replaceChildren()
      if (c.canAcquire) bar.append(button("Převzít úpravy", () => void run(acquire, "Převzetí nebylo potvrzeno.")))
      if (c.canRequestHandoff) bar.append(button("Požádat o předání", () => void run(async () => { lease.observe(await actions.requestHandoff()) }, "Žádost nebyla potvrzena.")))
      if (c.canRelease && editing) bar.append(button("Uvolnit úpravy", () => void run(() => transfer(), "Uvolnění nebylo potvrzeno. Koncept zůstává zachován.")))
      if (editing) for (const t of c.handoffTargets) bar.append(button(t.label, () => void run(() => transfer(t.request_id), "Předání nebylo potvrzeno. Koncept zůstává zachován.")))
    }
    status.textContent = editing ? saveLabel(editing.state) : "Pouze čtení"
    $<HTMLButtonElement>("save-now").disabled = !editing || busy
    $("reconcile").hidden = editing?.state.phase !== "uncertain"
  }
  async function load() {
    server = await host.document.read(signal)
    note.value = decode(server.bytes)
  }
  function startEditing(generation: string) {
    edits = 0
    let self: SaveController | undefined
    const controller = self = createSingleEditorSave({
      workspaceId: host.scope.workspaceId, pageId: host.scope.pageId, applicationId: NOTECRAFT_APPLICATION, enginePin: host.scope.enginePin,
      sessionId: host.scope.sessionId, generation, baseVersion: server.version, loaded: { bytes: server.bytes }, journal,
      engine: {
        snapshot: async () => ({ bytes: encode(note.value), editorRevision: `note:${generation}:${edits}` }),
        validate: async draft => { if (draft.bytes.byteLength > MAX_NOTE_BYTES) throw new Error("note_too_large"); decode(draft.bytes) },
      },
      transport: host.document,
      onState: state => { if (editing && editing.controller === self) { editing.state = state; render() } },
    })
    editing = { controller, generation, state: { phase: "idle", dirty: false } }
    note.readOnly = false
  }
  /** Lost custody: capture the draft durably, stop writing, show the server
   * version, and offer any unsaved text for compare/copy. Never resend it. */
  async function stopEditing() {
    const current = editing
    if (!current) return
    const text = note.value, clean = current.controller.confirmedVersion() !== undefined
    note.readOnly = true
    await current.controller.revokeWrite().catch(() => false)
    await current.controller.close().catch(() => {})
    editing = undefined
    const base = server.version
    await load()
    if (!clean && text !== note.value) {
      const draft: RecoveryDraft = { applicationId: NOTECRAFT_APPLICATION, enginePin: host.scope.enginePin, sessionId: host.scope.sessionId, generation: current.generation, baseVersion: base, editorRevision: "local", bytes: encode(text) }
      offer([{ label: "Váš neuložený koncept", draft, compare: s => compareRecovery(s, draft), copy: () => draft.bytes.slice() }])
    }
  }
  /** Compare and copy are the only actions; there is deliberately no "save". */
  function offer(offers: (Pick<RecoveryOffer, "draft" | "compare" | "copy"> & { label?: string })[]) {
    if (!offers.length) return
    $("recovery").hidden = false
    for (const o of offers) {
      const item = document.createElement("li")
      item.append(`${o.label ?? "Místní koncept"} nad verzí ${o.draft.baseVersion}`,
        button("Porovnat", () => void o.compare(server).then(describe)),
        button("Kopírovat", () => { const text = decode(o.copy()); $<HTMLTextAreaElement>("copy").value = text; void navigator.clipboard?.writeText(text).catch(() => {}) }))
      $("offers").append(item)
    }
  }
  const describe = (c: RecoveryComparison) => { $("compare").textContent = c.equal ? `Shodné se serverovou verzí ${c.serverVersion}` : `Liší se od serverové verze ${c.serverVersion}` }
  async function listVersions() {
    const ol = $("versions"); ol.replaceChildren()
    for (const v of await history.list(signal)) {
      const li = document.createElement("li")
      li.append(`Verze ${v.version}${v.current ? " · aktuální" : ""}${v.restoredFrom ? ` · obnoveno z ${v.restoredFrom}` : ""}`)
      if (editing && !v.current) li.append(button(`Obnovit verzi ${v.version}`, () => void run(() => restore(v.version), "Obnovení nebylo potvrzeno.")))
      ol.append(li)
    }
  }
  async function observe(view: LeaseView | undefined) {
    if (!view) return
    if (editing && !ownsLease(view, editing.generation)) await stopEditing()
    if (!editing && host.mayEdit && ownsLease(view)) { await load(); startEditing(view.generation!) }
    render(); await listVersions()
  }
  async function refresh() { await observe(lease.observe(await host.document.lease(signal))) }
  async function acquire() { await observe(lease.observe(await actions.acquire())) }
  async function transfer(requestId?: string) {
    const current = editing, view = lease.current
    if (!current || !view) return
    const custody: LeaseCustody = { generation: current.generation, flush: () => current.controller.flush(), confirmedVersion: () => current.controller.confirmedVersion(), revokeWrite: () => current.controller.revokeWrite() }
    const proof = requestId ? await actions.handOff(view, requestId, custody) : await actions.release(view, custody)
    await observe(lease.observe(proof))
  }
  async function restore(target: string) {
    const current = editing
    if (!current) return
    const base = current.controller.confirmedVersion()
    if (base === undefined) { fail("Nejdřív uložte koncept; obnovení ho nepřepíše."); return }
    await current.controller.close()
    editing = undefined
    await history.restore(target, { base, generation: current.generation }, signal)
    await load(); startEditing(current.generation); render(); await listVersions()
  }

  note.addEventListener("input", () => { if (editing) { edits++; editing.controller.edited() } })
  $("save-now").addEventListener("click", () => { void editing?.controller.save(`notecraft-${++tickets}`).catch(() => {}) })
  $("reconcile").addEventListener("click", () => { void editing?.controller.reconcile() })
  const ready = (async () => {
    await load()
    offer(await recoveryOffers(host.journal))
    await refresh()
  })()
  return {
    ready,
    refresh: () => run(refresh, "Stav úprav se nepodařilo načíst."),
    async close() { lifetime.abort(); await editing?.controller.close().catch(() => {}); editing = undefined; await host.journal.close() },
  }
}
