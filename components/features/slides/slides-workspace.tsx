"use client"

import * as React from "react"
import type { SlideDeck } from "@/lib/slides/model"
import { diffToPatch, type Unrepresentable } from "@/lib/slides/operations"
import type { SlidesSaveClient, SlidesSnapshot } from "@/lib/slides/save-client"
import { durabilityOf, initialSaveState, losesWork, saveReducer, saveStateLabel, saveWordOf, type SaveEvent } from "@/lib/editor-lifecycle/save-state"
import { useLeaveGuard } from "@/lib/editor-lifecycle/use-leave-guard"
import { SlidesEditor } from "./slides-editor"

const control = "min-h-9 coarse:min-h-[44px] rounded border px-3 disabled:opacity-50"
const reasons: Record<Unrepresentable, string> = {
  layer_order: "layer order",
  slide_order: "slide order",
  deck_frame: "theme or slide size",
  deck_identity: "the deck identity or format",
  identity_reuse: "an element moved between slides or a reused id",
  order_version_missing: "slide structure (order version unknown)",
  operation_budget: "more than 100 changes at once",
  unreproduced: "changes the save format cannot reproduce",
}
type Phase =
  | { name: "loading" }
  | { name: "unavailable" }
  | { name: "not_implemented" }
  | { name: "failed"; message: string }
  | { name: "ready"; snapshot: SlidesSnapshot; draft: SlideDeck; conflict?: SlidesSnapshot; stale?: boolean; message?: string }

const randomId = () => globalThis.crypto?.randomUUID?.().replaceAll("-", "") ?? `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`

export interface SlidesWorkspaceProps {
  pageId: string
  client: SlidesSaveClient
  /**
   * Off by default and never turned on by an environment flag: the server has no save route
   * (backend decision F5-7), so editing is a local draft. Only tests with the in-memory
   * contract fixture enable it.
   */
  saveEnabled?: boolean
  /** Called with true while the local draft differs from the opened revision, and false on unmount. */
  onDirtyChange?: (dirty: boolean) => void
}

/**
 * Loads a deck and owns the saved snapshot, the local draft and the conflict states.
 * What may be claimed about the draft is the shared save lifecycle's: without a
 * save route (FE-REQ-F5-1) an edit is unsaved, and "Saved" needs a receipt (SX-02).
 */
export function SlidesWorkspace({ pageId, client, saveEnabled = false, onDirtyChange }: SlidesWorkspaceProps) {
  const [phase, setPhase] = React.useState<Phase>({ name: "loading" })
  const [lifecycle, dispatch] = React.useReducer(saveReducer, initialSaveState)
  const [attempt, setAttempt] = React.useState(0)
  const mutation = React.useRef<{ key: string; id: string }>(undefined)
  React.useEffect(() => {
    let active = true
    setPhase({ name: "loading" }); dispatch({ type: "reset" })
    client.load(pageId).then(result => {
      if (!active) return
      if ("deck" in result) { setPhase({ name: "ready", snapshot: result, draft: result.deck }); dispatch({ type: "opened", canWrite: true, canSend: saveEnabled }) }
      else if (result.status === "unavailable") setPhase(result.reason === "not_implemented" ? { name: "not_implemented" } : { name: "unavailable" })
      else setPhase({ name: "failed", message: result.status === "error" ? result.message : "Loading failed." })
    }, () => { if (active) setPhase({ name: "failed", message: "The presentation could not be loaded." }) })
    return () => { active = false }
  }, [client, pageId, attempt]) // eslint-disable-line react-hooks/exhaustive-deps -- saveEnabled is fixed per mount

  const ready = phase.name === "ready" ? phase : undefined
  const blocked = React.useMemo<Unrepresentable[]>(() => {
    if (!ready || !saveEnabled || JSON.stringify(ready.draft) === JSON.stringify(ready.snapshot.deck)) return []
    try { return diffToPatch(ready.snapshot.deck, ready.draft, { pageId, schemaVersion: ready.snapshot.deck.schema_version, baseRev: ready.snapshot.rev, orderVersion: ready.snapshot.orderVersion, mutationId: "preview" }).unrepresentable } catch { return ["unreproduced"] }
  }, [ready?.draft, ready?.snapshot, saveEnabled, pageId]) // eslint-disable-line react-hooks/exhaustive-deps

  useLeaveGuard(losesWork(lifecycle), onDirtyChange)

  if (phase.name === "loading") return <p role="status">Loading presentation…</p>
  if (phase.name === "unavailable") return <p role="alert">This presentation is not available.</p>
  if (phase.name === "not_implemented") return <p role="alert">Slides editing is not available on this server yet. The presentation cannot be loaded or saved here.</p>
  if (phase.name === "failed") return <div role="alert" className="space-y-2"><p>{phase.message}</p><button className={control} onClick={() => setAttempt(value => value + 1)}>Try again</button></div>

  const { snapshot, draft, conflict } = phase
  const dirty = JSON.stringify(draft) !== JSON.stringify(snapshot.deck)
  const saving = lifecycle.sending, saved = saveWordOf(lifecycle) === "saved"
  const context = { pageId, schemaVersion: snapshot.deck.schema_version, baseRev: snapshot.rev, orderVersion: snapshot.orderVersion }
  const key = JSON.stringify(draft)
  const loadLatest = (latest: SlidesSnapshot) => {
    mutation.current = undefined; setPhase({ name: "ready", snapshot: latest, draft: latest.deck })
    dispatch({ type: "reset" }); dispatch({ type: "opened", canWrite: true, canSend: saveEnabled })
  }
  const edit = (next: SlideDeck) => {
    setPhase({ ...phase, draft: next, message: undefined })
    dispatch(JSON.stringify(next) !== JSON.stringify(snapshot.deck) ? { type: "edit", pending: 1 } : { type: "recount", pending: 0 })
  }
  const submit = async () => {
    // The same draft keeps its mutation id across retries; a changed draft gets a new one.
    if (mutation.current?.key !== key) mutation.current = { key, id: `m_${randomId()}` }
    let body
    try { body = diffToPatch(snapshot.deck, draft, { ...context, mutationId: mutation.current.id }).request } catch { body = null }
    if (!body) return
    const mutationId = mutation.current.id
    setPhase({ ...phase, message: undefined }); dispatch({ type: "send" })
    const result = await client.patch(body)
    // Only a receipt for this mutation is a save; anything else keeps the draft unsaved.
    const receipted = result.status === "saved" && result.mutationId === mutationId
    const event: SaveEvent = receipted ? { type: "ack", pending: 0 }
      : result.status === "saved" ? { type: "unconfirmed" }
      : result.status === "conflict" ? { type: "conflict" }
      : result.status === "unavailable" ? (result.reason === "not_implemented" ? { type: "rejected" } : { type: "denied" })
      : result.retryable ? { type: "transient-fail" } : { type: "rejected" }
    dispatch(event)
    setPhase(current => {
      if (current.name !== "ready") return current
      if (receipted) return { name: "ready", snapshot: result.snapshot, draft: result.snapshot.deck }
      if (result.status === "saved") return { ...current, message: "Saving could not be confirmed. Your draft is kept; try again." }
      if (result.status === "conflict") return { ...current, conflict: result.current }
      // A missing route is not a revoked page: the draft stays and nothing is claimed as saved.
      if (result.status === "unavailable" && result.reason === "not_implemented") return { ...current, message: "Saving is not available on this server yet. Your changes were not saved; the draft is kept here." }
      // The page may be gone or revoked, but the draft is the user's work: keep it with a neutral message.
      if (result.status === "unavailable") return { ...current, message: result.reason === "signed_out" ? "Your session has ended. Sign in again to save; the draft is kept here." : "You can't save this presentation. Your changes were not saved; the draft is kept here." }
      return { ...current, message: result.retryable ? "Saving failed. Your draft is kept; try again." : `This change cannot be saved: ${result.message}` }
    })
  }
  const sentence = !saveEnabled ? (dirty ? `Local draft, not saved. Revision ${snapshot.rev} is the version you opened.` : `Revision ${snapshot.rev}.`)
    : saving ? "Saving…"
    : blocked.length ? `Changes to ${blocked.map(reason => reasons[reason]).join(", ")} cannot be saved yet. They stay in this draft only.`
    : dirty ? "Unsaved changes." : saved ? `Saved as revision ${snapshot.rev}.` : `Revision ${snapshot.rev}.`
  // The same durability words as Document: an edit without a receipt is never shown as saved.
  const label = saveStateLabel(durabilityOf(lifecycle), saved)
  const status = label ? `${sentence} · ${label}` : sentence
  return <div className="space-y-3">
    {conflict && !phase.stale && <div role="alert" className="space-y-2">
      <p>Someone else changed this presentation (now revision {conflict.rev}). Your draft was not saved and nothing was overwritten.</p>
      <div className="flex flex-wrap gap-2">
        <button className={control} onClick={() => loadLatest(conflict)}>Load latest and discard my draft</button>
        <button className={control} onClick={() => setPhase({ ...phase, stale: true })}>Keep editing locally</button>
      </div>
    </div>}
    {phase.stale && conflict && <div role="status" className="space-y-2">
      <p>You are editing a draft based on an older revision. Saving stays blocked until you load the latest version.</p>
      <button className={control} onClick={() => loadLatest(conflict)}>Load latest and discard my draft</button>
    </div>}
    {phase.message && <p role="alert">{phase.message}</p>}
    <SlidesEditor deck={draft} onChange={edit} disabled={saving} persistenceStatus={status} persistenceNote={saveEnabled ? undefined : "Saving is not available yet: edits stay in this tab and are lost on reload."}
      onSave={saveEnabled ? () => void submit() : undefined} saveDisabled={!dirty || blocked.length > 0 || saving || Boolean(conflict) || Boolean(phase.stale)} />
  </div>
}
