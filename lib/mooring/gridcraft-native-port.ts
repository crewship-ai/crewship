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
  const files = new Map<string, { file: GridcraftSavedFile; hash: Promise<string> }>()
  let lastAcknowledged = BigInt(scope.baseVersion), dirty = false, opened = false
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
    const bytes = file.bytes.slice()
    files.set(file.requestId, { file: { ...file, bytes }, hash: crypto.subtle.digest("SHA-256", bytes).then(digest => `sha256:${hex(digest)}`) })
    if (files.size > 16) files.delete(files.keys().next().value!)
  }
  const inner = createGridcraftPort({
    bundle: options.bundle, frame: options.frame, createChannel: options.createChannel, timeoutMs: options.timeoutMs,
    onChanged(change) {
      dirty = change.dirty; current = revisionOf(change); options.onChanged(current, change.document)
      if (change.calculated === true && !calculated) { calculated = true; try { options.onCalculated?.() } catch { /* reporting only */ } }
    },
    onDirty(value) { dirty = value },
    onSaveRefused(refusal) {
      const document = inner.document
      if (document !== undefined && refusal.code === "fidelity_loss") refuse(`${document}:${refusal.revision}`, new GridcraftSaveRefused(refusal.code, refusal.parts, refusal.bytes))
    },
    onSave(file) { remember(file); options.onSaveRequest({ requestId: file.requestId, editorRevision: revisionOf(file), documentId: file.document }) },
    onExport: file => options.onExport?.(file),
    onError: reason => options.onError(reason),
    // The editor's engine build goes along: without it no replay over unsaved work runs.
    onReady: (version, build) => build === undefined ? options.onReady?.(version) : options.onReady?.(version, build),
    onEditAttempt: kind => options.onEditAttempt?.(kind),
    onHeldInputDropped: reason => options.onHeldInputDropped?.(reason),
  })
  // The editor's file of an unedited workbook is not the stored file byte for byte, so the
  // coordinator compares the editor's revision instead (its nativeState, mounted at open).
  const snapshotOf = (file: GridcraftSavedFile): NativeEditorSnapshot => ({ bytes: file.bytes.slice(), editorRevision: revisionOf(file), documentId: file.document, nativeState: revisionOf(file), format: file.format, ...(file.touched !== undefined ? { touched: file.touched } : {}) })
  return {
    loaded: () => inner.loaded(),
    handleWindowMessage: event => inner.handleWindowMessage(event),
    async bootstrap(bytes, signal, timeoutMs = 30_000) {
      if (opened) throw new Error("native_bootstrap_unavailable")
      if (options.applicationId !== "gridcraft") throw new Error("invalid_native_port")
      const result = await inner.open({ name: options.fileName, bytes }, { calculate: false, autoFit: false }, signal, timeoutMs)
      if (!options.fileName.endsWith("." + result.format)) throw new Error("native_workbook_format_mismatch")
      if (!options.fileName.toLowerCase().endsWith("." + result.format)) throw new Error("native_workbook_format_mismatch")
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
        return snapshotOf(kept.file)
      }
      // Autosave asks the editor once per revision after a fidelity refusal; Ctrl+S in the editor always asks.
      if (refused && refused.revision === current) throw refused.error
      const asked = current
      let file: GridcraftSavedFile
      try { file = await inner.requestSave(signal, saveTimeoutMs) }
      catch (error) { if (error instanceof GridcraftSaveRefused && error.code === "fidelity_loss" && asked !== undefined) refuse(asked, error); throw error }
      remember(file)
      return snapshotOf(file)
    },
    async validate() { if (!opened) throw new Error("native_validation_unavailable") },
    async ack(receipt) {
      if (!receipt || typeof receipt !== "object") return false
      const saved = { ...receipt }
      const unchanged = saved.unchanged === true
      if ("unchanged" in saved && !unchanged) return false
      if (saved.applicationId !== "gridcraft" || saved.enginePin !== options.enginePin || saved.workspaceId !== scope.workspaceId || saved.pageId !== scope.pageId || saved.sessionId !== scope.sessionId || saved.generation !== scope.generation
        || saved.issuedBy !== "crewship" || saved.durability !== "node" || saved.phase !== "committed" || saved.candidateHash !== saved.contentHash
        || !validNativeVersion(saved.baseVersion) || !validNativeVersion(saved.version) || BigInt(saved.version) !== BigInt(saved.baseVersion) + (unchanged ? 0n : 1n)
        || (unchanged ? BigInt(saved.version) < lastAcknowledged : BigInt(saved.version) <= lastAcknowledged)) return false
      // The newest file of this revision whose bytes are exactly the ones the server stored.
      let match: string | undefined
      for (const [id, kept] of files) {
        if (revisionOf(kept.file) !== saved.editorRevision) continue
        try { if (await kept.hash === saved.contentHash) match = id } catch { return false }
      }
      if (match === undefined) return false
      // Other files of the same revision are settled first, so the editor ends saved, not failed.
      for (const [id, kept] of [...files]) if (id !== match && revisionOf(kept.file) === saved.editorRevision) { inner.saveFailed(id, "conflict"); files.delete(id) }
      if (!inner.ack(match, saved.version)) return false
      files.delete(match); lastAcknowledged = BigInt(saved.version)
      if (refused) { refused = undefined; report(null) }
      return true
    },
    saveFailed(requestId) {
      const sent = inner.saveFailed(requestId, "failed")
      if (sent) files.delete(requestId)
      return sent
    },
    setReadOnly: (readOnly, signal) => inner.setReadOnly(readOnly, signal),
    rebind(next) {
      if (!opened || dirty || !next || next.sessionId !== scope.sessionId) return false
      if (!validNativeVersion(next.generation) || next.generation === "0" || BigInt(next.generation) <= BigInt(scope.generation)) return false
      if (!validNativeVersion(next.baseVersion) || BigInt(next.baseVersion) < lastAcknowledged) return false
      scope.generation = next.generation; scope.baseVersion = next.baseVersion; lastAcknowledged = BigInt(next.baseVersion)
      // Files of the earlier generation prove nothing under the new one.
      for (const id of [...files.keys()]) { inner.saveFailed(id, "conflict"); files.delete(id) }
      return true
    },
    execute: (commands, executeOptions, signal) => inner.execute(commands, executeOptions, signal),
    close: () => inner.close(),
  }
}
