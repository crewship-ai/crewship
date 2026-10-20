"use client"

import * as React from "react"
import type { SlideDeck } from "@/lib/slides/model"
import { diffToPatch, type Unrepresentable } from "@/lib/slides/operations"
import { slidesEditorSave, type SlidesSaveClient, type SlidesSnapshot } from "@/lib/slides/save-client"
import { SlidesEditor } from "./slides-editor"

const control = "min-h-9 coarse:min-h-12 rounded border px-3 disabled:opacity-50"
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
  | { name: "ready"; snapshot: SlidesSnapshot; draft: SlideDeck; save: "idle" | "saving" | "saved" | "failed"; conflict?: SlidesSnapshot; stale?: boolean; message?: string }

const randomId = () => globalThis.crypto?.randomUUID?.().replaceAll("-", "") ?? `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`

export interface SlidesWorkspaceProps {
  pageId: string
  client: SlidesSaveClient
  /** Defaults to the NEXT_PUBLIC_SLIDES_EDITOR_SAVE flag. */
  saveEnabled?: boolean
}

/** Loads a deck and owns the saved snapshot, the local draft and the save/conflict states. */
export function SlidesWorkspace({ pageId, client, saveEnabled = slidesEditorSave() }: SlidesWorkspaceProps) {
  const [phase, setPhase] = React.useState<Phase>({ name: "loading" })
  const [attempt, setAttempt] = React.useState(0)
  const mutation = React.useRef<{ key: string; id: string }>(undefined)
  React.useEffect(() => {
    let active = true
    setPhase({ name: "loading" })
    client.load(pageId).then(result => {
      if (!active) return
      if ("deck" in result) setPhase({ name: "ready", snapshot: result, draft: result.deck, save: "idle" })
      else if (result.status === "unavailable") setPhase(result.reason === "not_implemented" ? { name: "not_implemented" } : { name: "unavailable" })
      else setPhase({ name: "failed", message: result.status === "error" ? result.message : "Loading failed." })
    }, () => { if (active) setPhase({ name: "failed", message: "The presentation could not be loaded." }) })
    return () => { active = false }
  }, [client, pageId, attempt])

  const ready = phase.name === "ready" ? phase : undefined
  const blocked = React.useMemo<Unrepresentable[]>(() => {
    if (!ready || !saveEnabled || JSON.stringify(ready.draft) === JSON.stringify(ready.snapshot.deck)) return []
    try { return diffToPatch(ready.snapshot.deck, ready.draft, { pageId, schemaVersion: ready.snapshot.deck.schema_version, baseRev: ready.snapshot.rev, orderVersion: ready.snapshot.orderVersion, mutationId: "preview" }).unrepresentable } catch { return ["unreproduced"] }
  }, [ready?.draft, ready?.snapshot, saveEnabled, pageId]) // eslint-disable-line react-hooks/exhaustive-deps

  if (phase.name === "loading") return <p role="status">Loading presentation…</p>
  if (phase.name === "unavailable") return <p role="alert">This presentation is not available.</p>
  if (phase.name === "not_implemented") return <p role="alert">Slides editing is not available on this server yet. The presentation cannot be loaded or saved here.</p>
  if (phase.name === "failed") return <div role="alert" className="space-y-2"><p>{phase.message}</p><button className={control} onClick={() => setAttempt(value => value + 1)}>Try again</button></div>

  const { snapshot, draft, save, conflict } = phase
  const dirty = JSON.stringify(draft) !== JSON.stringify(snapshot.deck)
  const context = { pageId, schemaVersion: snapshot.deck.schema_version, baseRev: snapshot.rev, orderVersion: snapshot.orderVersion }
  const key = JSON.stringify(draft)
  const loadLatest = (latest: SlidesSnapshot) => { mutation.current = undefined; setPhase({ name: "ready", snapshot: latest, draft: latest.deck, save: "idle" }) }
  const submit = async () => {
    // The same draft keeps its mutation id across retries; a changed draft gets a new one.
    if (mutation.current?.key !== key) mutation.current = { key, id: `m_${randomId()}` }
    let body
    try { body = diffToPatch(snapshot.deck, draft, { ...context, mutationId: mutation.current.id }).request } catch { body = null }
    if (!body) return
    setPhase({ ...phase, save: "saving", message: undefined })
    const result = await client.patch(body)
    setPhase(current => {
      if (current.name !== "ready") return current
      if (result.status === "saved") return { name: "ready", snapshot: result.snapshot, draft: result.snapshot.deck, save: "saved" }
      if (result.status === "conflict") return { ...current, save: "failed", conflict: result.current }
      // A missing route is not a revoked page: the draft stays and nothing is claimed as saved.
      if (result.status === "unavailable" && result.reason === "not_implemented") return { ...current, save: "failed", message: "Saving is not available on this server yet. Your changes were not saved; the draft is kept here." }
      if (result.status === "unavailable") return { name: "unavailable" }
      return { ...current, save: "failed", message: result.retryable ? "Saving failed. Your draft is kept; try again." : `This change cannot be saved: ${result.message}` }
    })
  }
  const status = !saveEnabled ? "Saving is not available yet. Changes in this preview are not saved."
    : save === "saving" ? "Saving…"
    : blocked.length ? `Changes to ${blocked.map(reason => reasons[reason]).join(", ")} cannot be saved yet. They stay in this draft only.`
    : dirty ? "Unsaved changes." : save === "saved" ? `Saved as revision ${snapshot.rev}.` : `Revision ${snapshot.rev}.`
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
    <SlidesEditor deck={draft} onChange={next => setPhase({ ...phase, draft: next, save: "idle", message: undefined })} disabled={save === "saving"} persistenceStatus={status}
      onSave={saveEnabled ? () => void submit() : undefined} saveDisabled={!dirty || blocked.length > 0 || save === "saving" || Boolean(conflict) || Boolean(phase.stale)} />
  </div>
}
