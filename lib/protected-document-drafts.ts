import { openDocumentDrafts, purgeDocumentDrafts, captureDocumentDrafts, removeCapturedDocumentDrafts, type DocumentDraft, type DocumentDraftStore } from "./document-queue"
import { createLocalDraftCipher, isSealedLocalDraft, LocalDraftProtectionError, LocalDraftReadRevoked } from "./local-draft-seal"

/** The raw IDB layer keeps its locking and strict transactions; only encrypted
 * document payloads cross that boundary in production. */
export async function openProtectedDocumentDrafts(human: string, workspace: string, page: string, options?: { maxDraftBytes?: number; requireExclusive?: boolean }): Promise<DocumentDraftStore> {
  const scope = { human, workspace, page }
  const limit = options?.maxDraftBytes ?? 4 * 1024 * 1024
  // Acquire custody before checking/erasing recovery data. No content leaves
  // the store until the separate current-human read admission succeeds.
  const raw = await openDocumentDrafts(human, workspace, page, { ...options, maxDraftBytes: Math.min(64 * 1024 * 1024, Math.ceil(limit * 4 / 3) + 8192) })
  let rawClosed = false
  const closeRaw = () => { if (!rawClosed) { rawClosed = true; raw.close() } }
  const captured = raw.exclusive ? undefined : await captureDocumentDrafts(human, workspace, page).catch(error => { closeRaw(); throw error })
  async function purgeAndDeny(error: LocalDraftReadRevoked): Promise<never> {
    try { if (captured) await removeCapturedDocumentDrafts(captured); else await purgeDocumentDrafts(human, workspace, page) }
    catch { throw new LocalDraftReadRevoked("Page read was removed; local deletion could not be confirmed.") }
    finally { closeRaw() }
    throw error
  }
  const cipher = await createLocalDraftCipher(scope).catch(async error => {
    if (error instanceof LocalDraftReadRevoked) return purgeAndDeny(error)
    closeRaw(); throw error
  })
  const purpose = (draft: DocumentDraft) => JSON.stringify(["document", draft.id, draft.schema, draft.epoch])
  const close = () => { cipher.close(); closeRaw() }
  const protect = async (draft: DocumentDraft) => {
    const text = JSON.stringify(draft)
    if (new TextEncoder().encode(text).length > limit) throw new LocalDraftProtectionError("Private draft exceeds its storage limit.")
    return { id: draft.id, schema: draft.schema, epoch: draft.epoch, attempted: false, update: "", snapshot: await cipher.seal(text, purpose(draft)) }
  }
  return {
    async load() {
      const rows = await raw.load(), drafts: DocumentDraft[] = []
      try {
        for (const row of rows) {
          if (isSealedLocalDraft(row.snapshot)) {
            const draft: DocumentDraft = JSON.parse(await cipher.open(row.snapshot, purpose(row)))
            if (!draft || draft.id !== row.id || draft.schema !== row.schema || draft.epoch !== row.epoch) throw new LocalDraftProtectionError("Draft scope changed.")
            drafts.push(draft)
          } else {
            // A legacy row is exposed only after current server admission and
            // after its encrypted replacement has durably overwritten it.
            await raw.put(await protect(row)); drafts.push(row)
          }
        }
        return drafts
      } catch (error) {
        if (error instanceof LocalDraftReadRevoked) { cipher.close(); return purgeAndDeny(error) }
        throw error
      }
    },
    async put(draft) { await raw.put(await protect(draft)) },
    remove: id => raw.remove(id),
    close,
    exclusive: raw.exclusive,
  }
}
