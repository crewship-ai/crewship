"use client"

import * as React from "react"
import { ArrowLeft, Bot, ChevronDown, ChevronRight, History, Info, Link2, MessageSquare, MoreHorizontal, Share2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuShortcut, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Segmented } from "@/components/ui/segmented"
import { StatusPill } from "@/components/ui/status-pill"
import { SubBar } from "@/components/layout/sub-bar"
import { useLayoutBreakpoint } from "@/hooks/use-layout-breakpoint"
import { cn } from "@/lib/utils"
import { KINDS, type ResolvedKind } from "../kinds"
import { KindTile } from "../kind-tile"
import { AccessGate } from "./access-gate"
import { AgentDrawer, agentModes } from "./agent-drawer"
import { FrameContext, type SectionMeta } from "./frame-context"
import { statusInfo, STATUS_INFO_ICON, type FrameStatus } from "./status"
import { COMMON_TABS, type FrameAction, type FrameCapabilities, type FrameLayout } from "./types"

export interface ArtifactFrameProps {
  name: string
  kind: ResolvedKind
  /** The folder crumb; shown on desktop only. */
  folder?: string | null
  caps: FrameCapabilities
  /** Back to the library (the Artifacts crumb, the gates). */
  onHome: () => void
  /** The stable address (/artifacts/<id>), for Copy link. */
  href?: string
  onShare?: () => void
  /** Real presence from the kind's backend; nothing when it has none. */
  presence?: React.ReactNode
  /** The Info tab: what the library knows about this item. */
  info?: React.ReactNode
  /** A line under the status popover (version, owner, lock rule). */
  statusFoot?: React.ReactNode
  /** Kind actions for the status popover (Review proposal, Retry…). */
  statusActions?: React.ReactNode
  signInHref?: string
  /** Opened on mount: e.g. "history" from a link. */
  defaultDrawer?: string | null
  /** Tests and stories pin the layout; the app follows the viewport. */
  layout?: FrameLayout
  children: React.ReactNode
}

const COMMON_META: Record<(typeof COMMON_TABS)[number], { label: string; icon: typeof Bot }> = {
  agent: { label: "Agent", icon: Bot },
  comments: { label: "Comments", icon: MessageSquare },
  history: { label: "History", icon: History },
  info: { label: "Info", icon: Info },
}

function useFrameLayout(pinned?: FrameLayout): FrameLayout {
  const band = useLayoutBreakpoint()
  if (pinned) return pinned
  return band === "phone" ? "phone" : band === "tablet" ? "tablet" : "desktop"
}

/** The chip and its popover: the one place that says who can do what right now. */
export function StatusChip({ status, actions, foot, compact }: { status: FrameStatus; actions?: React.ReactNode; foot?: React.ReactNode; compact?: boolean }) {
  const info = statusInfo(status)
  const Glyph = typeof info.icon === "string" ? null : info.icon
  const lead = info.icon === "spin"
    ? <span aria-hidden className="size-2.5 animate-spin rounded-full border-[1.5px] border-current border-r-transparent" />
    : Glyph ? <Glyph aria-hidden className="size-3" /> : undefined
  const pill = <StatusPill tone={info.tone} live={info.icon === "dot"} icon={lead} label={compact ? <span className="sr-only">{info.label}</span> : info.label} />
  return <Popover>
    <PopoverTrigger asChild>
      <button type="button" data-status={info.key} aria-label={`Status: ${info.label}`} title={info.detail} className="ml-1 inline-flex shrink-0 rounded-full focus-visible:outline-2 focus-visible:outline-primary coarse:min-h-[44px] coarse:items-center">
        {pill}
      </button>
    </PopoverTrigger>
    <PopoverContent align="start" className="w-[300px] p-3" aria-label="Status">
      <StatusPill tone={info.tone} live={info.icon === "dot"} icon={lead} label={info.label} />
      <p className="mt-2 text-sm text-muted-foreground">{info.detail}</p>
      {actions && <div className="mt-2.5 flex flex-wrap gap-1.5">{actions}</div>}
      {foot && <div className="mt-2.5 flex gap-1.5 border-t pt-2 text-xs text-muted-foreground"><STATUS_INFO_ICON aria-hidden className="mt-px size-3 shrink-0" /><span>{foot}</span></div>}
    </PopoverContent>
  </Popover>
}

function TitleMenu({ name, kind, actions, compact, restricted }: { name: string; kind: ResolvedKind; actions: FrameAction[]; compact: boolean; restricted?: boolean }) {
  const label = restricted ? "Restricted artifact" : name
  return <DropdownMenu>
    <DropdownMenuTrigger asChild>
      <button type="button" aria-label={`${label}, artifact menu`} title={`${KINDS[kind.kind].label} · ${label}`}
        className="-ml-1 inline-flex min-w-0 max-w-[44vw] items-center gap-1.5 rounded-md py-0.5 pl-1 pr-1.5 text-sm font-semibold text-foreground hover:bg-accent data-[state=open]:bg-accent coarse:min-h-[44px] md:max-w-[36vw]">
        {!compact && <KindTile kind={kind.kind} size="sm" />}
        <span className="truncate">{label}</span>
        <ChevronDown aria-hidden className="size-3 shrink-0 text-muted-foreground" />
      </button>
    </DropdownMenuTrigger>
    {actions.length > 0 && <DropdownMenuContent align="start" className="w-64">
      {actions.map(action => {
        const Icon = action.icon
        return <React.Fragment key={action.id}>
          {action.separated && <DropdownMenuSeparator />}
          <DropdownMenuItem disabled={Boolean(action.disabledReason)} onSelect={action.onSelect} data-action={action.id}
            className={cn("items-start", action.danger && !action.disabledReason && "text-destructive focus:text-destructive")}>
            {Icon ? <Icon aria-hidden className="mt-0.5" /> : <span className="w-4" />}
            <span className="grid min-w-0 flex-1">
              <span>{action.label}</span>
              {action.disabledReason && <small className="text-xs text-muted-foreground">{action.disabledReason}</small>}
            </span>
            {action.shortcut && <DropdownMenuShortcut>{action.shortcut}</DropdownMenuShortcut>}
          </DropdownMenuItem>
        </React.Fragment>
      })}
    </DropdownMenuContent>}
  </DropdownMenu>
}

/**
 * The one frame every artifact kind opens in (Harbor 2 v87 `shell()`): one
 * header row (Artifacts › folder › kind tile + name ▾, the status chip, then
 * presence, kind tabs, Agent, Comments, History, Share), a stage that runs
 * edge to edge with slots floating in it, and one right drawer. What shows
 * comes from `caps` and from the drawer sections the body mounts. See FRAME.md.
 */
export function ArtifactFrame({ name, kind, folder, caps, onHome, href, onShare, presence, info, statusFoot, statusActions, signInHref, defaultDrawer = null, layout: pinned, children }: ArtifactFrameProps) {
  const layout = useFrameLayout(pinned)
  const phone = layout === "phone"
  const [sections, setSections] = React.useState<ReadonlyMap<string, SectionMeta>>(new Map())
  const [targets, setTargets] = React.useState<ReadonlyMap<string, HTMLElement>>(new Map())
  const [slotLayer, setSlotLayer] = React.useState<HTMLElement | null>(null)
  const [drawer, setDrawer] = React.useState<string | null>(defaultDrawer)

  const register = React.useCallback((meta: SectionMeta) => {
    setSections(prev => new Map(prev).set(meta.id, meta))
    return () => setSections(prev => { const next = new Map(prev); next.delete(meta.id); return next })
  }, [])
  // One stable ref callback per pane id, so a re-render never re-registers a target.
  const refs = React.useRef(new Map<string, (node: HTMLDivElement | null) => void>())
  const paneRef = React.useCallback((id: string) => {
    let ref = refs.current.get(id)
    if (!ref) {
      ref = (node: HTMLDivElement | null) => setTargets(prev => {
        if ((prev.get(id) ?? null) === node) return prev
        const next = new Map(prev)
        if (node) next.set(id, node); else next.delete(id)
        return next
      })
      refs.current.set(id, ref)
    }
    return ref
  }, [])

  const ok = caps.access === "ok"
  const registered = { propose: sections.has("agent:propose"), edit: sections.has("agent:edit") }
  const hasAgent = ok && agentModes(caps.agent ?? {}, registered).length > 0
  const kindTabs = [...sections.values()].filter(meta => !meta.hidden && !(COMMON_TABS as readonly string[]).includes(meta.id)).sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
  const tabs: { id: string; label: string; icon?: typeof Bot }[] = ok ? [
    ...kindTabs,
    ...(hasAgent ? [{ id: "agent", ...COMMON_META.agent }] : []),
    ...(sections.has("comments") ? [{ id: "comments", ...COMMON_META.comments }] : []),
    ...(sections.has("history") ? [{ id: "history", ...COMMON_META.history }] : []),
    ...(info || sections.has("info") ? [{ id: "info", ...COMMON_META.info }] : []),
  ] : []
  const open = drawer && tabs.some(tab => tab.id === drawer) ? drawer : null
  const [infoSeen, setInfoSeen] = React.useState(false)
  React.useEffect(() => { if (open === "info") setInfoSeen(true) }, [open])
  const toggle = (tab: string) => setDrawer(current => (current === tab ? null : tab))

  const context = React.useMemo(() => ({
    layout, slotLayer, sections, register, target: (id: string) => targets.get(id) ?? null, openDrawer: setDrawer, drawerTab: open,
  }), [layout, slotLayer, sections, register, targets, open])

  // Every action, in one place; the history and info tabs are reachable from here too.
  const actions: FrameAction[] = ok ? [
    ...(onShare ? [{ id: "share", label: "Share…", icon: Share2, onSelect: onShare }] : []),
    ...(href ? [{ id: "copy-link", label: "Copy link", icon: Link2, onSelect: () => { void navigator.clipboard?.writeText(new URL(href, window.location.origin).toString()).catch(() => {}) } }] : []),
    ...(caps.actions ?? []).map((action, index) => index === 0 ? { ...action, separated: true } : action),
    ...(sections.has("history") ? [{ id: "history", label: "Version history", icon: History, onSelect: () => setDrawer("history"), separated: !(caps.actions?.length) }] : []),
    ...(tabs.some(tab => tab.id === "info") ? [{ id: "info", label: "Info", icon: Info, onSelect: () => setDrawer("info") }] : []),
  ] : []

  const chip = ok && caps.status ? <StatusChip status={caps.status} compact={phone} actions={statusActions} foot={statusFoot} /> : null
  const title = <TitleMenu name={name} kind={kind} actions={actions} compact={phone} restricted={caps.access === "denied"} />
  const known = caps.access === "ok" || caps.access === "denied"
  const crumbs = <span className="flex min-w-0 items-center gap-1.5">
    {/* The way back on desktop and tablet: the folder the artifact lives in, or All artifacts. A phone has its back button. */}
    {!phone && <><button type="button" aria-label="All artifacts" onClick={onHome}
      className="max-w-[16rem] truncate rounded-sm font-normal text-muted-foreground hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-primary">
      {ok && folder && layout === "desktop" ? folder : "All artifacts"}
    </button><ChevronRight aria-hidden className="size-3.5 shrink-0 text-muted-foreground-soft" /></>}
    {known ? title : <span className="font-semibold">Artifact</span>}
    {phone && chip}
  </span>
  const headerButton = (id: string, label: string, Icon: typeof Bot) =>
    <Button key={id} type="button" variant="ghost" size="icon-sm" aria-label={label} title={label} aria-pressed={open === id} onClick={() => toggle(id)}
      className={cn("coarse:min-h-[44px] coarse:min-w-[44px]", open === id && "bg-primary/12 text-primary-hover")}>
      <Icon aria-hidden className="size-4" />
    </Button>
  const headerActions = !ok ? undefined : phone ? <>
    {hasAgent && headerButton("agent", "Agent", Bot)}
    {sections.has("comments") && headerButton("comments", "Comments", MessageSquare)}
    {actions.length > 0 && <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="ghost" size="icon-sm" aria-label="Artifact menu" className="coarse:min-h-[44px] coarse:min-w-[44px]"><MoreHorizontal aria-hidden className="size-4" /></Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        {actions.map(action => <DropdownMenuItem key={action.id} disabled={Boolean(action.disabledReason)} onSelect={action.onSelect}>{action.label}</DropdownMenuItem>)}
      </DropdownMenuContent>
    </DropdownMenu>}
  </> : <>
    {presence}
    {kindTabs.map(tab => headerButton(tab.id, tab.label, tab.icon ?? Info))}
    {hasAgent && headerButton("agent", "Agent", Bot)}
    {sections.has("comments") && headerButton("comments", "Comments", MessageSquare)}
    {sections.has("history") && headerButton("history", "History", History)}
    {onShare && <Button type="button" variant="outline" size="sm" className="coarse:min-h-[44px]" onClick={onShare}><Share2 aria-hidden />Share</Button>}
  </>

  const drawerClass = layout === "desktop" ? "w-[320px] shrink-0 border-l"
    : layout === "tablet" ? "absolute inset-y-0 right-0 z-30 w-[min(360px,100%)] border-l shadow-lg" : "absolute inset-0 z-30 w-full"

  return <FrameContext.Provider value={context}>
    <SubBar title="Artifacts" ariaLabel="Artifact" section={crumbs} meta={phone ? undefined : chip ?? undefined} actions={headerActions}
      leading={phone ? <Button type="button" variant="ghost" size="icon-sm" aria-label="All artifacts" onClick={onHome} className="coarse:min-h-[44px] coarse:min-w-[44px]"><ArrowLeft aria-hidden className="size-4" /></Button> : undefined} />
    <div data-artifact-frame={kind.kind} data-kind-mode={kind.mode} data-layout={layout} className="relative flex min-h-0 flex-1 overflow-hidden">
      <div data-artifact-stage className="relative flex min-h-0 min-w-0 flex-1 flex-col">
        <div className="flex min-h-0 flex-1 flex-col overflow-auto">
          {ok ? children : <AccessGate access={caps.access as Exclude<typeof caps.access, "ok">} onHome={onHome} signInHref={signInHref} />}
        </div>
        <div ref={setSlotLayer} data-slot-layer className="pointer-events-none absolute inset-0 z-10" />
      </div>
      {ok && <aside aria-label={tabs.find(tab => tab.id === open)?.label ?? "Panel"} hidden={!open} data-drawer={open ?? ""}
        className={cn("flex min-h-0 flex-col bg-background", drawerClass, !open && "hidden")}>
        <div className="flex shrink-0 items-center gap-1.5 border-b py-2 pl-2.5 pr-2">
          <div className="min-w-0 flex-1 overflow-x-auto [scrollbar-width:none]">
            {open && <Segmented<string> ariaLabel="Panel" size="xs" value={open} onChange={setDrawer} options={tabs.map(tab => ({ value: tab.id, label: tab.label }))} />}
          </div>
          <Button type="button" variant="ghost" size="icon-sm" aria-label="Close panel" onClick={() => setDrawer(null)} className="coarse:min-h-[44px] coarse:min-w-[44px]"><X aria-hidden className="size-4" /></Button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {hasAgent && <div hidden={open !== "agent"} data-pane="agent">
            <AgentDrawer support={caps.agent ?? {}} canEdit={caps.canEdit} kindNoun={KINDS[kind.kind].noun} registered={registered}
              modeTarget={mode => paneRef(`agent:${mode}`)} />
          </div>}
          {[...sections.values()].filter(meta => !meta.hidden).map(meta => <div key={meta.id} ref={paneRef(meta.id)} hidden={open !== meta.id} data-pane={meta.id} />)}
          {/* Info mounts the first time it opens: its access list reads the server, which an open artifact must not do unasked. */}
          {info && infoSeen && !sections.has("info") && <div hidden={open !== "info"} data-pane="info">{info}</div>}
        </div>
      </aside>}
    </div>
  </FrameContext.Provider>
}
