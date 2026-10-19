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
  operation_budget: "more than 100 changes at once",
}
type Phase =
  | { name: "loading" }
  | { name: "unavailable" }
  | { name: "failed"; message: string }
  | { name: "ready"; snapshot: SlidesSnapshot; draft: SlideDeck; save: "idle" | "saving" | "saved" | "failed"; conflict?: SlidesSnapshot; stale?: boolean; message?: string }

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
      else if (result.status === "unavailable") setPhase({ name: "unavailable" })
      else setPhase({ name: "failed", message: result.status === "error" ? result.message : "Loading failed." })
    }, () => { if (active) setPhase({ name: "failed", message: "The presentation could not be loaded." }) })
    return () => { active = false }
  }, [client, pageId, attempt])

  if (phase.name === "loading") return <p role="status">Loading presentation…</p>
  if (phase.name === "unavailable") return <p role="alert">This presentation is not available.</p>
  if (phase.name === "failed") return <div role="alert" className="space-y-2"><p>{phase.message}</p><button className={control} onClick={() => setAttempt(value => value + 1)}>Try again</button></div>

  const { snapshot, draft, save, conflict } = phase
  const dirty = JSON.stringify(draft) !== JSON.stringify(snapshot.deck)
  const context = { pageId, schemaVersion: snapshot.deck.schema_version, baseRev: snapshot.rev, orderVersion: snapshot.orderVersion }
  const key = JSON.stringify(draft)
  const request = () => {
    if (mutation.current?.key !== key) mutation.current = { key, id: `m_${crypto.randomUUID().replaceAll("-", "")}` }
    return diffToPatch(snapshot.deck, draft, { ...context, mutationId: mutation.current.id })
  }
  const blocked = dirty && saveEnabled ? request().unrepresentable : []
  const submit = async () => {
    const { request: body } = request()
    if (!body) return
    setPhase({ ...phase, save: "saving", message: undefined })
    const result = await client.patch(body)
    setPhase(current => {
      if (current.name !== "ready") return current
      if (result.status === "saved") return { name: "ready", snapshot: result.snapshot, draft: result.snapshot.deck, save: "saved" }
      if (result.status === "conflict") return { ...current, save: "failed", conflict: result.current }
      if (result.status === "unavailable") return { name: "unavailable" }
      return { ...current, save: "failed", message: result.retryable ? "Saving failed. Your draft is kept; try again." : `This change cannot be saved: ${result.message}` }
    })
  }
  const status = !saveEnabled ? "Saving is not available yet. Changes in this preview are not saved."
    : save === "saving" ? "Saving…"
    : blocked.length ? `Changes to ${blocked.map(reason => reasons[reason]).join(", ")} cannot be saved yet. They stay in this draft only.`
    : dirty ? "Unsaved changes." : save === "saved" ? `Saved as revision ${snapshot.rev}.` : `Revision ${snapshot.rev}.`
  return <div className="space-y-3">
    {conflict && <div role="alert" className="space-y-2">
      <p>Someone else changed this presentation (now revision {conflict.rev}). Your draft was not saved and nothing was overwritten.</p>
      <div className="flex flex-wrap gap-2">
        <button className={control} onClick={() => { mutation.current = undefined; setPhase({ name: "ready", snapshot: conflict, draft: conflict.deck, save: "idle" }) }}>Load latest and discard my draft</button>
        <button className={control} onClick={() => setPhase({ ...phase, conflict: undefined, stale: true })}>Keep editing locally</button>
      </div>
    </div>}
    {phase.stale && <p role="status">You are editing a draft based on an older revision. Saving stays blocked until you load the latest version.</p>}
    {phase.message && <p role="alert">{phase.message}</p>}
    <SlidesEditor deck={draft} onChange={next => setPhase({ ...phase, draft: next, save: "idle", message: undefined })} disabled={save === "saving"} persistenceStatus={status}
      onSave={saveEnabled ? () => void submit() : undefined} saveDisabled={!dirty || blocked.length > 0 || save === "saving" || Boolean(conflict) || Boolean(phase.stale)} />
  </div>
}
