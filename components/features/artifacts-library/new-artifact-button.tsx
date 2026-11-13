"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { ArrowDown, ArrowUp, ChevronDown, ChevronLeft, LayoutGrid, Plus, Search, Sparkles, Star, X } from "lucide-react"
import { Popover, PopoverAnchor, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Button } from "@/components/ui/button"
import { SubBarPrimary } from "@/components/layout/sub-bar"
import { useUserPreference } from "@/hooks/use-user-preference"
import { cn } from "@/lib/utils"
import { KIND_ORDER, KINDS, type ArtifactKind, type KindFamily } from "./kinds"
import { KindTile } from "./kind-tile"
import { effectivePins, KIND_EDITOR, lastKind, matchesKind, movePin, PIN_MAX, pushRecent, togglePin, type PinResult } from "./new-artifact-prefs"

/** One creatable UI kind and the creation flow it opens (see CREATE_FLOW in library-home). */
export interface NewEntry<Flow extends string = string> { kind: ArtifactKind; flow: Flow }

export interface NewArtifactPrefs {
  pins: ArtifactKind[]
  recent: ArtifactKind[]
  /** Star or unstar; the result says what happened so the menu can explain a refusal. */
  toggle: (kind: ArtifactKind) => PinResult
  move: (kind: ArtifactKind, delta: -1 | 1) => void
  reset: () => void
  /** Remember a kind someone just started to make. */
  record: (kind: ArtifactKind) => void
  guide: boolean
  setGuide: (on: boolean) => void
}

/** Per person and workspace, synced across their devices; never shared with anyone else. */
export function useNewArtifactPrefs(workspaceId: string, available: readonly ArtifactKind[]): NewArtifactPrefs {
  const [storedPins, setPins] = useUserPreference<ArtifactKind[] | null>(`artifacts.${workspaceId}.newPins`, null)
  const [storedRecent, setRecent] = useUserPreference<ArtifactKind[]>(`artifacts.${workspaceId}.newRecent`, [])
  const [dismissed, setDismissed] = useUserPreference<boolean>("artifacts.newGuideDismissed", false)
  const key = available.join(",")
  const pins = useMemo(() => effectivePins(storedPins, available), [storedPins, key]) // eslint-disable-line react-hooks/exhaustive-deps -- key stands for available
  const recent = useMemo(() => (Array.isArray(storedRecent) ? storedRecent : []).filter(kind => (KIND_ORDER as readonly string[]).includes(kind)), [storedRecent])
  return {
    pins,
    recent,
    toggle: kind => { const next = togglePin(pins, kind, available); if (next.pins !== pins) setPins([...next.pins]); return next.result },
    move: (kind, delta) => setPins(movePin(pins, kind, delta)),
    reset: () => setPins(null),
    record: kind => setRecent(pushRecent(recent, kind)),
    guide: dismissed !== true,
    setGuide: on => setDismissed(!on),
  }
}

const UNAVAILABLE = "Not available in this workspace yet"
const typing = (target: EventTarget | null) => {
  const el = target as HTMLElement | null
  return !!el && (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.isContentEditable)
}

type Mode = "short" | "all" | "edit"

/**
 * The library's New artifact button (Harbor 2 round 72, owner's pick "split
 * button + guide"). The main part opens the New artifact dialog on the kind
 * made last (N). The chevron opens a menu (Shift+N): search, Pinned (keys 1–5,
 * star to pin, up to five), Recent and every kind grouped Artifact / Craft.
 * Kinds this workspace cannot make stay visible with the reason and cannot be
 * pinned. Built for many kinds: search and grouping, not one tile per kind.
 */
export function NewArtifactButton<Flow extends string>({ entries, prefs, onNew, open, onOpenChange, family = null }: {
  entries: readonly NewEntry<Flow>[]
  prefs: NewArtifactPrefs
  /** Start making this flow; `fromMain` is the main button (the dialog may still switch kind). */
  onNew: (flow: Flow, fromMain?: boolean) => void
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Opened from the Home guide: show every kind of this family. */
  family?: KindFamily | null
}) {
  const [mode, setMode] = useState<Mode>("short")
  const [query, setQuery] = useState("")
  const [famFilter, setFamFilter] = useState<KindFamily | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const search = useRef<HTMLInputElement>(null)
  const available = entries.map(entry => entry.kind)
  const flowOf = (kind: ArtifactKind) => entries.find(entry => entry.kind === kind)?.flow
  const last = lastKind(prefs.recent, available)
  const pins = prefs.pins
  const pinnedLive = pins.filter(kind => available.includes(kind))

  // Shift+N asks for every kind; a fresh open otherwise starts at Pinned (or the guide's family).
  const wantAll = useRef(false)
  useEffect(() => {
    if (!open) return
    setQuery(""); setNote(null)
    setMode(family || wantAll.current ? "all" : "short"); setFamFilter(family)
    wantAll.current = false
  }, [open, family])

  const start = (kind: ArtifactKind, fromMain = false) => {
    const flow = flowOf(kind)
    if (!flow) { setNote(`${KINDS[kind].label}: ${UNAVAILABLE.toLowerCase()}`); return }
    prefs.record(kind)
    onOpenChange(false)
    onNew(flow, fromMain)
  }
  const star = (kind: ArtifactKind) => {
    const result = prefs.toggle(kind)
    setNote(result === "full" ? `Pinned is full (${PIN_MAX}). Unpin one first.` : result === "unavailable" ? `${KINDS[kind].label} can’t be pinned: ${UNAVAILABLE.toLowerCase()}.` : null)
    if (result === "full") setMode("edit")
  }

  // N makes the last kind; Shift+N opens every kind. Only on the library list, never while typing or in a dialog.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() !== "n" || event.metaKey || event.ctrlKey || event.altKey || typing(event.target)) return
      // Our own menu is a dialog too: Shift+N inside it still switches to every kind.
      if (document.querySelector("[role=dialog][data-state=open]:not([data-new-artifact-menu]), [role=alertdialog]")) return
      if (!available.length) return
      event.preventDefault()
      if (event.shiftKey) { if (open) { setFamFilter(null); setMode("all") } else { wantAll.current = true; onOpenChange(true) } }
      else if (last) start(last, true)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  })

  if (!entries.length) return null

  const row = (kind: ArtifactKind, key?: number) => {
    const def = KINDS[kind]
    const live = available.includes(kind)
    const pinned = pins.includes(kind)
    const editor = KIND_EDITOR[kind]
    return <li key={kind} className={cn("group flex min-w-0 items-center rounded-md hover:bg-accent", !live && "opacity-60")}>
      <button type="button" aria-label={`New ${def.label}`} aria-disabled={!live || undefined} title={live ? def.description : UNAVAILABLE}
        onClick={() => start(kind)} data-new-kind={kind}
        className="flex min-w-0 flex-1 items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm coarse:min-h-[44px]">
        <KindTile kind={kind} size="sm" className={cn("size-6", !live && "grayscale")} />
        <span className="flex min-w-0 flex-1 items-baseline gap-1.5 truncate">
          <span className="font-medium">{editor?.app ?? def.label}</span>
          {editor && <small className="truncate text-xs text-muted-foreground">{def.label} · {editor.extensions}</small>}
        </span>
        {!live ? <span className="rounded-full border px-1.5 text-micro font-semibold text-muted-foreground">Off</span>
          : key ? <kbd className="rounded border px-1 font-mono text-micro text-muted-foreground">{key}</kbd> : null}
      </button>
      {live && <Button type="button" variant="ghost" size="icon-sm" aria-pressed={pinned} aria-label={`${pinned ? "Unpin" : "Pin"} ${editor?.app ?? def.label}`}
        title={pinned ? "Unpin" : `Pin to this menu (up to ${PIN_MAX})`} onClick={() => star(kind)}
        className={cn("shrink-0 text-muted-foreground", pinned ? "text-warn" : "opacity-0 group-hover:opacity-100 focus-visible:opacity-100 coarse:opacity-100")}>
        <Star className={cn("size-3.5", pinned && "fill-current")} aria-hidden />
      </Button>}
    </li>
  }
  const caption = (text: React.ReactNode, extra?: React.ReactNode) =>
    <div className="flex items-center gap-2 px-2 pb-0.5 pt-2 text-xs font-semibold text-muted-foreground">{text}{extra && <span className="ml-auto">{extra}</span>}</div>
  const group = (label: string, kinds: ArtifactKind[]) => kinds.length > 0 && <section aria-label={label}>
    {caption(label)}<ul className="grid gap-px">{kinds.map(kind => row(kind))}</ul>
  </section>
  const byLive = (kinds: readonly ArtifactKind[]) => [...kinds].sort((a, b) => Number(available.includes(b)) - Number(available.includes(a)))

  let body: React.ReactNode
  const q = query.trim()
  if (q) {
    const found = byLive(KIND_ORDER.filter(kind => matchesKind(kind, q)))
    body = found.length ? <section aria-label="Results">{caption(`${found.length} found`)}<ul className="grid gap-px">{found.map(kind => row(kind))}</ul></section>
      : <p className="px-2 py-3 text-sm text-muted-foreground">Nothing called “{q}”. Try a file type like .xlsx.</p>
  } else if (mode === "edit") {
    body = <section aria-label="Edit pinned">
      <div className="flex items-center gap-2 px-1 pb-1">
        <Button type="button" variant="ghost" size="xs" onClick={() => setMode("short")}><ChevronLeft aria-hidden />Back</Button>
        <span className="ml-auto text-xs text-muted-foreground">Keys 1–{PIN_MAX} follow this order</span>
      </div>
      {pins.length ? <ul className="grid gap-px">{pins.map((kind, i) => <li key={kind} className="flex items-center gap-2 rounded-md px-2 py-1 text-sm">
        <KindTile kind={kind} size="sm" className="size-6" />
        <span className="min-w-0 flex-1 truncate">{KIND_EDITOR[kind]?.app ?? KINDS[kind].label}</span>
        <kbd className="rounded border px-1 font-mono text-micro text-muted-foreground">{i + 1}</kbd>
        <Button type="button" variant="ghost" size="icon-sm" aria-label={`Move ${KINDS[kind].label} up`} disabled={i === 0} onClick={() => prefs.move(kind, -1)}><ArrowUp aria-hidden /></Button>
        <Button type="button" variant="ghost" size="icon-sm" aria-label={`Move ${KINDS[kind].label} down`} disabled={i === pins.length - 1} onClick={() => prefs.move(kind, 1)}><ArrowDown aria-hidden /></Button>
        <Button type="button" variant="ghost" size="icon-sm" aria-label={`Unpin ${KINDS[kind].label}`} onClick={() => { prefs.toggle(kind); setNote(null) }}><X aria-hidden /></Button>
      </li>)}</ul> : <p className="px-2 py-2 text-sm text-muted-foreground">Nothing pinned. Star a kind under All kinds.</p>}
      <div className="flex items-center gap-2 px-1 pt-2">
        <Button type="button" variant="ghost" size="xs" onClick={() => { prefs.reset(); setNote(null) }}>Reset to default</Button>
        <Button type="button" variant="ghost" size="xs" className="ml-auto" onClick={() => setMode("all")}>All kinds</Button>
      </div>
    </section>
  } else if (mode === "all") {
    const fam = famFilter
    const tabs: [KindFamily | null, string][] = [[null, "All"], ["Artifact", "Artifacts"], ["Craft", "Craft"]]
    body = <>
      <div className="flex items-center gap-2 px-1">
        <Button type="button" variant="ghost" size="xs" onClick={() => setMode("short")}><ChevronLeft aria-hidden />Back</Button>
        <span className="ml-auto text-xs text-muted-foreground">Star to pin · up to {PIN_MAX}</span>
      </div>
      <div role="group" aria-label="Family" className="flex gap-1 px-1 pb-1 pt-1.5">
        {tabs.map(([value, label]) => <button key={label} type="button" aria-pressed={fam === value} onClick={() => setFamFilter(value)}
          className={cn("inline-flex h-6 items-center rounded-full border px-2.5 text-xs font-medium coarse:min-h-[44px]", fam === value ? "border-primary/40 bg-primary/12 text-primary-hover" : "bg-card hover:bg-accent")}>{label}</button>)}
      </div>
      {(!fam || fam === "Artifact") && group("Artifacts · live, versioned, agents help", byLive(KIND_ORDER.filter(kind => KINDS[kind].family === "Artifact")))}
      {(!fam || fam === "Craft") && group("Craft · files, one editor at a time", byLive(KIND_ORDER.filter(kind => KINDS[kind].family === "Craft")))}
    </>
  } else {
    const recent = prefs.recent.filter(kind => available.includes(kind) && !pins.includes(kind)).slice(0, 3)
    body = <>
      <section aria-label="Pinned">
        {caption("Pinned", <button type="button" className="text-xs font-medium text-primary-hover hover:underline" onClick={() => setMode("edit")}>Edit</button>)}
        {pins.length ? <ul className="grid gap-px">{pins.map(kind => row(kind, pinnedLive.includes(kind) ? pinnedLive.indexOf(kind) + 1 : undefined))}</ul>
          : <p className="px-2 py-1.5 text-sm text-muted-foreground">Nothing pinned. Star a kind under All kinds.</p>}
      </section>
      {recent.length > 0 && <section aria-label="Recent">{caption("Recent")}<ul className="grid gap-px">{recent.map(kind => row(kind))}</ul></section>}
      <div className="mt-1 grid gap-px border-t pt-1">
        <button type="button" onClick={() => { setFamFilter(null); setMode("all") }} className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent coarse:min-h-[44px]">
          <LayoutGrid aria-hidden className="mx-1 size-4 text-muted-foreground" /><span className="flex-1">All kinds <small className="text-xs text-muted-foreground">{available.length} of {KIND_ORDER.length} here</small></span>
          <kbd className="rounded border px-1 font-mono text-micro text-muted-foreground">⇧N</kbd>
        </button>
        {!prefs.guide && <button type="button" onClick={() => { prefs.setGuide(true); onOpenChange(false) }} className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent coarse:min-h-[44px]">
          <Sparkles aria-hidden className="mx-1 size-4 text-muted-foreground" />Show the guide on Home
        </button>}
      </div>
    </>
  }

  const onMenuKey = (event: React.KeyboardEvent) => {
    const inSearch = event.target === search.current
    if (/^[1-5]$/.test(event.key) && !event.metaKey && !event.ctrlKey && !event.altKey && mode === "short" && (!inSearch || !query)) {
      const kind = pinnedLive[Number(event.key) - 1]
      if (kind) { event.preventDefault(); start(kind) }
      return
    }
    if (event.key === "Enter" && inSearch) {
      event.preventDefault()
      const first = q ? byLive(KIND_ORDER.filter(kind => matchesKind(kind, q)))[0] : pinnedLive[0]
      if (first) start(first)
    }
  }

  return <Popover open={open} onOpenChange={onOpenChange}>
    <PopoverAnchor asChild>
      <span className="inline-flex">
        <SubBarPrimary icon={Plus} aria-label="New artifact" onClick={() => last && start(last, true)}
          title={last ? `New artifact · starts as ${KIND_EDITOR[last]?.app ?? KINDS[last].label} · N` : "New artifact"}
          className="rounded-r-none max-md:rounded-r-none">New artifact</SubBarPrimary>
        {/* The arrow is the Radix trigger: it toggles the menu, gets focus back on close and carries aria-expanded. */}
        <PopoverTrigger asChild>
          <SubBarPrimary icon={ChevronDown} aria-label="All ways to make an artifact" title="All ways to make an artifact · Shift+N"
            className="rounded-l-none border-l border-white/30 px-1.5 max-md:rounded-l-none" />
        </PopoverTrigger>
      </span>
    </PopoverAnchor>
    <PopoverContent align="end" aria-label="New artifact menu" data-new-artifact-menu="" onKeyDown={onMenuKey}
      onOpenAutoFocus={event => { event.preventDefault(); if (window.matchMedia?.("(pointer: fine)").matches) search.current?.focus() }}
      className="grid max-h-[min(560px,80vh)] w-[min(360px,calc(100vw-1rem))] content-start gap-0.5 overflow-y-auto p-1.5">
      <label className="relative mx-0.5 mb-1 block">
        <Search aria-hidden className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <input ref={search} value={query} onChange={event => setQuery(event.target.value)} aria-label="Find a kind" placeholder="Find a kind or file type…" autoComplete="off"
          className="h-8 w-full rounded-md border border-control-border bg-card pl-7 pr-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50" />
      </label>
      {/* Always mounted, so a screen reader announces the note when its text appears. */}
      <p role="status" className={cn("mx-1 text-xs text-warn", note && "rounded-md bg-warn/12 px-2 py-1.5")}>{note}</p>
      {body}
    </PopoverContent>
  </Popover>
}

/**
 * First-run guide on Home: what Artifacts and Craft are, with the kinds of
 * each. "Got it" hides it for this person; the New menu brings it back.
 */
export function NewArtifactGuide({ available, onNew, onBrowse, onDismiss }: {
  available: readonly ArtifactKind[]
  onNew: (kind: ArtifactKind) => void
  onBrowse: (family: KindFamily) => void
  onDismiss: () => void
}) {
  const way = (family: KindFamily, title: string, text: string) => {
    const kinds = KIND_ORDER.filter(kind => KINDS[kind].family === family)
    return <div className="grid content-start gap-2 rounded-xl border bg-card p-3">
      <div className="flex items-center gap-2"><b className="text-sm font-semibold">{title}</b>
        <span className="ml-auto font-mono text-xs text-muted-foreground">{kinds.filter(kind => available.includes(kind)).length}/{kinds.length}</span></div>
      <p className="text-xs text-muted-foreground">{text}</p>
      <div className="flex flex-wrap gap-1.5">
        {kinds.map(kind => {
          const live = available.includes(kind)
          return <button key={kind} type="button" aria-label={`Start ${KINDS[kind].label}`} disabled={!live} title={live ? `New ${KINDS[kind].label}` : UNAVAILABLE}
            onClick={() => onNew(kind)} className="rounded-lg p-0.5 hover:ring-2 hover:ring-primary/25 disabled:cursor-default disabled:opacity-45 disabled:grayscale disabled:hover:ring-0 coarse:min-h-[44px]">
            <KindTile kind={kind} />
          </button>
        })}
      </div>
      <div><Button type="button" variant="outline" size="sm" onClick={() => onBrowse(family)}>Browse</Button></div>
    </div>
  }
  return <section aria-labelledby="new-guide-heading" className="grid gap-3 rounded-xl border bg-gradient-to-b from-primary/5 to-card p-4">
    <div className="flex items-start gap-3">
      <div className="min-w-0 flex-1">
        <h2 id="new-guide-heading" className="text-base font-semibold">Two kinds of things live here</h2>
        <p className="text-sm text-muted-foreground">Artifacts are live and versioned; agents build and update them. Craft files open in their own editor, one person at a time. Star the kinds you use in <b className="font-medium text-foreground">New artifact</b> and they come first.</p>
      </div>
      <Button type="button" variant="ghost" size="sm" onClick={onDismiss}>Got it</Button>
    </div>
    <div className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,260px),1fr))] gap-2.5">
      {way("Artifact", "Artifacts", "Docs, dashboards, slides, designs and apps. Live, versioned and shareable.")}
      {way("Craft", "Craft", "Word documents in WordCraft and sheets in GridCraft. Agents propose changes while you hold the lock.")}
    </div>
  </section>
}
