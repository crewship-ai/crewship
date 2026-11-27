// The host's editor port (NativeEditorPort) on GridCraft protocol 2. The save coordinator,
// lease, journal and agent replay keep their interface; underneath, the editor opens the
// stored workbook file and a save is the editor's own file of one revision.
//   bootstrap → open the file (no recalculation, row heights from the file), no snapshot after
//   snapshot  → the editor's saved file (host save-request, or the one the person started);
//               nativeState is the editor's revision, so an untouched workbook is not saved again
//   validate  → nothing to do: the server validates the file and computes its hash
//   ack       → save-ack with the stored version, only for the file whose hash the receipt names
import { createGridcraftPort, GridcraftSaveRefused, type GridcraftFormat, type GridcraftOpened, type GridcraftSavedFile, type GridcraftWarning } from "./gridcraft-port"
import { validNativeVersion } from "./native-draft"
import type { NativeEditorPort, NativeEditorPortOptions, NativeEditorSnapshot } from "./native-editor-port"

export interface GridcraftNativePortOptions extends NativeEditorPortOptions {
  /** The stored file's name; its extension (.xlsx or .xlsm) is the format the editor opens. */
  fileName: string
  onOpened?(opened: GridcraftOpened): void
  /** Once: the editor has calculated every value itself since the open, so none is only the file's saved value any more. */
  onCalculated?(): void
  /** Deadline for the editor to hand over a saved file. */
  saveTimeoutMs?: number
  /** The editor would not save this revision without dropping parts of the workbook (null once a save is stored). */
  onSaveRefused?(refusal: GridcraftNativeRefusal | null): void
}
/** With `bytes`: the file as the editor would write it at `editorRevision`, lacking `parts`. For a local draft only. */
export interface GridcraftNativeRefusal { code: string; parts: GridcraftWarning[]; bytes?: Uint8Array; format?: GridcraftFormat; editorRevision?: string }

const hex = (digest: ArrayBuffer) => Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")

export function createGridcraftNativePort(options: GridcraftNativePortOptions): NativeEditorPort {
  const scope = { ...options.scope }
  const saveTimeoutMs = options.saveTimeoutMs ?? 120_000
  // Files the editor gave, by request id, each with the hash of its exact bytes.
  type Capture = { file: GridcraftSavedFile; hash?: string }
  const files = new Map<string, Capture>()
  let lastAcknowledged = BigInt(scope.baseVersion), opened = false
  // The editor's current revision, and the one it refused to save (fidelity_loss holds per revision).
  let current: string | undefined, refused: { revision: string; error: GridcraftSaveRefused } | undefined
  let openedFormat: GridcraftFormat | undefined, calculated = false
  const report = (value: GridcraftNativeRefusal | null) => { try { options.onSaveRefused?.(value) } catch { /* reporting only */ } }
  function refuse(revision: string, error: GridcraftSaveRefused) {
    refused = { revision, error }
    report({ code: error.code, parts: error.parts, ...(error.bytes && openedFormat ? { format: openedFormat, editorRevision: revision, bytes: error.bytes.slice() } : {}) })
  }
  const revisionOf = (file: { document: string; revision: string }) => `${file.document}:${file.revision}`
  function remember(file: GridcraftSavedFile) {
    const capture: Capture = { file: { ...file, bytes: file.bytes.slice() } }
    files.set(file.requestId, capture)
    if (files.size > 16) files.delete(files.keys().next().value!)
    return capture
  }
  const inner = createGridcraftPort({
    bundle: options.bundle, frame: options.frame, createChannel: options.createChannel, timeoutMs: options.timeoutMs,
    onChanged(change) {
      current = revisionOf(change); if (change.dirty) options.onChanged(current, change.document)
      if (change.calculated === true && !calculated) { calculated = true; try { options.onCalculated?.() } catch { /* reporting only */ } }
    },
    onSaveRefused(refusal) {
      const document = inner.document
      if (document !== undefined && refusal.code === "fidelity_loss") refuse(`${document}:${refusal.revision}`, new GridcraftSaveRefused(refusal.code, refusal.parts, refusal.bytes))
    },
    onSave(file) { remember(file); options.onSaveRequest({ requestId: file.requestId, editorRevision: revisionOf(file), documentId: file.document }) },
    onExport: file => options.onExport?.(file),
    onError: reason => options.onError(reason),
    onReady: version => options.onReady?.(version),
  })
  // The editor's file of an unedited workbook is not the stored file byte for byte, so the
  // coordinator compares the editor's revision instead (its nativeState, mounted at open).
  async function snapshotOf(capture: Capture): Promise<NativeEditorSnapshot> {
    const { file } = capture
    const bytes = new Uint8Array(file.bytes)
    capture.hash = `sha256:${hex(await crypto.subtle.digest("SHA-256", bytes))}`
    const revision = revisionOf(file)
    return { bytes, editorRevision: revision, documentId: file.document, nativeState: revision, captureId: file.requestId }
  }
  function acknowledgementFailed(reason: string): false {
    console.warn("gridcraft_save_ack_unconfirmed", reason)
    return false
  }
  return {
    loaded: () => inner.loaded(),
    handleWindowMessage: event => inner.handleWindowMessage(event),
    async bootstrap(bytes, signal, timeoutMs = 30_000) {
      if (opened) throw new Error("native_bootstrap_unavailable")
      if (options.applicationId !== "gridcraft") throw new Error("invalid_native_port")
      const result = await inner.open({ name: options.fileName, bytes }, { calculate: false, autoFit: false }, signal, timeoutMs)
      if (!options.fileName.endsWith("." + result.format)) throw new Error("native_workbook_format_mismatch")
      opened = true; openedFormat = result.format
      try { options.onOpened?.(result) } catch { /* reporting only */ }
      const revision = revisionOf(result)
      current = revision
      return { bytes: bytes.slice(), editorRevision: revision, documentId: result.document, revision, nativeState: revision }
    },
    async snapshot(signal, requestId) {
      if (!opened) throw new Error("native_bootstrap_required")
      if (requestId !== undefined) {
        const kept = files.get(requestId)
        if (!kept || kept.file.source !== "editor") throw new Error("unknown_native_save_ticket")
        return snapshotOf(kept)
      }
      // Autosave asks the editor once per revision after a fidelity refusal; Ctrl+S in the editor always asks.
      if (refused && refused.revision === current) throw refused.error
      const asked = current
      let file: GridcraftSavedFile
      try { file = await inner.requestSave(signal, saveTimeoutMs) }
      catch (error) { if (error instanceof GridcraftSaveRefused && error.code === "fidelity_loss" && asked !== undefined) refuse(asked, error); throw error }
      return snapshotOf(remember(file))
    },
    async validate() { if (!opened) throw new Error("native_validation_unavailable") },
    async ack(receipt, captureId) {
      if (!receipt || typeof receipt !== "object") return acknowledgementFailed("receipt")
      const saved = { ...receipt }
      const unchanged = saved.unchanged === true
      if ("unchanged" in saved && !unchanged) return acknowledgementFailed("unchanged")
      if (saved.applicationId !== "gridcraft" || saved.enginePin !== options.enginePin || saved.workspaceId !== scope.workspaceId || saved.pageId !== scope.pageId || saved.sessionId !== scope.sessionId || saved.generation !== scope.generation
        || saved.issuedBy !== "crewship" || saved.durability !== "node" || saved.phase !== "committed" || saved.candidateHash !== saved.contentHash
        || !validNativeVersion(saved.baseVersion) || !validNativeVersion(saved.version) || BigInt(saved.version) !== BigInt(saved.baseVersion) + (unchanged ? 0n : 1n)) return acknowledgementFailed("binding")
      const superseded = () => unchanged ? BigInt(saved.version) < lastAcknowledged : BigInt(saved.version) <= lastAcknowledged
      const kept = captureId === undefined ? undefined : files.get(captureId)
      if (captureId === undefined || !kept || revisionOf(kept.file) !== saved.editorRevision) return acknowledgementFailed("capture")
      // Autosave and Ctrl+S can export the same revision concurrently. Each
      // reply settles only the ticket whose bytes were used for that save.
      if (kept.hash !== saved.contentHash) return acknowledgementFailed("hash")
      if (superseded()) return acknowledgementFailed("version")
      if (!inner.ack(captureId, saved.version)) return acknowledgementFailed("port_ticket")
      files.delete(captureId); lastAcknowledged = BigInt(saved.version)
      if (refused) { refused = undefined; report(null) }
      return true
    },
    saveFailed(requestId) {
      const sent = inner.saveFailed(requestId, "failed")
      if (sent) files.delete(requestId)
      return sent
    },
    setReadOnly: (readOnly, signal) => inner.setReadOnly(readOnly, signal),
    close: () => inner.close(),
  }
}
