"use client"
import { useEffect, useMemo, useRef, useState } from "react"
import { AlertTriangle, ChevronLeft, Clock, Eye, Gauge, Info, Link2, MessageSquare, MousePointerClick, OctagonAlert, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Segmented } from "@/components/ui/segmented"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import { Textarea } from "@/components/ui/textarea"
import type { StatusTone } from "@/lib/format-status"
import { fmtSize } from "@/lib/file-format"
import { timeAgo } from "@/lib/time"
import { useSessionSafe } from "@/hooks/use-auth"
import { useArtifactAgentDispatch } from "@/hooks/use-artifact-agent-dispatch"
import { cn } from "@/lib/utils"
import { artifactDashboards } from "@/lib/feature-flags"
import { parseDashboardManifest } from "@/lib/pages/artifact-dashboard"
import { artifactSlidesMode } from "@/lib/pages/artifact-slides"
import { useArtifactViewer, type ArtifactViewerTarget } from "../projects/artifact-viewer-context"
import { useArtifactProjectMutations, useArtifactProjectSnapshot } from "../projects/use-artifact-project-data"
import type { ArtifactComment, ArtifactProjectSnapshot } from "../projects/types"
import { ArtifactPreviewFrame, type PreviewSelection } from "./artifact-preview-frame"
import { ArtifactDashboardPreview } from "./artifact-dashboard-preview"
import { ArtifactSlidesPreview } from "./artifact-slides-preview"
import { isValidAnchorSelector, screenLabel, screenLabels } from "./element-select"

export type ArtifactViewerVariant = "pane" | "fullscreen" | "library"

/** Queue state of a comment. Derived only from what the server sent: no status, no reply, no task → no chip. */
export function commentQueueChip(comment: ArtifactComment): { label: string; tone: StatusTone; live?: boolean } | null {
  const replyVersion = [...(comment.replies ?? [])].reverse().find(reply => reply.version > 0)?.version
  switch (comment.status) {
    case "pending": return { label: "Waiting", tone: "muted" }
    case "working": return { label: "Agent working", tone: "blue", live: true }
    case "answered": {
      const version = comment.answered_version ?? replyVersion
      return { label: version ? `Done → v${version}` : "Done", tone: "success" }
    }
    case "needs_clarification": return { label: "Needs a new selection", tone: "warn" }
  }
  if (replyVersion) return { label: `Done → v${replyVersion}`, tone: "success" }
  if (comment.task_id) return { label: "Sent to agent", tone: "blue" }
  return null
}

type Banner = "conflict" | "quota_full" | "anchor" | "failed"
interface Anchor extends PreviewSelection { version: number; path: string }

function errorOf(error: unknown): { status?: number; code?: string } {
  return error && typeof error === "object" ? error as { status?: number; code?: string } : {}
}

const BANNER_TONE = {
  warn: { box: "bg-chip-warn-bg", icon: AlertTriangle, iconClass: "text-chip-warn-fg" },
  info: { box: "bg-chip-info-bg", icon: Info, iconClass: "text-chip-info-fg" },
  bad: { box: "bg-chip-danger-bg", icon: OctagonAlert, iconClass: "text-chip-danger-fg" },
} as const

function ViewerBanner({ tone, icon, title, children, action }: { tone: keyof typeof BANNER_TONE; icon?: typeof Info; title?: string; children?: React.ReactNode; action?: React.ReactNode }) {
  const t = BANNER_TONE[tone]
  const Icon = icon ?? t.icon
  return <div role={tone === "bad" ? "alert" : undefined} data-tone={tone} className={cn("flex flex-wrap items-start gap-3 rounded-card px-3.5 py-2.5 text-sm text-foreground", t.box)}>
    <Icon className={cn("mt-0.5 size-4 shrink-0", t.iconClass)} aria-hidden />
    <div className="min-w-0 flex-[1_1_200px] space-y-1">{title && <b className="block font-semibold">{title}</b>}{children}</div>
    {action}
  </div>
}

function truncate(text: string, max: number) { return text.length > max ? `${text.slice(0, max - 1)}…` : text }
/** What a person calls the element: its text when it has some, otherwise its id. */
function elementLabel(selector: string, quote: string) { return quote ? `“${truncate(quote, 40)}”` : selector }

/** The server sends the author's account id; a person reads "You" for their own and no opaque id for others. */
const OPAQUE_ID = /^c[a-z0-9]{20,}$|^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
function authorLabel(author: string | undefined, me: string | undefined): string | null {
  if (!author) return null
  if (me && author === me) return "You"
  // A version the agent published carries its run, e.g. agent:<id>:msg_<id>.
  if (/^agent:/i.test(author)) return "Agent"
  return OPAQUE_ID.test(author) ? null : author
}

const PIN = "inline-flex shrink-0 items-center justify-center rounded-[999px_999px_999px_4px] font-semibold tabular-nums"

/** The right-side viewer for the artifact project the chat opened. Renders nothing without a target. */
export function ArtifactViewerPanel({ className, variant = "pane" }: { className?: string; variant?: ArtifactViewerVariant }) {
  const viewer = useArtifactViewer()
  const target = viewer?.target
  if (!viewer || !target) return null
  // A new target starts clean: no draft, banner or old version carries over to another project.
  return <ViewerBody key={`${viewer.workspaceId}:${target.slug}:${target.version ?? ""}`} workspaceId={viewer.workspaceId} target={target} onClose={viewer.close} className={className} variant={variant} />
}

interface BodyProps { workspaceId: string; target: ArtifactViewerTarget; onClose: () => void; className?: string; variant: ArtifactViewerVariant }
type Shell = (header: React.ReactNode, body: React.ReactNode) => React.ReactNode

function ViewerBody({ workspaceId, target, onClose, className, variant }: BodyProps) {
  const [viewing, setViewing] = useState<number | undefined>(target.version)
  const snapshot = useArtifactProjectSnapshot(workspaceId, target.slug, viewing)
  const data = snapshot.data
  const fullscreen = variant === "fullscreen"

  const closeButton = fullscreen
    ? <Button type="button" variant="ghost" size="sm" className="gap-1 px-1.5 coarse:min-h-[44px]" onClick={onClose}><ChevronLeft className="size-4" aria-hidden />Back to chat</Button>
    : <Button type="button" variant="ghost" size="icon" className="size-7 coarse:min-h-[44px] coarse:min-w-[44px]" aria-label="Close viewer" onClick={onClose}><X className="size-4" aria-hidden /></Button>

  const shell: Shell = (header, body) =>
    <section aria-label="Artifact viewer" data-variant={variant} className={cn("@container flex h-full w-full min-w-0 flex-col overflow-hidden bg-background", className)}>
      <header className="flex h-11 shrink-0 items-center gap-2 border-b px-2.5">
        {fullscreen && closeButton}
        {header}
        {!fullscreen && closeButton}
      </header>
      {body}
    </section>

  const status = errorOf(snapshot.error).status
  // Lost access wins over the cached copy: a background 403/404 must not leave the old preview on screen.
  if (snapshot.error && (status === 403 || status === 404)) {
    return shell(<div className="flex-1" />, <p className="p-6 text-sm text-muted-foreground">You don’t have access to this project</p>)
  }
  if (!data) {
    if (snapshot.error) {
      return shell(<div className="flex-1" />, <div className="space-y-2 p-6 text-sm">
        <p className="text-muted-foreground">This project could not be loaded.</p>
        <Button type="button" size="sm" variant="outline" onClick={() => void snapshot.refetch()}>Try again</Button>
      </div>)
    }
    return shell(<><Skeleton className="h-4 w-40" /><div className="flex-1" /></>, <div aria-busy="true" aria-label="Loading project" className="flex flex-1 flex-col gap-3 p-3">
      <Skeleton className="h-8 w-full" /><Skeleton className="min-h-40 flex-1" />
    </div>)
  }
  const props = { data, target, setViewing, refetch: () => void snapshot.refetch(), workspaceId, shell, fullscreen, writeEnabled: !snapshot.error && data.can_write === true }
  return variant === "library"
    ? <LibraryLoadedViewer {...props} dispatchEnabled={props.writeEnabled} />
    : <LoadedViewer {...props} />
}

interface LoadedProps {
  data: ArtifactProjectSnapshot
  target: ArtifactViewerTarget
  setViewing: (version: number | undefined) => void
  refetch: () => void
  workspaceId: string
  shell: Shell
  fullscreen: boolean
  writeEnabled: boolean
  library?: boolean
  sendToOrigin?: (request: { pageId: string; comment: ArtifactComment }) => Promise<boolean>
}

/** Only the library connects to the source chat; chat panes already own their live dispatcher. */
function LibraryLoadedViewer(props: LoadedProps & { dispatchEnabled: boolean }) {
  const { data, workspaceId, dispatchEnabled } = props
  const agent = useArtifactAgentDispatch(workspaceId, data.page_id, data.origin ?? undefined, dispatchEnabled)
  return <LoadedViewer {...props} library sendToOrigin={data.origin && dispatchEnabled ? request => agent.dispatch(request.comment) : undefined} />
}

function LoadedViewer({ data, target, setViewing, refetch, workspaceId, shell, fullscreen, writeEnabled, library = false, sendToOrigin }: LoadedProps) {
  const me = useSessionSafe()?.data?.user?.id
  const canWrite = writeEnabled && data.can_write === true
  const scope = JSON.stringify([workspaceId, data.page_id])
  const permission = useRef({ scope, allowed: canWrite, epoch: 0 })
  if (permission.current.scope !== scope || permission.current.allowed !== canWrite) permission.current = { scope, allowed: canWrite, epoch: permission.current.epoch + 1 }
  const epoch = permission.current.epoch
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  // Keep ordinary polls/code updates from cancelling an acknowledged comment,
  // while stale callbacks from an old Page or write epoch cannot mutate or dispatch.
  const writeAllowed = () => mounted.current && permission.current.allowed && permission.current.epoch === epoch
  const chatDispatch = useArtifactViewer()?.sendToAgent
  const sendToAgent = library ? sendToOrigin : chatDispatch
  const { restore, share, comment } = useArtifactProjectMutations(workspaceId, target.slug)
  // A comment saved for the agent whose chat message did not go out; Retry sends it again.
  const [undispatched, setUndispatched] = useState<ArtifactComment | null>(null)
  const [historyOpen, setHistoryOpen] = useState(target.tab === "versions")
  const [activeTab, setActiveTab] = useState(target.tab ?? "preview")
  // A card asking for another section of the same project keeps the viewer (and an unsent comment).
  const [seenTab, setSeenTab] = useState(target.tab)
  if (seenTab !== target.tab) { setSeenTab(target.tab); setActiveTab(target.tab ?? "preview"); if (target.tab === "versions") setHistoryOpen(true) }
  const [screenPath, setScreenPath] = useState<string | null>(null)
  const [selectMode, setSelectMode] = useState(false)
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const [draft, setDraft] = useState("")
  const [banner, setBanner] = useState<Banner | null>(null)
  // A comment notice belongs to the version it was sent on and to that selection; see below.
  const [notice, setNotice] = useState<{ text: string; commentOn?: number }>({ text: "" })
  const [highlight, setHighlight] = useState<{ selector: string; seq: number } | null>(null)
  const [confirmShare, setConfirmShare] = useState(false)
  useEffect(() => {
    if (!canWrite) { setSelectMode(false); setAnchor(null); setUndispatched(null); setConfirmShare(false) }
  }, [canWrite])
  const historyRef = useRef<HTMLDivElement>(null)
  const queueRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLDivElement>(null)

  // The old tabbed entry points still land: "versions" opens history, "comments" brings the queue into view.
  useEffect(() => {
    const section = target.tab === "versions" ? historyRef.current : target.tab === "comments" ? queueRef.current : null
    section?.scrollIntoView?.({ block: "start" })
    section?.focus({ preventScroll: true })
  }, [target.tab])

  const latest = data.latest_version
  const sharedVersion = data.shared_version
  const shown = data.manifest.version
  const viewingOld = shown !== latest
  const isShared = sharedVersion > 0 && sharedVersion === latest
  const slidesMode = artifactSlidesMode(data.artifact_kind, data.manifest.files)
  const labels = useMemo(() => slidesMode
    ? new Map(data.manifest.files.filter(file => file.media_type === "text/html").map(file => [file.path, screenLabel(file.path)]))
    : screenLabels(data.manifest), [data.manifest, slidesMode])
  const screens = useMemo(() => [...labels.keys()], [labels])
  const labelOf = (path: string) => labels.get(path) ?? screenLabel(path)
  const screen = screenPath && screens.includes(screenPath) ? screenPath : screens[0] ?? ""
  const html = data.manifest.files.find(file => file.path === screen)?.content ?? ""
  // The server anchors comments by a unique #id only; a screen without ids has nothing to select.
  const anchorable = useMemo(() => slidesMode ? slidesMode === "slides" : typeof DOMParser === "undefined" || new DOMParser().parseFromString(html, "text/html").querySelector("body [id]") !== null, [html, slidesMode])
  const versions = useMemo(() => [...data.versions].sort((a, b) => b.version - a.version), [data.versions])

  // "Comment sent" is stale once another version is shown (the agent published) or a new selection starts.
  // Adjusted during render rather than in an effect, so the stale notice never paints.
  if (notice.commentOn !== undefined && notice.commentOn !== shown) setNotice({ text: "" })
  const clearCommentNotice = () => { if (notice.commentOn !== undefined) setNotice({ text: "" }) }

  const fail = (error: unknown) => {
    if (!writeAllowed()) return
    const { status, code } = errorOf(error)
    if (code === "artifact_version_conflict") setBanner("conflict")
    else if (code === "artifact_history_quota" || status === 413) setBanner("quota_full")
    else if (code === "artifact_anchor_mismatch") { setBanner("anchor"); setAnchor(null); setSelectMode(true) }
    else setBanner("failed")
  }

  const select = (selection: PreviewSelection) => {
    if (!writeAllowed() || !selectMode || viewingOld) return
    setAnchor({ ...selection, version: shown, path: screen })
    clearCommentNotice()
    if (banner === "anchor") setBanner(null)
  }
  const resetComposer = () => { setAnchor(null); setDraft(""); setSelectMode(false) }
  // B's API dispatches to the agent only at creation (send_to_agent) and has no later "send queued" route,
  // so the choice is made per comment here instead of a batch "Send n to agent".
  const submit = (send: boolean) => {
    if (!writeAllowed() || viewingOld || !anchor || !draft.trim()) return
    comment.mutate({ version: anchor.version, path: anchor.path, selector: anchor.selector, quote: anchor.quote, text: draft.trim(), send_to_agent: send }, {
      onSuccess: saved => {
        if (!writeAllowed()) return
        resetComposer(); setBanner(null)
        if (send && sendToAgent) void dispatch(saved)
        // Without a chat to start it, a comment for the agent waits as its task.
        else setNotice({ text: send ? "Comment saved for the agent" : "Comment saved", commentOn: anchor.version })
      },
      onError: fail,
    })
  }
  // The comment is saved as the agent's task; the run starts with a chat message bound to it.
  const deliveryConfirmed = (saved: ArtifactComment) => Boolean(saved.task_id && data.comments.some(current =>
    current.id === saved.id && current.task_id === saved.task_id && current.version === saved.version &&
    (current.status === "working" || current.status === "answered" || current.status === "needs_clarification" || current.replies?.some(reply => /^agent:/.test(reply.author ?? ""))),
  ))
  // Authoritative task progress wins over a missing or late socket ACK. Clear
  // the old failure before it paints, so Retry cannot launch an answered task.
  if (undispatched && deliveryConfirmed(undispatched)) setUndispatched(null)
  const dispatch = async (saved: ArtifactComment) => {
    if (!writeAllowed() || deliveryConfirmed(saved)) return
    setUndispatched(null)
    const started = sendToAgent ? await sendToAgent({ pageId: data.page_id, comment: saved }).catch(() => false) : false
    if (!writeAllowed()) return
    if (started) setNotice({ text: "Comment sent to the agent", commentOn: saved.version })
    else setUndispatched(saved)
  }
  const jumpTo = (c: ArtifactComment) => {
    if (library) setActiveTab("preview")
    if (screens.includes(c.path)) setScreenPath(c.path)
    if (isValidAnchorSelector(c.selector)) setHighlight(prev => ({ selector: c.selector, seq: (prev?.seq ?? 0) + 1 }))
    canvasRef.current?.scrollIntoView?.({ block: "nearest" })
  }
  const doRestore = (version: number) => {
    if (!writeAllowed()) return
    restore.mutate({ base_version: latest, version }, {
      onSuccess: result => { if (!writeAllowed()) return; setNotice({ text: `Restored v${version} as v${result.version} · nothing deleted` }); setViewing(undefined) },
      onError: fail,
    })
  }
  const doShare = (version: number) => {
    if (!writeAllowed()) return
    share.mutate({ base_version: latest, version }, {
      onSuccess: result => { if (!writeAllowed()) return; setConfirmShare(false); setNotice({ text: `Shared v${result.shared_version}` }) },
      onError: error => { if (!writeAllowed()) return; setConfirmShare(false); fail(error) },
    })
  }

  const header = <>
    <h2 className="min-w-0 truncate text-sm font-semibold">{data.name ?? data.slug ?? target.slug}</h2>
    <Badge variant="outline" className="shrink-0">{slidesMode === "slides" ? "Slides" : slidesMode === "unsupported" ? "Artifact" : artifactDashboards() && parseDashboardManifest(data.manifest.files) ? "Dashboard" : "Project"}</Badge>
    <StatusPill tone={isShared ? "success" : "muted"} label={isShared ? "Shared" : "Draft"} />
    <div className="flex-1" />
  </>

  // Latest and the shared version are the two versions people talk about; another one only via history.
  const versionValue = !viewingOld ? "latest" : shown === sharedVersion ? "shared" : `v${shown}`
  const versionOptions = [
    { value: "latest", label: `Latest · v${latest}` },
    ...(sharedVersion > 0 && sharedVersion !== latest ? [{ value: "shared", label: `Shared · v${sharedVersion}` }] : []),
    ...(viewingOld && shown !== sharedVersion ? [{ value: `v${shown}`, label: `v${shown}` }] : []),
  ]
  const shareTarget = shown
  const canShare = canWrite && shareTarget !== sharedVersion

  const toolbar = <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b px-3 py-2">
    <Segmented ariaLabel="Version" size="xs" value={versionValue} options={versionOptions}
      onChange={value => setViewing(value === "latest" ? undefined : value === "shared" ? sharedVersion : Number(value.slice(1)))} />
    {!library && versions.length > 0 && <Button type="button" variant="ghost" size="sm" className="h-7 gap-1.5" aria-pressed={historyOpen} onClick={() => setHistoryOpen(open => !open)}>
      <Clock className="size-3.5" aria-hidden />History
    </Button>}
    <div className="flex-1" />
    {canShare && <Popover open={confirmShare} onOpenChange={setConfirmShare}>
      <PopoverTrigger asChild><Button type="button" variant="ghost" size="sm" className="h-7 gap-1.5"><Link2 className="size-3.5" aria-hidden />Share</Button></PopoverTrigger>
      <PopoverContent align="end" className="w-72 space-y-3 text-sm">
        <p>People with access will see v{shareTarget}. Later versions stay private until you share them.</p>
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="ghost" onClick={() => setConfirmShare(false)}>Cancel</Button>
          <Button type="button" size="sm" disabled={share.isPending} onClick={() => doShare(shareTarget)}>Share v{shareTarget}</Button>
        </div>
      </PopoverContent>
    </Popover>}
  </div>

  const compactScreens = screens.length > 4
  const subToolbar = <div className="flex shrink-0 items-center gap-1.5 border-b px-3 py-2">
    {!slidesMode && screens.length > 1 && <>
      {/* Up to four screens stay a segmented control at every width (labels truncate, as in the design);
          only more than that falls back to a select on a narrow pane. */}
      <div className={cn("min-w-0", compactScreens && "hidden @md:block")}>
        <Segmented ariaLabel="Screen" size="xs" value={screen} onChange={setScreenPath} className="shrink [&>button]:min-w-0 [&>button]:shrink"
          options={screens.map(path => ({ value: path, label: <span className="truncate">{labelOf(path)}</span>, title: path }))} />
      </div>
      {compactScreens && <select aria-label="Screen (compact)" value={screen} onChange={event => setScreenPath(event.target.value)}
        className="h-7 min-w-0 rounded-md border bg-card px-2 text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring @md:hidden">
        {screens.map(path => <option key={path} value={path}>{labelOf(path)}</option>)}
      </select>}
    </>}
    <div className="flex-1" />
    {canWrite && slidesMode !== "unsupported" && !viewingOld && <Button type="button" size="sm" variant={selectMode ? "secondary" : "ghost"} className="h-7 gap-1.5" aria-pressed={selectMode}
      onClick={() => { if (selectMode) setAnchor(null); else clearCommentNotice(); setSelectMode(on => !on) }}>
      <MousePointerClick className="size-3.5" aria-hidden />Select
    </Button>}
  </div>

  const quota = data.quota
  const pct = quota && quota.limit_bytes > 0 ? Math.min(100, Math.round(quota.used_bytes / quota.limit_bytes * 100)) : 100
  const banners = <>
    {banner === "conflict" && <ViewerBanner tone="warn" title="Changed meanwhile" action={<Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" onClick={() => { setBanner(null); setViewing(undefined); refetch() }}>Reload and reapply</Button>}>
      <span>Someone saved a newer version while you were working. Nothing was overwritten.</span>
    </ViewerBanner>}
    {banner === "quota_full" && <ViewerBanner tone="bad">History is full. Your changes were not saved and remain here.</ViewerBanner>}
    {quota?.warning && banner !== "quota_full" && <ViewerBanner tone="warn" icon={Gauge} title="This project is close to its limit">
      <span>{fmtSize(quota.used_bytes)} of {fmtSize(quota.limit_bytes)} ({pct}%). Old versions stay until someone decides.</span>
      <div role="progressbar" aria-label="History storage used" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} className="h-1.5 overflow-hidden rounded-full bg-background/60">
        <div className="h-full rounded-full bg-warn" style={{ width: `${pct}%` }} />
      </div>
    </ViewerBanner>}
    {banner === "anchor" && <ViewerBanner tone="info">This element changed. Select it again.</ViewerBanner>}
    {banner === "failed" && <ViewerBanner tone="bad">That did not go through. Nothing was changed; try again.</ViewerBanner>}
    {canWrite && undispatched && <ViewerBanner tone="warn" title="The agent did not start" action={<Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" onClick={() => void dispatch(undispatched)}>Retry</Button>}>
      The comment is saved. Sending it to the agent in this chat did not go through.</ViewerBanner>}
    {viewingOld && shown === sharedVersion && <ViewerBanner tone="info" icon={Eye}
      action={canWrite && <Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" disabled={share.isPending} onClick={() => doShare(latest)}>Share v{latest}</Button>}>
      You see the shared version v{shown}, the one people with the link get. Latest is v{latest}.
    </ViewerBanner>}
    {viewingOld && shown !== sharedVersion && <ViewerBanner tone="info" icon={Eye} action={<Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" onClick={() => setViewing(undefined)}>Back to latest</Button>}>
      You are viewing v{shown}. Latest is v{latest}.
    </ViewerBanner>}
  </>

  const history = (library || historyOpen) && versions.length > 0 && <div ref={historyRef} tabIndex={-1} role="region" aria-label="Version history" className="rounded-card border bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
    <ul>{versions.map(v => <li key={v.version} className="flex flex-wrap items-center gap-2.5 border-t px-3 py-2 first:border-t-0">
      <span data-slot="version-number" className="shrink-0 rounded-sm border bg-muted px-[5px] font-mono text-micro font-semibold text-foreground/80">v{v.version}</span>
      <div className="grid min-w-0 flex-[1_1_180px] grid-cols-[minmax(0,1fr)]">
        <b className="truncate text-sm font-semibold">{[authorLabel(v.actor, me), v.message].filter(Boolean).join(" · ") || `Version ${v.version}`}</b>
        <small className="text-xs text-muted-foreground">{[v.created_at && timeAgo(v.created_at), v.restored_from ? `Restored from v${v.restored_from}` : null].filter(Boolean).join(" · ")}</small>
      </div>
      {v.version === latest && <StatusPill tone="muted" label="Latest" />}
      {v.version === sharedVersion && <StatusPill tone="success" label="Shared" />}
      {v.version !== shown && <Button type="button" size="sm" variant="ghost" className="h-7" onClick={() => setViewing(v.version === latest ? undefined : v.version)}>View</Button>}
      {canWrite && v.version !== latest && <Button type="button" size="sm" variant="ghost" className="h-7" disabled={restore.isPending} onClick={() => doRestore(v.version)}>Restore as new version</Button>}
    </li>)}</ul>
  </div>

  const pinNumber = data.comments.length + 1
  const pinCard = canWrite && (anchor || draft) && !viewingOld && <form aria-label="New comment" onSubmit={event => { event.preventDefault(); submit(true) }}
    className="z-[2] mt-2.5 flex flex-wrap items-start gap-2 rounded-card border bg-card px-2.5 py-2 shadow-md @md:absolute @md:right-[18px] @md:top-[18px] @md:mt-0 @md:max-w-[min(320px,calc(100%-36px))]">
    <span aria-hidden className={cn(PIN, "size-6 bg-primary text-xs text-primary-foreground")}>{pinNumber}</span>
    <div className="grid min-w-0 flex-[1_1_160px] gap-1.5">
      <b className="truncate text-xs font-semibold">{anchor ? `On ${anchor.selector}${anchor.quote ? ` · ${elementLabel(anchor.selector, anchor.quote)}` : ""} · v${anchor.version}` : "Select an element to attach this comment"}</b>
      <Textarea aria-label="Comment" value={draft} onChange={event => setDraft(event.target.value)} rows={3} placeholder="What should change here?" className="min-h-16 text-sm" />
    </div>
    <div className="flex w-full flex-wrap justify-end gap-1.5">
      <Button type="button" size="sm" variant="ghost" onClick={resetComposer}>Cancel</Button>
      <Button type="button" size="sm" variant="secondary" disabled={!anchor || !draft.trim() || comment.isPending} onClick={() => submit(false)}>Save comment</Button>
      <Button type="submit" size="sm" disabled={!anchor || !draft.trim() || comment.isPending}>Send to agent</Button>
    </div>
  </form>

  const canvas = <div ref={canvasRef} className="relative rounded-card bg-muted p-3.5">
    <div className={cn("overflow-hidden rounded-md border bg-white", fullscreen ? "h-[max(320px,calc(100dvh-14rem))]" : "h-[min(70vh,560px)] min-h-[320px]")}>
      {slidesMode === "unsupported" ? <p role="alert" className="p-4">This Slides format is unsupported.</p>
        : slidesMode === "slides" ? <ArtifactSlidesPreview workspaceId={workspaceId} pageId={data.page_id} codeVersion={shown} artifactKind={data.artifact_kind} path={screenPath ?? ""} title={`Preview of ${labelOf(screen)}`} selectMode={canWrite && selectMode && !viewingOld} onSelect={select} onNavigate={setScreenPath} />
        : artifactDashboards() && data.manifest.files.some(file => file.path === "dashboard.json")
        ? <ArtifactDashboardPreview workspaceId={workspaceId} pageId={data.page_id} codeVersion={shown} files={data.manifest.files} path={screen} selectMode={canWrite && selectMode && !viewingOld} title={`Preview of ${labelOf(screen)}`} onSelect={select} onNavigate={setScreenPath} />
        : <ArtifactPreviewFrame html={html} files={data.manifest.files} screens={screens} path={screen} selectMode={canWrite && selectMode && !viewingOld} title={`Preview of ${labelOf(screen)}`}
          highlight={highlight} onSelect={select} onNavigate={setScreenPath} />}
    </div>
    {pinCard}
  </div>

  const queue = <div ref={queueRef} tabIndex={-1} role="region" aria-label="Comment queue" className="rounded-card border bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
    <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
      <MessageSquare className="size-4 text-muted-foreground" aria-hidden />
      <b className="text-sm font-semibold">Comments for agent</b>
      <span className="text-xs tabular-nums text-muted-foreground">{data.comments.length}</span>
    </div>
    {data.comments.length === 0
      ? <p className="px-3 py-2.5 text-sm text-muted-foreground">{canWrite ? "No comments yet. Turn on Select and click a part of the screen." : "No comments yet."}</p>
      : <ul>{data.comments.map((c, index) => {
        const chip = commentQueueChip(c)
        return <li key={c.id} className="border-t first:border-t-0">
          <button type="button" className="flex w-full flex-wrap items-center gap-2.5 px-3 py-2 text-left hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring" onClick={() => jumpTo(c)}>
            <span aria-hidden className={cn(PIN, "size-5 bg-muted text-micro text-foreground/80 ring-1 ring-border")}>{index + 1}</span>
            <span className="grid min-w-0 flex-[1_1_180px] grid-cols-[minmax(0,1fr)]">
              <b className="text-sm font-semibold">{c.text}</b>
              <small className="truncate text-xs text-muted-foreground">{[authorLabel(c.author, me), labelOf(c.path), c.selector && elementLabel(c.selector, c.quote), c.created_at && timeAgo(c.created_at)].filter(Boolean).join(" · ")}</small>
            </span>
            {chip && <StatusPill tone={chip.tone} label={chip.label} live={chip.live} />}
          </button>
          {!!c.replies?.length && <ul className="space-y-1 px-3 pb-2 pl-[42px]">{c.replies.map((reply, i) => <li key={i} className="text-xs">
            <span className="text-muted-foreground">{[authorLabel(reply.author, me), reply.version > 0 && `v${reply.version}`].filter(Boolean).join(" · ")}{(authorLabel(reply.author, me) || reply.version > 0) && ": "}</span>{reply.text}
          </li>)}</ul>}
        </li>
      })}</ul>}
  </div>

  return shell(header, <>
    {library && <div role="tablist" aria-label="Artifact detail" className="flex gap-1 border-b px-3 py-2" onKeyDown={event => {
      const tabs = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
      const index = tabs.indexOf(event.target as HTMLButtonElement)
      const next = event.key === "ArrowRight" ? (index + 1) % tabs.length : event.key === "ArrowLeft" ? (index + tabs.length - 1) % tabs.length : event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : -1
      if (next >= 0) { event.preventDefault(); tabs[next].focus(); tabs[next].click() }
    }}>
      {([['preview', 'Preview'], ['versions', 'History'], ['comments', 'Comments']] as const).map(([tab, label]) => <button key={tab} type="button" role="tab" aria-selected={activeTab === tab}
        id={`artifact-detail-tab-${tab}`} aria-controls={`artifact-detail-panel-${tab}`} onClick={() => setActiveTab(tab)}
        tabIndex={activeTab === tab ? 0 : -1}
        className={cn("rounded-md px-3 py-1.5 text-sm coarse:min-h-[44px]", activeTab === tab ? "bg-muted font-semibold" : "text-muted-foreground hover:text-foreground")}>
        {label}
      </button>)}
    </div>}
    {toolbar}
    {(!library || activeTab === "preview") && subToolbar}
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="grid content-start gap-3 p-3">
        {banners}
        <p role="status" aria-live="polite" className="text-xs text-muted-foreground empty:hidden">{notice.text}</p>
        <div hidden={library && activeTab !== "versions"} {...(library ? { role: "tabpanel", id: "artifact-detail-panel-versions", "aria-labelledby": "artifact-detail-tab-versions" } : {})}>{history}</div>
        <div hidden={library && activeTab !== "preview"} {...(library ? { role: "tabpanel", id: "artifact-detail-panel-preview", "aria-labelledby": "artifact-detail-tab-preview" } : {})}>
        {canWrite && selectMode && !anchor && !viewingOld && (anchorable
          ? <p className="flex items-center gap-1.5 text-xs text-muted-foreground"><MousePointerClick className="size-3.5" aria-hidden />Click a part of the screen. The comment stays anchored to that element and version.</p>
          : <p role="status" className="flex items-center gap-1.5 text-xs text-muted-foreground"><Info className="size-3.5" aria-hidden />Nothing on this screen can take a comment yet: comments anchor to parts with an id, and this screen has none. Ask the agent in chat to add them.</p>)}
        {canvas}
        </div>
        <div hidden={library && activeTab !== "comments"} {...(library ? { role: "tabpanel", id: "artifact-detail-panel-comments", "aria-labelledby": "artifact-detail-tab-comments" } : {})}>{queue}</div>
        {canWrite && <p className="flex items-center gap-1.5 text-xs text-muted-foreground"><Info className="size-3.5 shrink-0" aria-hidden />A comment starts the agent only when you choose Send to agent.</p>}
      </div>
    </div>
  </>)
}
