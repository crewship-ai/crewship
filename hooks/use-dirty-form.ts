"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"

/**
 * useDirtyForm — baseline/draft tracking for any card that edits typed-in
 * values (text, numbers, selects) and commits them with an explicit Save.
 *
 * Extracted from crew-policy-controls, which grew this shape by hand
 * (pending state → `dirty` → a footer that only appears when there is
 * something to save → "Saving…" → audit reason). Every card that wants the
 * same behaviour was reimplementing it, so it now lives here and pairs with
 * <SaveFooter/>.
 *
 * Not for atomic controls. A switch, a file upload or a delete commits on
 * the spot and confirms with a toast — putting them behind a Save button is
 * worse than leaving them alone.
 *
 * ```tsx
 * const form = useDirtyForm({ name: org.name, slug: org.slug })
 * <Input value={form.draft.name} onChange={e => form.set("name", e.target.value)} />
 * <SaveFooter
 *   dirty={form.isDirty} status={form.status} error={form.error}
 *   onCancel={form.reset}
 *   onSave={() => form.submit(async (d) => { ...PATCH...  })}
 * />
 * ```
 */

export type SaveStatus = "idle" | "saving" | "saved" | "error"

export interface DirtyForm<T> {
  /** Current edited values. Render inputs from this, never from the baseline. */
  draft: T
  /** True when `draft` differs from the baseline on at least one key. */
  isDirty: boolean
  set: <K extends keyof T>(key: K, value: T[K]) => void
  patch: (values: Partial<T>) => void
  /** Throw the draft away and return to the baseline. */
  reset: () => void
  status: SaveStatus
  /** Message from the last failed submit, cleared on the next edit. */
  error: string | null
  /**
   * Run `save` with the current draft. On success the draft becomes the new
   * baseline (so the form goes clean) and `status` shows "saved" briefly. On
   * failure the draft is kept intact — a failed write must never silently
   * undo what someone typed.
   */
  submit: (save: (draft: T) => Promise<void>) => Promise<void>
}

interface Options {
  /** How long "saved" shows before the footer collapses. */
  savedMs?: number
}

function shallowEqual<T extends Record<string, unknown>>(a: T, b: T): boolean {
  const keys = Object.keys(a) as (keyof T)[]
  if (keys.length !== Object.keys(b).length) return false
  return keys.every((k) => Object.is(a[k], b[k]))
}

export function useDirtyForm<T extends Record<string, unknown>>(
  baseline: T,
  { savedMs = 2000 }: Options = {},
): DirtyForm<T> {
  const [committed, setCommitted] = useState<T>(baseline)
  const [draft, setDraft] = useState<T>(baseline)
  const [status, setStatus] = useState<SaveStatus>("idle")
  const [error, setError] = useState<string | null>(null)

  const isDirty = useMemo(() => !shallowEqual(draft, committed), [draft, committed])

  // Refs the async submit path reads, so it never closes over stale values
  // and never touches state after the card unmounts.
  const draftRef = useRef(draft)
  draftRef.current = draft
  const inFlight = useRef(false)
  const alive = useRef(true)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
      if (savedTimer.current) clearTimeout(savedTimer.current)
    }
  }, [])

  // The baseline is server state and can refetch mid-edit. Adopt it while the
  // form is clean; once the user has typed something, their draft wins until
  // they save or cancel — losing half-typed input to a background poll is the
  // worst outcome available here.
  const dirtyRef = useRef(isDirty)
  dirtyRef.current = isDirty
  useEffect(() => {
    if (dirtyRef.current) return
    setCommitted(baseline)
    setDraft(baseline)
    // Compare by value: callers routinely pass a fresh object literal built
    // from props, which would re-fire this on every render by identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(baseline)])

  const clearStaleError = useCallback(() => {
    setStatus((s) => (s === "error" ? "idle" : s))
    setError((e) => (e === null ? e : null))
  }, [])

  const set = useCallback<DirtyForm<T>["set"]>((key, value) => {
    clearStaleError()
    setDraft((d) => ({ ...d, [key]: value }))
  }, [clearStaleError])

  const patch = useCallback<DirtyForm<T>["patch"]>((values) => {
    clearStaleError()
    setDraft((d) => ({ ...d, ...values }))
  }, [clearStaleError])

  const reset = useCallback(() => {
    setDraft(committed)
    setStatus("idle")
    setError(null)
  }, [committed])

  const submit = useCallback<DirtyForm<T>["submit"]>(async (save) => {
    // Double-clicking Save must not issue two writes.
    if (inFlight.current) return
    inFlight.current = true
    setStatus("saving")
    setError(null)

    const sent = draftRef.current
    try {
      await save(sent)
      if (!alive.current) return
      // Rebase rather than refetch: the values we just wrote are authoritative
      // until the parent's own query refreshes.
      setCommitted(sent)
      setStatus("saved")
      if (savedTimer.current) clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => {
        if (alive.current) setStatus("idle")
      }, savedMs)
    } catch (e) {
      if (!alive.current) return
      setStatus("error")
      setError(e instanceof Error ? e.message : "Save failed")
    } finally {
      inFlight.current = false
    }
  }, [savedMs])

  return { draft, isDirty, set, patch, reset, status, error, submit }
}
