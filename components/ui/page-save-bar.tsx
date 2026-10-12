"use client"

import * as React from "react"
import { useRouter } from "next/navigation"
import { CircleCheck, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

/**
 * One Save for a whole page — the app-wide save pattern. Settings, Admin and
 * nested pages use it today; any other editor (crew settings, routines…)
 * adopts it the same way: docs/ux/README.md › "Saving".
 *
 * Every card that edits typed-in values registers its pending edits here
 * (usePageSave, or SaveFooter / SettingsSaveBar, which do it for you). The
 * page shows ONE floating bar, bottom centre of the content pane, as soon as
 * anything differs from the server: "3 unsaved changes · Discard · Save".
 * Save commits every registered card; Discard throws every draft away.
 *
 * Why one bar and not a Save per card: on a long page the card's own Save is
 * off screen, and people leave without saving. The bar is always in view, and
 * the page asks before anyone leaves with edits pending (links, the section
 * nav, a reload).
 *
 * Errors never sit inside a card: a failed save is a toast in the bottom-right
 * corner that stays until dismissed and offers Retry; the edits are kept.
 * Success is quiet — the bar says "Saved" for a moment and goes.
 *
 * Switches, uploads and deletes do not register: they commit on the spot.
 */

export interface PageSaveEntry {
  /** What the card is called, for the error toast: "Couldn't save Workspace". */
  label: string
  /** Fields that differ from the server; 0 = nothing pending. */
  count: number
  /** False while the draft is invalid; the bar's Save stays disabled. */
  canSave?: boolean
  saving?: boolean
  /** Commit this card. May open its own preview dialog (bulk changes). A
   *  rejection becomes an error toast; the draft must stay intact. */
  save: () => void | Promise<unknown>
  discard: () => void
}

interface Registered extends Required<Omit<PageSaveEntry, "save" | "discard">> {
  save: () => void | Promise<unknown>
  discard: () => void
}

interface PageSaveApi {
  set: (id: string, entry: Registered | null) => void
  /** A card's save failed: whatever happens next is not that Save's success. */
  failed: () => void
  /** Run `go` now, or once the person agreed to leave their edits behind. */
  guard: (go: () => void) => void
}

const PageSaveContext = React.createContext<PageSaveApi | null>(null)
const PageSaveStateContext = React.createContext<PageSaveState | null>(null)

interface PageSaveState {
  count: number
  saving: boolean
  canSave: boolean
  justSaved: boolean
  labels: string[]
  saveAll: () => void
  discardAll: () => void
}

/** Show a failed save the one way the app shows errors: bottom-right, sticky. */
export function toastSaveError(label: string, message: string | null | undefined, retry?: () => void) {
  toast.error(`Couldn’t save ${label}`, {
    id: `save-error:${label}`,
    description: message ? `${message} Your edits are kept.` : "Your edits are kept.",
    duration: Infinity,
    action: retry ? { label: "Retry", onClick: () => retry() } : undefined,
  })
}

/**
 * A switch that the server refused: the switch is already back where it was,
 * and the reason is the same sticky bottom-right toast, never a line in the card.
 */
export function toastSwitchError(label: string, message: string | null | undefined, retry?: () => void) {
  toast.error(`Couldn’t change ${label}`, {
    id: `switch-error:${label}`,
    description: message || undefined,
    duration: Infinity,
    action: retry ? { label: "Retry", onClick: () => retry() } : undefined,
  })
}

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : typeof e === "string" ? e : "Save failed."
}

export function PageSaveProvider({ children, guardLeaving = true }: {
  children: React.ReactNode
  /** False when the host page asks its own "leave with unsaved work?" (the
   *  Pages editor, whose guard owns Back and its address): the bar then only
   *  saves and discards, so the person is asked once, not twice. */
  guardLeaving?: boolean
}) {
  const router = useRouter()
  const [entries, setEntries] = React.useState<Record<string, Registered>>({})
  const entriesRef = React.useRef(entries)
  entriesRef.current = entries

  const set = React.useCallback((id: string, entry: Registered | null) => {
    setEntries((prev) => {
      if (!entry) {
        if (!(id in prev)) return prev
        const next = { ...prev }
        delete next[id]
        return next
      }
      return { ...prev, [id]: entry }
    })
  }, [])

  // Saves the bar started and that have not settled yet, by card. A card that
  // reports `saving` itself (useDirtyForm) is covered either way.
  const [inFlight, setInFlight] = React.useState<Record<string, true>>({})
  const inFlightRef = React.useRef(inFlight)
  inFlightRef.current = inFlight

  const list = Object.values(entries)
  const count = list.reduce((n, e) => n + e.count, 0)
  const saving = list.some((e) => e.saving) || Object.keys(inFlight).length > 0
  const canSave = list.every((e) => e.canSave)
  const dirty = count > 0

  // "Saved" for a moment once a Save leaves nothing pending — the only
  // success signal, so it has to outlive the edits it confirms. Only after a
  // Save: undoing your own edits by hand is not a save.
  const [justSaved, setJustSaved] = React.useState(false)
  const awaiting = React.useRef(false)
  React.useEffect(() => {
    if (!awaiting.current || saving || count > 0) return
    awaiting.current = false
    setJustSaved(true)
    const t = setTimeout(() => setJustSaved(false), 1600)
    return () => clearTimeout(t)
  }, [saving, count])
  React.useEffect(() => { if (dirty) setJustSaved(false) }, [dirty])
  // A new edit after a Save starts a new round: only the next Save may say "Saved".
  const lastCount = React.useRef(count)
  React.useEffect(() => {
    if (count > lastCount.current && !saving) awaiting.current = false
    lastCount.current = count
  }, [count, saving])
  const failed = React.useCallback(() => { awaiting.current = false }, [])

  const saveAll = React.useCallback(function saveAll() {
    const pending = Object.entries(entriesRef.current).filter(([, e]) => e.count > 0)
    if (pending.length === 0) return
    if (pending.some(([id, e]) => !e.canSave || e.saving || inFlightRef.current[id])) return
    awaiting.current = true
    setInFlight((m) => ({ ...m, ...Object.fromEntries(pending.map(([id]) => [id, true as const])) }))
    for (const [id, e] of pending) {
      // Each card owns its write; one failing must not stop the others.
      Promise.resolve()
        .then(() => e.save())
        .catch((err) => {
          awaiting.current = false
          toastSaveError(e.label, errorMessage(err), saveAll)
        })
        .finally(() => setInFlight((m) => {
          const next = { ...m }
          delete next[id]
          return next
        }))
    }
  }, [])

  const discardAll = React.useCallback(() => {
    awaiting.current = false
    for (const e of Object.values(entriesRef.current)) e.discard()
  }, [])

  // Leaving with edits pending asks first.
  const [leaveTo, setLeaveTo] = React.useState<(() => void) | null>(null)
  const leaveAfterSave = React.useRef<(() => void) | null>(null)
  const guard = React.useCallback((go: () => void) => {
    const pending = Object.values(entriesRef.current).some((e) => e.count > 0)
    if (pending) setLeaveTo(() => go)
    else go()
  }, [])

  // Once a "Save and leave" lands with nothing left pending, go.
  React.useEffect(() => {
    const go = leaveAfterSave.current
    if (!go || saving) return
    leaveAfterSave.current = null
    if (count === 0) go()
  }, [saving, count])

  // A link anywhere on the page (rail, back arrow, a card's own link) is a way
  // out too. Caught in the capture phase, before Next's router sees it.
  React.useEffect(() => {
    if (!dirty) return
    const onClick = (ev: MouseEvent) => {
      if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return
      const a = (ev.target as HTMLElement | null)?.closest?.("a[href]") as HTMLAnchorElement | null
      if (!a || a.target === "_blank" || a.hasAttribute("download")) return
      const url = new URL(a.href, window.location.href)
      if (url.origin !== window.location.origin) return
      if (url.pathname === window.location.pathname && url.search === window.location.search) return
      ev.preventDefault()
      ev.stopPropagation()
      setLeaveTo(() => () => router.push(url.pathname + url.search + url.hash))
    }
    const onUnload = (ev: BeforeUnloadEvent) => { ev.preventDefault() }
    const onKey = (ev: KeyboardEvent) => {
      if ((ev.metaKey || ev.ctrlKey) && !ev.altKey && !ev.shiftKey && ev.key.toLowerCase() === "s") {
        ev.preventDefault()
        saveAll()
      }
    }
    if (guardLeaving) {
      document.addEventListener("click", onClick, true)
      window.addEventListener("beforeunload", onUnload)
    }
    window.addEventListener("keydown", onKey)
    return () => {
      document.removeEventListener("click", onClick, true)
      window.removeEventListener("beforeunload", onUnload)
      window.removeEventListener("keydown", onKey)
    }
  }, [dirty, router, saveAll, guardLeaving])

  const api = React.useMemo<PageSaveApi>(() => ({ set, failed, guard }), [set, failed, guard])
  const labels = list.filter((e) => e.count > 0).map((e) => e.label)
  const state: PageSaveState = { count, saving, canSave, justSaved, labels, saveAll, discardAll }

  return (
    <PageSaveContext.Provider value={api}>
      <PageSaveStateContext.Provider value={state}>
        {children}
        <AlertDialog open={leaveTo !== null} onOpenChange={(o) => { if (!o) setLeaveTo(null) }}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Leave with {changes(count)}?</AlertDialogTitle>
              <AlertDialogDescription>
                {labels.length ? `Edits in ${labels.join(", ")} are not saved yet.` : "Your edits are not saved yet."}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <Button type="button" variant="ghost" onClick={() => setLeaveTo(null)}>Stay</Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  const go = leaveTo
                  setLeaveTo(null)
                  discardAll()
                  go?.()
                }}
              >
                Discard
              </Button>
              <Button
                type="button"
                disabled={!canSave}
                onClick={() => {
                  leaveAfterSave.current = leaveTo
                  setLeaveTo(null)
                  saveAll()
                }}
              >
                Save and leave
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </PageSaveStateContext.Provider>
    </PageSaveContext.Provider>
  )
}

function changes(n: number): string {
  return `${n} unsaved change${n === 1 ? "" : "s"}`
}

const PageSaveLabelContext = React.createContext<string | null>(null)

/** Names the card around a SaveFooter, so its error toast says which card failed. */
export function PageSaveLabel({ label, children }: { label: string; children: React.ReactNode }) {
  return <PageSaveLabelContext.Provider value={label}>{children}</PageSaveLabelContext.Provider>
}

/** The enclosing card's name, or null outside one. */
export function usePageSaveLabel(): string | null {
  return React.useContext(PageSaveLabelContext)
}

/**
 * Report a failed save that did not reject (a card that shows its own error,
 * like SaveFooter's status machine), so the bar does not later claim "Saved".
 */
export function usePageSaveFailed(): () => void {
  const api = React.useContext(PageSaveContext)
  return React.useCallback(() => api?.failed(), [api])
}

/**
 * The bar's pending edits and its Discard, for a host page that asks the
 * leave question itself (`guardLeaving={false}`) and must drop the drafts
 * when the person answers Discard.
 */
export function usePageSaveControls(): { count: number; discardAll: () => void } | null {
  const s = React.useContext(PageSaveStateContext)
  return s ? { count: s.count, discardAll: s.discardAll } : null
}

/** True inside a PageSaveProvider: the card's own Save gives way to the bar. */
export function useInPageSave(): boolean {
  return React.useContext(PageSaveContext) !== null
}

/**
 * Run a navigation that unmounts the page's cards (a section switch) through
 * the unsaved-changes question. Outside a provider it just runs.
 */
export function usePageSaveGuard(): (go: () => void) => void {
  const api = React.useContext(PageSaveContext)
  return React.useCallback((go: () => void) => (api ? api.guard(go) : go()), [api])
}

/**
 * Register a card's pending edits with the page's bar. Returns whether a bar
 * exists — when it does not (a card rendered on its own), the card keeps its
 * own Save.
 */
export function usePageSave(entry: PageSaveEntry): boolean {
  const api = React.useContext(PageSaveContext)
  const id = React.useId()
  const latest = React.useRef(entry)
  latest.current = entry
  const { label, count, canSave = true, saving = false } = entry
  const active = count > 0 || saving

  React.useEffect(() => {
    if (!api) return
    api.set(id, active ? {
      label, count, canSave, saving,
      save: () => latest.current.save(),
      discard: () => latest.current.discard(),
    } : null)
  }, [api, id, active, label, count, canSave, saving])

  React.useEffect(() => () => api?.set(id, null), [api, id])
  return api !== null
}

/**
 * The floating bar. Place it once inside the page's content pane (a
 * `relative` box around the scroller); it docks to the phone tab bar below
 * `sm`. Leave ~6rem of bottom padding in the scroller so it never covers the
 * last card.
 */
export function PageSaveBar({ className }: { className?: string }) {
  const s = React.useContext(PageSaveStateContext)
  if (!s || (s.count === 0 && !s.saving && !s.justSaved)) return null
  const done = s.count === 0 && !s.saving
  return (
    // Green like the success toasts in the corner (--save-bar-* tokens): the
    // edits are one step from done, and the bar must not be overlooked.
    <div
      role="region"
      aria-label="Unsaved changes"
      data-slot="page-save-bar"
      className={cn(
        "z-40 flex items-center gap-3 border border-[var(--save-bar-border)] bg-[var(--save-bar-bg)] text-xs text-[var(--save-bar-fg)]",
        "absolute bottom-6 left-1/2 -translate-x-1/2 rounded-xl py-1.5 pl-4 pr-1.5 whitespace-nowrap",
        "max-sm:fixed max-sm:inset-x-0 max-sm:bottom-[var(--mobile-tab-bar-h)] max-sm:translate-x-0 max-sm:rounded-none max-sm:border-x-0 max-sm:border-b-0 max-sm:px-3 max-sm:py-2",
        done ? "shadow-[0_16px_40px_rgba(0,0,0,.45)]" : "save-bar-glow",
        className,
      )}
    >
      {done ? (
        <span role="status" className="flex h-7 items-center gap-1.5 pr-2.5 font-medium">
          <CircleCheck className="h-4 w-4 fill-[var(--save-bar-fg)] text-[var(--save-bar-bg)]" aria-hidden /> Saved
        </span>
      ) : (
        <>
          <span className="flex min-w-0 flex-1 items-center gap-2 font-medium" title={s.labels.join(", ")}>
            <span className="relative flex h-2 w-2 shrink-0" aria-hidden>
              <span className="absolute inline-flex h-full w-full rounded-full bg-[var(--save-bar-fg)] opacity-70 motion-safe:animate-ping" />
              <span className="relative inline-flex h-2 w-2 rounded-full bg-[var(--save-bar-fg)]" />
            </span>
            <span><span className="font-mono tabular-nums">{s.count}</span> unsaved change{s.count === 1 ? "" : "s"}</span>
          </span>
          <kbd className="hidden rounded border border-[var(--save-bar-border)] px-1.5 font-mono text-[11px] sm:inline">⌘S</kbd>
          <Button type="button" variant="ghost" size="sm"
            className="h-7 text-xs text-[var(--save-bar-fg)] opacity-85 hover:bg-[color-mix(in_oklab,var(--save-bar-fg)_10%,transparent)] hover:text-[var(--save-bar-fg)] hover:opacity-100 coarse:h-[2.75rem]"
            disabled={s.saving} onClick={s.discardAll}>
            Discard
          </Button>
          <Button type="button" size="sm"
            className="h-7 gap-1.5 bg-[var(--save-bar-action)] text-xs font-semibold text-[var(--save-bar-action-fg)] hover:bg-[var(--save-bar-action)] hover:brightness-110 coarse:h-[2.75rem]"
            disabled={s.saving || !s.canSave} onClick={s.saveAll}>
            {s.saving && <Loader2 className="h-3 w-3 animate-spin" />}
            {s.saving ? "Saving…" : "Save"}
          </Button>
        </>
      )}
    </div>
  )
}
