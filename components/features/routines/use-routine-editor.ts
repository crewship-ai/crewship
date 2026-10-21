"use client"

import * as React from "react"
import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import { extractProblemDetail } from "@/lib/problem-details"
import { renamePayload } from "@/lib/routine-save-payload"
import { loadRoutineDraft, saveRoutineDraft, RoutineDraftError, type RoutineDraft } from "@/lib/routine-drafts"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import { asString, isRecord } from "@/lib/routine-step-describe"
import type { RoutineDetail } from "./routines-detail-panel"

// The routine page is the editor (Harbor 2: Steps, Triggers and Settings with
// a save bar — the Edit dialog is gone). One editor per page, shared by the
// Steps and Settings tabs, so both edit the same document and one save bar
// saves it.
//
// Identity (name and purpose) applies at once through the rename path when it
// is the only change: it does not change how the routine runs. Everything else
// — inputs, steps, prompts, decisions, limits, the chat command — is a
// definition change and becomes a draft through the drafts API the CLI and the
// lead use (POST …/pipelines/drafts, compare-and-set on the revision the edit
// started from). The published version keeps running until the draft is
// published.

const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value))
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

export interface RoutineEditor {
  loading: boolean
  /** The draft baseline could not be loaded; saving is refused. */
  unavailable: boolean
  name: string
  setName: (v: string) => void
  description: string
  setDescription: (v: string) => void
  appearance: { icon: string; color: string }
  saveAppearance: (next: Partial<{ icon: string; color: string }>) => Promise<void>
  doc: Record<string, unknown>
  update: (patch: (next: Record<string, unknown>) => void) => void
  setDoc: (next: Record<string, unknown>) => void
  identityDirty: boolean
  definitionDirty: boolean
  /** Fields and steps that differ from what was loaded; the save bar counts these. */
  changeCount: number
  dirty: boolean
  /** The save will create (or continue) a draft rather than apply at once. */
  saveAsDraft: boolean
  nextRevision: number
  published: boolean
  busy: boolean
  refusal: string | null
  setRefusal: (v: string | null) => void
  save: () => Promise<boolean>
  reset: () => void
}

export function useRoutineEditor(workspaceId: string, routine: RoutineDetail, onChanged: () => void, enabled = true): RoutineEditor {
  const slug = routine.slug
  const [name, setName] = React.useState(routine.name ?? "")
  const [description, setDescription] = React.useState(routine.description ?? "")
  const [appearance, setAppearance] = React.useState({ icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) })
  const [base, setBase] = React.useState<Record<string, unknown>>(() => clone(routine.definition ?? {}))
  // The draft envelope (id, revision, base) as it was when editing started.
  // Saving sends THIS envelope, never a freshly loaded one: the server's
  // compare-and-set has to compare against the revision the edits started
  // from, otherwise a colleague's r3 saved meanwhile would be overwritten with
  // our r2-based document under r3's number. null = not loaded yet or failed.
  const [envelope, setEnvelope] = React.useState<RoutineDraft | null>(null)
  const [doc, setDocState] = React.useState<Record<string, unknown>>(() => clone(routine.definition ?? {}))
  const [loading, setLoading] = React.useState(enabled)
  const [unavailable, setUnavailable] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [epoch, setEpoch] = React.useState(0)

  // Open on the current draft's definition when there is one, so an edit
  // lands as the next revision of that draft rather than a fork of the
  // published version. Reloads after every save (the draft revision moves).
  React.useEffect(() => {
    if (!enabled) return
    setName(routine.name ?? "")
    setDescription(routine.description ?? "")
    setAppearance({ icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) })
    setRefusal(null)
    setUnavailable(false)
    const published = clone(routine.definition ?? {})
    setBase(published)
    setDocState(clone(published))
    setEnvelope(null)
    // Always load the envelope, even without a draft: the revision-0 baseline
    // carries the base pipeline id and publication revision the server checks
    // when the first revision is inserted.
    const controller = new AbortController()
    setLoading(true)
    loadRoutineDraft(workspaceId, slug, controller.signal)
      .then((draft) => {
        if (controller.signal.aborted) return
        setEnvelope(draft)
        if (!draft.id || !isRecord(draft.document.definition)) return
        setBase(clone(draft.document.definition))
        setDocState(clone(draft.document.definition))
      })
      .catch(() => {
        if (controller.signal.aborted) return
        setUnavailable(true)
        setRefusal("Could not load the routine draft. Reload the page before saving.")
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, slug, workspaceId, routine.draft?.revision, routine.definition_hash, epoch])

  const identityDirty = name.trim() !== (routine.name ?? "").trim() || description.trim() !== (routine.description ?? "").trim()
  const definitionDirty = !same(doc, base)
  const published = (routine.head_version ?? 0) > 0 && !routine.draft_only
  const appearanceDirty = !published && !same(appearance, { icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) })
  const dirty = identityDirty || definitionDirty || appearanceDirty
  const changeCount = React.useMemo(() => {
    let n = 0
    if (name.trim() !== (routine.name ?? "").trim()) n++
    if (description.trim() !== (routine.description ?? "").trim()) n++
    if (appearanceDirty) n++
    const keys = new Set([...Object.keys(doc), ...Object.keys(base)])
    for (const k of keys) {
      if (k === "steps") {
        const a = Array.isArray(doc.steps) ? doc.steps : []
        const b = Array.isArray(base.steps) ? base.steps : []
        const len = Math.max(a.length, b.length)
        for (let i = 0; i < len; i++) if (!same(a[i], b[i])) n++
      } else if (!same(doc[k], base[k])) n++
    }
    return n
  }, [name, description, appearanceDirty, doc, base, routine.name, routine.description])
  const saveAsDraft = definitionDirty || (!published && dirty)
  const nextRevision = (envelope?.revision ?? routine.draft?.revision ?? 0) + 1

  const update = React.useCallback(
    (patch: (next: Record<string, unknown>) => void) =>
      setDocState((previous) => {
        const next = clone(previous)
        patch(next)
        return next
      }),
    [],
  )

  const saveAppearance = async (next: Partial<typeof appearance>) => {
    const previous = appearance
    setAppearance({ ...appearance, ...next })
    if (!published) return
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/appearance`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(next),
      })
      if (!res.ok) throw new Error("save")
      onChanged()
    } catch {
      setAppearance(previous)
      toast.error("Could not save the icon")
    }
  }

  const save = async (): Promise<boolean> => {
    if (busy || loading || !envelope) return false
    if (identityDirty && !name.trim()) {
      setRefusal("The routine needs a name.")
      return false
    }
    setBusy(true)
    setRefusal(null)
    try {
      if (identityDirty && !saveAsDraft) {
        const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/save`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(
            renamePayload(
              { slug, name: routine.name, description: routine.description, definition: routine.definition, author_crew_id: routine.author_crew_id },
              { name, description },
            ),
          ),
        })
        if (!res.ok) {
          const problem = await res
            .clone()
            .json()
            .then((body) => extractProblemDetail(body) ?? body?.error)
            .catch(() => null)
          throw new Error(problem || "The name and purpose could not be saved.")
        }
      }
      if (saveAsDraft) {
        const baseline = envelope
        // An input-only edit must retain identity already authored in the
        // draft instead of replacing it with the published page's metadata.
        const draftName = name.trim() !== (routine.name ?? "").trim()
          ? name.trim()
          : asString(baseline.document.name) || asString(doc.display_name) || name.trim() || slug
        const draftDescription = description.trim() !== (routine.description ?? "").trim()
          ? description.trim()
          : typeof baseline.document.description === "string" ? baseline.document.description
            : typeof doc.description === "string" ? doc.description : description.trim()
        const document: Record<string, unknown> = {
          ...(baseline.id ? baseline.document : {}),
          slug,
          name: draftName,
          description: draftDescription,
          definition: { ...doc, display_name: draftName, description: draftDescription },
        }
        if (routine.author_crew_id && !document.author_crew_id) document.author_crew_id = routine.author_crew_id
        if (!document.icon || !published) document.icon = appearance.icon
        if (!document.color || !published) document.color = appearance.color
        const saved = await saveRoutineDraft(workspaceId, { ...baseline, slug }, document)
        setEnvelope(saved)
        toast.success(`Saved as draft r${saved.revision} · publish to make it live`)
      } else if (identityDirty) {
        toast.success("Name and purpose saved")
      }
      onChanged()
      return true
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setRefusal(
        e instanceof RoutineDraftError && e.status === 409
          ? "Someone saved a newer draft or published recipe. Your changes are still here. Copy what you want to keep, then reload to review the latest version."
          : message,
      )
      return false
    } finally {
      setBusy(false)
    }
  }

  return {
    loading,
    unavailable,
    name,
    setName,
    description,
    setDescription,
    appearance,
    saveAppearance,
    doc,
    update,
    setDoc: setDocState,
    identityDirty,
    definitionDirty,
    changeCount,
    dirty,
    saveAsDraft,
    nextRevision,
    published,
    busy,
    refusal,
    setRefusal,
    save,
    reset: () => setEpoch((n) => n + 1),
  }
}
