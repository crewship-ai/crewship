/** NoteCraft: a minimal plain-text Craft application built only on the
 * Mooring SDK (docs/development/mooring-sdk.md). It shows who is editing,
 * request/hand-off/release, a truthful save status, local recovery drafts for
 * compare/copy, and version history with restore. The host supplies the
 * authorized transport and the private journal; NoteCraft owns only its UI. */
import {
  compareRecovery, createLeaseActions, createLeaseState, createSingleEditorSave, createVersionHistory, describeLease, ownsLease, recoveryOffers, SaveError, saveLabel, saveSettled,
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
  /** Opens the private draft journal (`openRecoveryJournal` in the product).
   * Each editing lifecycle opens its own, so it owns one journal row: a draft
   * retained from an earlier lost custody is never overwritten or confirmed
   * away by a later lifecycle. */
  openJournal(): Promise<RecoveryJournal>
}
export const NOTECRAFT_APPLICATION = "notecraft"
/** `durable`: every typed text is saved or journaled. `local-copy`: some text
 * exists only in this window (the view stays usable to store or discard it).
 * `revoked`: the SDK ended the lifecycle (read access removed, or the host
 * retired the journal at sign-out); this view keeps no text and no copy. */
export type NoteCraftCloseResult = "durable" | "local-copy" | "revoked"
const readRevokedError = (error: unknown) => error instanceof SaveError && error.reason === "read_revoked"
const MAX_NOTE_BYTES = 256 * 1024
const encode = (text: string) => new TextEncoder().encode(text)
const decode = (bytes: Uint8Array) => new TextDecoder("utf-8", { fatal: true }).decode(bytes)

export function mountNoteCraft(root: HTMLElement, host: NoteCraftHost) {
  root.innerHTML = `<section class="notecraft" aria-label="NoteCraft">
<div role="toolbar" aria-label="Úpravy dokumentu"><span data-lease>Načítám…</span><span data-lease-actions></span></div>
<output role="status" data-save>Pouze čtení</output>
<label>Poznámka<textarea data-note readonly rows="10"></textarea></label>
<button type="button" data-save-now disabled>Uložit</button><button type="button" data-reconcile hidden>Ověřit výsledek</button>
<button type="button" data-keep-retry hidden>Znovu uchovat koncept</button><button type="button" data-keep-discard hidden>Zahodit místní kopii</button>
<p role="alert" data-error hidden></p>
<section data-recovery hidden><h2>Místní koncepty</h2><ul data-offers></ul><p data-compare></p><label>Kopie konceptu<textarea data-copy readonly rows="4"></textarea></label></section>
<section><h2>Verze</h2><ol data-versions></ol></section>
</section>`
  const $ = <T extends HTMLElement>(name: string) => root.querySelector<T>(`[data-${name}]`)!
  const note = $<HTMLTextAreaElement>("note"), status = $<HTMLOutputElement>("save"), alert = $("error")
  const lifetime = new AbortController(), signal = lifetime.signal
  /** NoteCraft's own server reads and actions. A read revocation reported by
   * any of them goes to the one handler, `terminate`, before the caller sees
   * the error; the SDK save lifecycle reports its own as the `revoked` state. */
  const guard = <T>(work: Promise<T>): Promise<T> => work.catch((error: unknown) => { if (readRevokedError(error)) terminate("revoked"); throw error })
  const doc = {
    read: (s: AbortSignal) => guard(host.document.read(s)),
    lease: (...a: Parameters<LeaseTransport["lease"]>) => guard(host.document.lease(...a)),
    leaseAction: (...a: Parameters<LeaseTransport["leaseAction"]>) => guard(host.document.leaseAction(...a)),
  }
  const versions: VersionsTransport = {
    list: (...a: Parameters<VersionsTransport["list"]>) => guard(host.versions.list(...a)),
    restore: (...a: Parameters<VersionsTransport["restore"]>) => guard(host.versions.restore(...a)),
  }
  const lease = createLeaseState(), actions = createLeaseActions(doc, signal), history = createVersionHistory(versions)
  let server: { version: string; bytes: Uint8Array } = { version: "1", bytes: new Uint8Array() }
  let editing: { controller: SaveController; generation: string; state: SaveState } | undefined
  let edits = 0, tickets = 0, busy = false, closing = false
  /** Set only by `terminate`: `revoked` (read access removed) or `ended` (the
   * host retired the journal, e.g. at sign-out). The view then keeps no text. */
  let terminated: "revoked" | "ended" | undefined
  /** Controllers NoteCraft closes itself; any other `stopped` means the host
   * retired the journal. */
  const selfClosed = new WeakSet<SaveController>()
  const closeController = (c: SaveController) => { selfClosed.add(c); return c.close() }
  /** Text no journal could store durably. It stays in the note, editable, and
   * nothing loads over it until it is stored or deliberately discarded. */
  let localCopy: { generation: string; baseVersion: string } | undefined
  const LOCAL_COPY_ONLY = "Koncept se nepodařilo uchovat v prohlížeči. Text zůstává jen v tomto okně; zkopírujte si ho, než okno zavřete."

  function fail(message: string) { alert.hidden = false; alert.textContent = localCopy && message !== LOCAL_COPY_ONLY ? `${message} ${LOCAL_COPY_ONLY}` : message }
  async function run(task: () => Promise<void>, message: string) {
    if (busy || closing || terminated || signal.aborted) return
    busy = true; alert.hidden = true; render()
    try { await task() } catch (error) {
      // A read revocation is never swallowed: it goes to the one handler.
      if (readRevokedError(error)) terminate("revoked")
      if (terminated) return
      fail(message); await refresh().catch(() => {})
    } finally { busy = false; render() }
  }
  /** The single place a view ends without NoteCraft closing it. Every copy of
   * the text leaves this view (note, offers, memory) and no local copy is ever
   * made afterwards. On `revoked` the lifecycle's SDK revokeRead (stop, purge,
   * terminal state) runs and a fresh handle purges once more; on `ended` the
   * host retired the journal and keeps its rows. */
  function terminate(reason: "revoked" | "ended") {
    if (terminated) return
    terminated = reason
    const controller = editing?.controller
    editing = undefined; localCopy = undefined; stray = undefined
    server = { version: server.version, bytes: new Uint8Array() }
    note.value = ""; note.readOnly = true
    $("offers").replaceChildren(); $("recovery").hidden = true; $("compare").textContent = ""
    $<HTMLTextAreaElement>("copy").value = ""; $("versions").replaceChildren()
    alert.hidden = false
    alert.textContent = reason === "revoked" ? "Přístup ke čtení byl odebrán. Text byl z tohoto okna odstraněn." : "Úpravy byly ukončeny. Text byl z tohoto okna odstraněn."
    lifetime.abort()
    if (reason === "revoked") {
      if (controller) void controller.revokeRead().catch(() => {})
      void host.openJournal().then(journal => journal.revokeRead()).catch(() => {})
    }
    render()
  }
  function button(label: string, onClick: () => void) {
    const b = document.createElement("button"); b.type = "button"; b.textContent = label; b.addEventListener("click", onClick); return b
  }
  function render() {
    const view = lease.current
    if (terminated) {
      $("lease").textContent = "Pouze čtení"; $("lease-actions").replaceChildren()
      status.textContent = terminated === "revoked" ? "Přístup ke čtení byl odebrán" : "Úpravy ukončeny"
      $("keep-retry").hidden = $("keep-discard").hidden = true
      $<HTMLButtonElement>("save-now").disabled = true; $("reconcile").hidden = true
      return
    }
    if (view) {
      const c = describeLease(view, host.mayEdit)
      $("lease").textContent = c.label + (c.readOnly ? " · Pouze čtení" : "") + (c.draftKeptReadOnly ? " · Pouze čtení · místní koncept zachován" : "")
      const bar = $("lease-actions"); bar.replaceChildren()
      if (c.canAcquire) bar.append(button("Převzít úpravy", () => void run(acquire, "Převzetí nebylo potvrzeno.")))
      if (c.canRequestHandoff) bar.append(button("Požádat o předání", () => void run(async () => { lease.observe(await actions.requestHandoff()) }, "Žádost nebyla potvrzena.")))
      if (c.canRelease && editing) bar.append(button("Uvolnit úpravy", () => void run(() => transfer(), "Uvolnění nebylo potvrzeno. Koncept zůstává zachován.")))
      if (editing) for (const t of c.handoffTargets) bar.append(button(t.label, () => void run(() => transfer(t.request_id), "Předání nebylo potvrzeno. Koncept zůstává zachován.")))
    }
    // The SDK's own wording for a draft its journal could not store.
    status.textContent = localCopy ? saveLabel({ phase: "draft", dirty: true, reason: "storage" }) : editing ? saveLabel(editing.state) : "Pouze čtení"
    $("keep-retry").hidden = $("keep-discard").hidden = !localCopy
    $<HTMLButtonElement>("keep-retry").disabled = $<HTMLButtonElement>("keep-discard").disabled = busy
    if (localCopy && alert.hidden) fail(LOCAL_COPY_ONLY)
    $<HTMLButtonElement>("save-now").disabled = !editing || busy || retired(editing)
    $("reconcile").hidden = editing?.state.phase !== "uncertain"
  }
  async function load() {
    server = await doc.read(signal)
    note.value = decode(server.bytes)
  }
  /** A lifecycle that revoked its writing never writes again. */
  const retired = (e: typeof editing) => !!e && (e.state.phase === "read-only" || e.state.phase === "stopped")
  async function startEditing(generation: string) {
    const journal = await host.openJournal()
    if (signal.aborted || terminated) { await journal.close(); return }
    edits = 0
    let self: SaveController | undefined
    const controller = self = createSingleEditorSave({
      workspaceId: host.scope.workspaceId, pageId: host.scope.pageId, applicationId: NOTECRAFT_APPLICATION, enginePin: host.scope.enginePin,
      sessionId: host.scope.sessionId, generation, baseVersion: server.version, loaded: { bytes: server.bytes }, journal,
      engine: {
        snapshot: async () => ({ bytes: encode(note.value), editorRevision: `note:${generation}:${edits}` }),
        validate: async draft => { if (draft.bytes.byteLength > MAX_NOTE_BYTES) throw new Error("note_too_large"); decode(draft.bytes) },
      },
      // The SDK handles a read revocation itself and reports `revoked`.
      transport: host.document,
      onState: state => {
        // The SDK's terminal read revocation counts whenever it arrives, also
        // from a lifecycle NoteCraft already closed or replaced.
        if (state.phase === "revoked") { terminate("revoked"); return }
        if (!editing || editing.controller !== self) return
        editing.state = state
        if (state.phase === "stopped" && !selfClosed.has(self)) { terminate("ended"); return }
        if (retired(editing)) note.readOnly = true
        render()
      },
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
    const captured = await current.controller.revokeWrite().catch(() => false)
    // The lifecycle ended on the SDK's side (terminate already ran).
    const purged = current.state.phase === "stopped" || current.state.phase === "revoked"
    await closeController(current.controller).catch(() => {})
    if (editing === current) editing = undefined
    const base = server.version
    if (purged || terminated) return
    const latest = await doc.read(signal)
    if (terminated) return
    const unsaved = !clean && text !== decode(latest.bytes)
    // The lifecycle's journal holds a captured draft; otherwise store it now,
    // and never load the server version over text no journal holds.
    if (unsaved && !captured && !await preserve(text, current.generation, base, "Váš neuložený koncept")) {
      if (stray !== undefined) { note.value = stray; stray = undefined } // the newest text wins the note
      server = latest; return
    }
    if (!await keepStray(text, current.generation, base)) { server = latest; return }
    server = latest; note.value = decode(latest.bytes)
    if (unsaved && captured) keep(text, current.generation, base, "Váš neuložený koncept")
  }
  /** Stores text in a fresh journal row (compare/copy only, never a write).
   * On failure the text becomes the editable local copy and the alert says it
   * exists only in this window. */
  async function preserve(text: string, generation: string, baseVersion: string, label: string): Promise<boolean> {
    if (terminated) return false
    const draft: RecoveryDraft = { applicationId: NOTECRAFT_APPLICATION, enginePin: host.scope.enginePin, sessionId: host.scope.sessionId, generation, baseVersion, editorRevision: "local", bytes: encode(text) }
    try {
      const journal = await host.openJournal()
      try {
        if (terminated) return false
        await journal.persist(draft)
      } finally { await journal.close().catch(() => {}) }
      // Revoked while writing: the purge below erases this row too.
      if (terminated) { if (terminated === "revoked") void host.openJournal().then(j => j.revokeRead()).catch(() => {}); return false }
    } catch {
      if (terminated) return false
      localCopy = { generation, baseVersion }
      note.value = text; note.readOnly = false
      fail(LOCAL_COPY_ONLY); render()
      return false
    }
    keep(text, generation, baseVersion, label)
    return true
  }
  function keep(text: string, generation: string, baseVersion: string, label: string) {
    const draft: RecoveryDraft = { applicationId: NOTECRAFT_APPLICATION, enginePin: host.scope.enginePin, sessionId: host.scope.sessionId, generation, baseVersion, editorRevision: "local", bytes: encode(text) }
    offer([{ label, draft, compare: s => compareRecovery(s, draft), copy: () => draft.bytes.slice() }])
  }
  /** Input that reached a read-only note (no lifecycle captured it) is never
   * dropped silently: it is offered for copy and refused visibly. */
  let stray: string | undefined
  async function keepStray(before: string, generation: string, baseVersion: string): Promise<boolean> {
    const text = stray
    stray = undefined
    if (text === undefined || text === before) return true
    if (!await preserve(text, generation, baseVersion, "Text psaný bez úprav")) return false
    fail("Text napsaný během přepínání nebyl uložen; je nabídnut ke zkopírování.")
    return true
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
    // A retired lifecycle (e.g. a failed transfer after local revocation) is
    // replaced by a fresh one from the confirmed server version, never reused.
    if (editing && (!ownsLease(view, editing.generation) || retired(editing))) await stopEditing()
    if (!editing && !localCopy && host.mayEdit && ownsLease(view) && await keepStray(decode(server.bytes), view.generation!, server.version)) { await load(); await startEditing(view.generation!) }
    render(); await listVersions()
  }
  async function refresh() { await observe(lease.observe(await doc.lease(signal))) }
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
    // The note is confirmed at `base` right now; freeze it before any await.
    const before = note.value
    note.readOnly = true
    await closeController(current.controller)
    editing = undefined
    render()
    let kept = true
    try { await history.restore(target, { base, generation: current.generation }, signal) }
    finally { if (!terminated) kept = await keepStray(before, current.generation, base) }
    if (terminated) return
    if (kept) { await load(); await startEditing(current.generation) }
    render(); await listVersions()
  }

  note.addEventListener("input", () => {
    if (terminated) { note.value = ""; return }
    if (localCopy) return // the note itself is the local copy
    if (editing && !retired(editing) && !closing) { edits++; editing.controller.edited() } else stray = note.value
  })
  $("keep-retry").addEventListener("click", () => void run(async () => {
    const copy = localCopy, text = note.value
    if (!copy) return
    localCopy = undefined; note.readOnly = true
    if (!await preserve(text, copy.generation, copy.baseVersion, "Místní kopie") || !await keepStray(text, copy.generation, copy.baseVersion)) return
    await load(); await refresh()
  }, "Stav úprav se nepodařilo načíst."))
  $("keep-discard").addEventListener("click", () => void run(async () => { localCopy = undefined; stray = undefined; note.readOnly = true; await load(); await refresh() }, "Stav úprav se nepodařilo načíst."))
  $("save-now").addEventListener("click", () => { void editing?.controller.save(`notecraft-${++tickets}`).catch(() => {}) })
  $("reconcile").addEventListener("click", () => { void editing?.controller.reconcile() })
  const ready = (async () => {
    await load()
    const journal = await host.openJournal()
    try { offer(await recoveryOffers(journal)) } finally { await journal.close() }
    await refresh()
  })()
  /** Captures pending input before closing: the final save, else a journal
   * row. `local-copy` means some text exists only in this window (the note and
   * the alert keep it) and the host must not discard the view silently;
   * `revoked` means read access was removed and the text was wiped. */
  let closed: Promise<NoteCraftCloseResult> | undefined
  async function closeOnce(): Promise<NoteCraftCloseResult> {
    if (terminated) return "revoked"
    closing = true; note.readOnly = true
    const current = editing
    let durable = true
    if (current && current.state.phase !== "stopped" && !saveSettled(current.state)) {
      if (retired(current)) durable = await current.controller.revokeWrite().catch(() => false)
      else {
        try { await current.controller.flush() } catch { durable = await current.controller.revokeWrite().catch(() => false) }
      }
    }
    if (current) await closeController(current.controller).catch(() => {})
    editing = undefined
    // A read revocation during the final flush or capture reached terminate
    // through the SDK's `revoked` state: never a local copy, never durable.
    if (terminated) return "revoked"
    if (durable) durable = await keepStray(decode(server.bytes), current?.generation ?? lease.current?.generation ?? "1", server.version)
    else if (current && !localCopy) { localCopy = { generation: current.generation, baseVersion: server.version }; note.readOnly = false; fail(LOCAL_COPY_ONLY); render() }
    if (terminated) return "revoked"
    if (durable && !localCopy) { lifetime.abort(); return "durable" }
    // The view stays usable so the local copy can still be stored or discarded.
    closing = false; closed = undefined; render()
    return "local-copy"
  }
  return {
    ready,
    refresh: () => run(refresh, "Stav úprav se nepodařilo načíst."),
    /** See closeOnce and NoteCraftCloseResult. */
    close(): Promise<NoteCraftCloseResult> { return closed ??= closeOnce() },
  }
}
