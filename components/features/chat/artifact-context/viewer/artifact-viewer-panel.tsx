"use client"
import Link from "next/link"
import { useEffect, useMemo, useRef, useState } from "react"
import { AlertTriangle, ArrowLeft, ArrowUpRight, ChevronLeft, Clock, Eye, Gauge, Info, Link2, Lock, MessageSquare, MousePointerClick, OctagonAlert, Search, Send, X } from "lucide-react"
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
import { artifactDashboards, artifactDesignSystem, artifactsLibrary } from "@/lib/feature-flags"
import { parseDashboardManifest } from "@/lib/pages/artifact-dashboard"
import { artifactSlidesMode } from "@/lib/pages/artifact-slides"
import { useArtifactViewer, type ArtifactViewerTarget } from "../projects/artifact-viewer-context"
import { useArtifactProjectMutations, useArtifactProjectSnapshot } from "../projects/use-artifact-project-data"
import type { ArtifactComment, ArtifactProjectSnapshot } from "../projects/types"
import { ArtifactPreviewFrame, type PreviewSelection } from "./artifact-preview-frame"
import { ArtifactDashboardPreview } from "./artifact-dashboard-preview"
import { ArtifactSlidesPreview } from "./artifact-slides-preview"
import { ArtifactDesignSystemPreview } from "@/components/features/artifacts/design-system/design-system-preview"
import { hasDesignSystemFile } from "@/components/features/artifacts/design-system/manifest"
import { isValidAnchorSelector, screenLabel, screenLabels } from "./element-select"
import { artifactsItemHref } from "../artifacts-href"

export type ArtifactViewerVariant = "pane" | "fullscreen" | "library"

/** Queue state of a comment. Derived only from what the server sent: no status, no reply, no task → no chip. */
export function commentQueueChip(comment: ArtifactComment): { label: string; tone: StatusTone; live?: boolean } | null {
  const replyVersion = [...(comment.replies ?? [])].reverse().find(reply => reply.version > 0)?.version
  switch (comment.status) {
    case "pending": return { label: "Waiting", tone: "muted" }
    case "working": return { label: "Agent working", tone: "blue", live: true }
    case "answered": {
      const version = comment.answered_version ?? replyVersion
      return { label: version ? `Done · v${version}` : "Done", tone: "success" }
    }
    case "needs_clarification": return { label: "Needs a new selection", tone: "warn" }
  }
  if (replyVersion) return { label: `Done · v${replyVersion}`, tone: "success" }
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

/** A whole-viewer state: what happened and what to do next, without anything of the artifact itself. */
function ViewerState({ icon: Icon, title, text, action, children }: { icon: typeof Info; title: string; text: string; action?: React.ReactNode; children?: React.ReactNode }) {
  return <div className="grid content-start justify-items-start gap-2.5 p-6 text-sm">
    <span aria-hidden className="flex size-10 items-center justify-center rounded-full border bg-muted text-muted-foreground"><Icon className="size-5" /></span>
    <h2 className="text-sm font-semibold">{title}</h2>
    <p className="max-w-prose text-muted-foreground">{text}</p>
    {action}
    {children}
  </div>
}

/**
 * 403: the artifact exists and this person may not open it. There is no access
 * request route yet, so Request access hands them a ready request to send the owner.
 */
function NoAccess({ slug }: { slug: string }) {
  const [copied, setCopied] = useState<"idle" | "copied" | "failed">("idle")
  const request = async () => {
    const link = typeof window === "undefined" ? "" : window.location.href
    try {
      await navigator.clipboard.writeText(`Could you give me access to the artifact ${slug}? ${link}`.trim())
      setCopied("copied")
    } catch { setCopied("failed") }
  }
  return <ViewerState icon={Lock} title="You can't open this artifact" text="Ask the owner for access. Nothing of it is shown until they share it with you."
    action={<Button type="button" size="sm" onClick={() => void request()}>Request access</Button>}>
    <p role="status" className="text-xs text-muted-foreground empty:hidden">
      {copied === "copied" ? "Access request copied. Paste it to the owner." : copied === "failed" ? "Could not copy the request. Ask the owner for access." : ""}
    </p>
  </ViewerState>
}

/** True when the element a comment anchors to is still in that file. */
function hasAnchor(html: string, selector: string): boolean {
  if (!isValidAnchorSelector(selector)) return false
  if (typeof DOMParser === "undefined") return true
  try { return new DOMParser().parseFromString(html, "text/html").querySelector(selector) !== null } catch { return false }
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
      <header className={cn("flex shrink-0 items-center gap-2 border-b px-2.5", variant === "library" ? "min-h-11 flex-wrap py-1.5" : "h-11")}>
        {fullscreen && closeButton}
        {header}
        {!fullscreen && closeButton}
      </header>
      {body}
    </section>

  const status = errorOf(snapshot.error).status
  // Lost access wins over the cached copy: a background 403/404 must not leave the old preview on screen.
  if (snapshot.error && status === 403) return shell(<div className="flex-1" />, <NoAccess slug={target.slug} />)
  // A 404 never says whether the artifact exists: archived and private look the same.
  if (snapshot.error && status === 404) {
    return shell(<div className="flex-1" />, <ViewerState icon={Search} title="This artifact isn't available"
      text="It was archived, or the link points to a private version. We don't say which, so private work stays private."
      action={variant === "library" && <Button type="button" size="sm" variant="secondary" onClick={onClose}><ArrowLeft aria-hidden />All artifacts</Button>} />)
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
  const props = { data, target, setViewing, refetch: () => void snapshot.refetch(), workspaceId, shell, fullscreen, writeEnabled: !snapshot.error && data.can_write === true, readForRetry: async () => { const fresh = await snapshot.refetch(); return fresh && !fresh.error ? fresh.data : undefined } }
  return variant === "library"
    ? <LibraryLoadedViewer {...props} dispatchEnabled={props.writeEnabled && data.can_send_to_agent === true} />
    : <LoadedViewer {...props} />
}

interface LoadedProps {
  data: ArtifactProjectSnapshot
  target: ArtifactViewerTarget
  setViewing: (version: number | undefined) => void
  refetch: () => void
  readForRetry: () => Promise<ArtifactProjectSnapshot | undefined>
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

function readCommentDraft(key: string | null): string {
  if (!key) return ""
  try { return localStorage.getItem(key) ?? "" } catch { return "" }
}

function LoadedViewer({ data, target, setViewing, readForRetry, workspaceId, shell, fullscreen, writeEnabled, library = false, sendToOrigin }: LoadedProps) {
  const me = useSessionSafe()?.data?.user?.id
  const canWrite = writeEnabled && data.can_write === true
  const mayShare=canWrite && data.can_share === true
  const mayRestore=canWrite && data.can_restore === true
  const maySend=canWrite && data.can_send_to_agent === true
  const scope = JSON.stringify([me, workspaceId, data.page_id, data.origin?.chat_id ?? null, data.origin?.agent_id ?? null,mayShare,mayRestore,maySend])
  const permission = useRef({ scope, allowed: canWrite, epoch: 0 })
  if (permission.current.scope !== scope || permission.current.allowed !== canWrite) permission.current = { scope, allowed: canWrite, epoch: permission.current.epoch + 1 }
  const epoch = permission.current.epoch
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  // Keep ordinary polls/code updates from cancelling an acknowledged comment,
  // while stale callbacks from an old Page or write epoch cannot mutate or dispatch.
  const writeAllowed = () => mounted.current && permission.current.allowed && permission.current.epoch === epoch
  const chatDispatch = useArtifactViewer()?.sendToAgent
  const sendToAgent = maySend ? (library ? sendToOrigin : chatDispatch) : undefined
  const { restore, share, comment } = useArtifactProjectMutations(workspaceId, target.slug)
  // A comment saved for the agent whose chat message did not go out; Retry sends it again.
  const [undispatched, setUndispatched] = useState<ArtifactComment | null>(null)
  const confirmedDeliveries = useRef(new Set<string>())
  const retryInFlight = useRef(false)
  const [checkingRetry, setCheckingRetry] = useState(false)
  useEffect(() => { retryInFlight.current = false; setCheckingRetry(false) }, [epoch])
  const deliveryKey = (saved: ArtifactComment) => JSON.stringify([saved.id, saved.task_id, saved.version])
  const [historyOpen, setHistoryOpen] = useState(target.tab === "versions")
  const [activeTab, setActiveTab] = useState(target.tab ?? "preview")
  // A card asking for another section of the same project keeps the viewer (and an unsent comment).
  const [seenTab, setSeenTab] = useState(target.tab)
  if (seenTab !== target.tab) { setSeenTab(target.tab); setActiveTab(target.tab ?? "preview"); if (target.tab === "versions") setHistoryOpen(true) }
  const [screenPath, setScreenPath] = useState<string | null>(null)
  const [selectMode, setSelectMode] = useState(false)
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  // An unsent comment survives a crash or closed tab in this browser, per
  // person, workspace and Page; the selected place must be picked again.
  const draftKey = me ? `artifact-viewer-comment:${me}:${workspaceId}:${data.page_id}` : null
  const [draftState, setDraftState] = useState<{ key: string | null; text: string }>(() => ({ key: draftKey, text: readCommentDraft(draftKey) }))
  const draft = draftState.key === draftKey ? draftState.text : readCommentDraft(draftKey)
  const setDraft = (text: string) => {
    setDraftState({ key: draftKey, text })
    try { if (draftKey) { if (text) localStorage.setItem(draftKey, text); else localStorage.removeItem(draftKey) } } catch { /* the in-memory draft and Copy my comment remain */ }
  }
  const [banner, setBanner] = useState<Banner | null>(null)
  // A comment notice belongs to the version it was sent on and to that selection; see below.
  const [notice, setNotice] = useState<{ text: string; commentOn?: number }>({ text: "" })
  const [highlight, setHighlight] = useState<{ selector: string; seq: number } | null>(null)
  const [confirmShare, setConfirmShare] = useState(false)
  useEffect(() => {
    if (!canWrite) { setSelectMode(false); setAnchor(null) }
    if(!maySend)setUndispatched(null)
    if(!mayShare)setConfirmShare(false)
  }, [canWrite,maySend,mayShare])
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
  const designSystem = !slidesMode && artifactDesignSystem() && hasDesignSystemFile(data.manifest.files)
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
  // The API admits an agent run only for a comment saved with send_to_agent. The library saves it as a
  // waiting task (Add to queue) and Send to agent starts the waiting ones; there is no batch route.
  // "queue" saves the agent's task without starting it (Waiting); Send to agent in the queue starts it.
  const submit = (mode: "send" | "queue" | "save") => {
    const send = mode !== "save"
    if (!writeAllowed() || send && !maySend || viewingOld || !anchor || !draft.trim()) return
    comment.mutate({ version: anchor.version, path: anchor.path, selector: anchor.selector, quote: anchor.quote, text: draft.trim(), send_to_agent: send }, {
      onSuccess: saved => {
        if (!writeAllowed()) return
        resetComposer(); setBanner(null)
        if (mode === "send" && sendToAgent) void dispatch(saved)
        else if (mode === "queue" && sendToAgent) setNotice({ text: "Added to the queue. The agent starts when you send it.", commentOn: anchor.version })
        // Without a chat to start it, a comment for the agent waits as its task.
        else setNotice({ text: send ? "Comment saved for the agent" : "Comment saved", commentOn: anchor.version })
      },
      onError: fail,
    })
  }
  // The comment is saved as the agent's task; the run starts with a chat message bound to it.
  const deliveryConfirmed = (saved: ArtifactComment) => confirmedDeliveries.current.has(deliveryKey(saved)) || Boolean(saved.task_id && data.comments.some(current =>
    current.id === saved.id && current.task_id === saved.task_id && current.version === saved.version &&
    (current.status === "working" || current.status === "answered" || current.status === "needs_clarification" || current.replies?.some(reply => /^agent:/.test(reply.author ?? ""))),
  ))
  // Authoritative task progress wins over a missing or late socket ACK. Clear
  // the old failure before it paints, so Retry cannot launch an answered task.
  if (undispatched && deliveryConfirmed(undispatched)) setUndispatched(null)
  /** Starts the agent on one saved task; a failure keeps it for Retry. */
  const deliver = async (saved: ArtifactComment): Promise<boolean> => {
    if (!writeAllowed() || !maySend || deliveryConfirmed(saved)) return false
    setUndispatched(null)
    const started = sendToAgent ? await sendToAgent({ pageId: data.page_id, comment: saved }).catch(() => false) : false
    if (!writeAllowed()) return false
    if (started) confirmedDeliveries.current.add(deliveryKey(saved))
    else setUndispatched(saved)
    return started
  }
  const dispatch = async (saved: ArtifactComment) => {
    if (!writeAllowed() || !maySend || deliveryConfirmed(saved)) return
    if (await deliver(saved)) setNotice({ text: "Comment sent to the agent", commentOn: saved.version })
  }
  const recoverable = (snapshot: ArtifactProjectSnapshot, saved: ArtifactComment) =>
    snapshot.can_write === true && snapshot.can_send_to_agent === true && snapshot.origin?.chat_id && snapshot.origin.agent_id &&
    snapshot.manifest.version === snapshot.latest_version && saved.version === snapshot.latest_version &&
    saved.delivery_retryable === true && saved.status === "pending" && Boolean(saved.task_id) &&
    saved.author === me && !saved.answered_version && !saved.replies?.length
  // Tasks saved for the agent and not started yet. The library queues them on purpose (Send to agent
  // starts them); a chat pane only gets them from a send that did not go out, so it offers Retry.
  const waitingTasks = sendToAgent ? data.comments.filter(saved => recoverable(data, saved) && !deliveryConfirmed(saved)) : []
  const queuedRetry = library ? undefined : waitingTasks[0]
  // A legacy immediate failure remains visible until the fresh server snapshot
  // classifies it. Reopening recovers only explicitly eligible persisted tasks.
  const localRetry = undispatched && !deliveryConfirmed(undispatched) &&
    data.comments.find(saved => saved.id === undispatched.id)?.delivery_retryable !== false ? undispatched : undefined
  const retryComment = localRetry ?? queuedRetry
  const retry = async (saved: ArtifactComment) => {
    if (!writeAllowed() || !maySend || retryInFlight.current || deliveryConfirmed(saved)) return
    retryInFlight.current = true; setCheckingRetry(true)
    try {
      // A cached Waiting task may have been ACKed or claimed while this viewer
      // was closed. Refresh before any send; only the existing admission grants
      // execution. Refetch errors and changed identity/origin fail closed.
      const fresh = await readForRetry().catch(() => undefined)
      if (!writeAllowed() || !fresh || fresh.page_id !== data.page_id ||
        fresh.origin?.chat_id !== data.origin?.chat_id || fresh.origin?.agent_id !== data.origin?.agent_id) return
      const current = fresh.comments.find(candidate => candidate.id === saved.id && candidate.task_id === saved.task_id &&
        candidate.version === saved.version && candidate.path === saved.path && candidate.selector === saved.selector && candidate.quote === saved.quote)
      if (!current || !recoverable(fresh, current)) return
      await dispatch(current)
    } finally {
      if (mounted.current && permission.current.epoch === epoch) {
        retryInFlight.current = false
        setCheckingRetry(false)
      }
    }
  }
  // One fresh read, then each waiting task in order; the first that does not start stops the run and offers Retry.
  const sendWaiting = async () => {
    if (!writeAllowed() || !maySend || retryInFlight.current || waitingTasks.length === 0) return
    retryInFlight.current = true; setCheckingRetry(true)
    try {
      const chosen = new Set(waitingTasks.map(saved => saved.id))
      const fresh = await readForRetry().catch(() => undefined)
      if (!writeAllowed() || !fresh || fresh.page_id !== data.page_id ||
        fresh.origin?.chat_id !== data.origin?.chat_id || fresh.origin?.agent_id !== data.origin?.agent_id) return
      let sent = 0
      for (const saved of fresh.comments.filter(c => chosen.has(c.id) && recoverable(fresh, c) && !deliveryConfirmed(c))) {
        if (!writeAllowed() || !await deliver(saved)) break
        sent++
      }
      if (sent && writeAllowed()) setNotice({ text: `Sent ${sent} ${sent === 1 ? "comment" : "comments"} to the agent` })
    } finally {
      if (mounted.current && permission.current.epoch === epoch) {
        retryInFlight.current = false
        setCheckingRetry(false)
      }
    }
  }
  // 409: read the newest version and put the kept comment back on the same element there.
  const reapply = async () => {
    if (!writeAllowed()) return
    setBanner(null); setViewing(undefined)
    const fresh = await readForRetry().catch(() => undefined)
    if (!writeAllowed() || !fresh || !anchor) return
    const file = fresh.manifest.files.find(f => f.path === anchor.path)
    if (fresh.manifest.version === fresh.latest_version && file && hasAnchor(file.content, anchor.selector)) setAnchor({ ...anchor, version: fresh.latest_version })
    else { setAnchor(null); setSelectMode(true); setBanner("anchor") }
  }
  const openHistory = () => {
    if (library) setActiveTab("versions")
    else setHistoryOpen(true)
    window.requestAnimationFrame(() => historyRef.current?.focus())
  }
  const copyDraft = async () => {
    try { await navigator.clipboard.writeText(draft); if (writeAllowed()) setNotice({ text: "Comment copied" }) } catch { if (writeAllowed()) setNotice({ text: "Could not copy the comment" }) }
  }
  const jumpTo = (c: ArtifactComment) => {
    if (library) setActiveTab("preview")
    if (screens.includes(c.path)) setScreenPath(c.path)
    if (isValidAnchorSelector(c.selector)) setHighlight(prev => ({ selector: c.selector, seq: (prev?.seq ?? 0) + 1 }))
    canvasRef.current?.scrollIntoView?.({ block: "nearest" })
  }
  const doRestore = (version: number) => {
    if (!writeAllowed() || !mayRestore) return
    restore.mutate({ base_version: latest, version }, {
      onSuccess: result => { if (!writeAllowed()) return; setNotice({ text: `Restored v${version} as v${result.version} · nothing deleted` }); setViewing(undefined) },
      onError: fail,
    })
  }
  const doShare = (version: number) => {
    if (!writeAllowed() || !mayShare) return
    share.mutate({ base_version: latest, version }, {
      onSuccess: result => { if (!writeAllowed()) return; setConfirmShare(false); setNotice({ text: `Shared v${result.shared_version}` }) },
      onError: error => { if (!writeAllowed()) return; setConfirmShare(false); fail(error) },
    })
  }

  // Latest and the shared version are the two versions people talk about; another one only via history.
  const versionValue = !viewingOld ? "latest" : shown === sharedVersion ? "shared" : `v${shown}`
  const versionOptions = [
    { value: "latest", label: `Latest · v${latest}` },
    ...(sharedVersion > 0 && sharedVersion !== latest ? [{ value: "shared", label: `Shared · v${sharedVersion}` }] : []),
    ...(viewingOld && shown !== sharedVersion ? [{ value: `v${shown}`, label: `v${shown}` }] : []),
  ]
  const pickVersion = (value: string) => setViewing(value === "latest" ? undefined : value === "shared" ? sharedVersion : Number(value.slice(1)))
  const versionNumber = (text: string) => <b className="font-mono font-semibold">{text}</b>
  // The library detail names both versions in its header, Shared even when there is none yet.
  const librarySwitch = <Segmented ariaLabel="Version" size="xs" value={versionValue} onChange={pickVersion} options={[
    { value: "latest", label: <>Latest {versionNumber(`v${latest}`)}</> },
    { value: "shared", label: <>Shared {versionNumber(sharedVersion > 0 ? `v${sharedVersion}` : "—")}</>, disabled: sharedVersion === 0, title: sharedVersion === 0 ? "No version is shared yet" : undefined },
    ...(viewingOld && shown !== sharedVersion ? [{ value: `v${shown}`, label: versionNumber(`v${shown}`) }] : []),
  ]} />

  const header = <>
    <h2 className="min-w-0 truncate text-sm font-semibold">{data.name ?? data.slug ?? target.slug}</h2>
    <Badge variant="outline" className="shrink-0">{slidesMode === "slides" ? "Slides" : slidesMode === "unsupported" ? "Artifact" : designSystem ? "Design System" : artifactDashboards() && parseDashboardManifest(data.manifest.files) ? "Dashboard" : "Project"}</Badge>
    <StatusPill tone={isShared ? "success" : "muted"} label={isShared ? "Shared" : "Draft"} />
    <div className="flex-1" />
    {library && librarySwitch}
    {/* The chat shows the artifact beside the conversation; its home, with the library, list and history, is Artifacts. */}
    {!library && artifactsLibrary() && <Button asChild type="button" variant="ghost" size="sm" className="h-7 shrink-0 gap-1 px-2 coarse:min-h-[44px]">
      <Link href={artifactsItemHref(workspaceId, data.page_id)}>Open in Artifacts<ArrowUpRight className="size-3.5" aria-hidden /></Link>
    </Button>}
  </>
  const shareTarget = shown
  const canShare = mayShare && shareTarget !== sharedVersion

  const toolbar = <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b px-3 py-2">
    {!library && <Segmented ariaLabel="Version" size="xs" value={versionValue} options={versionOptions} onChange={pickVersion} />}
    {!library && versions.length > 0 && <Button type="button" variant="ghost" size="sm" className="h-7 gap-1.5" aria-pressed={historyOpen} onClick={() => setHistoryOpen(open => !open)}>
      <Clock className="size-3.5" aria-hidden />History
    </Button>}
    <div className="flex-1" />
    {canWrite && <Link className="inline-flex items-center px-2 text-sm underline coarse:min-h-[44px]" href={`/pages/${encodeURIComponent(target.slug)}?${new URLSearchParams({artifact:"1",workspace_id:workspaceId})}`}>Edit source</Link>}
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
    {banner === "conflict" && <ViewerBanner tone="warn" title="Changed meanwhile" action={<Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" onClick={() => void reapply()}>Reload and reapply</Button>}>
      <span>Someone saved a newer version while you were working. Nothing was overwritten. Your comment and selection are kept; Reload and reapply puts them on the newest version.</span>
    </ViewerBanner>}
    {banner === "quota_full" && <ViewerBanner tone="bad" title="Saving is paused" action={<span className="flex shrink-0 flex-wrap gap-1.5">
      {canWrite && versions.length > 0 && <Button type="button" size="sm" variant="secondary" className="h-7" onClick={openHistory}>Manage versions</Button>}
      {draft.trim() && <Button type="button" size="sm" variant="ghost" className="h-7" onClick={() => void copyDraft()}>Copy my comment</Button>}
    </span>}>
      <span>History is full, so this was not saved. Your comment stays here; nothing in the history is deleted without you.</span>
    </ViewerBanner>}
    {quota?.warning && banner !== "quota_full" && <ViewerBanner tone="warn" icon={Gauge} title="This project is close to its limit"
      action={canWrite && versions.length > 0 && <Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" onClick={openHistory}>Review versions</Button>}>
      <span>{fmtSize(quota.used_bytes)} of {fmtSize(quota.limit_bytes)} ({pct}%). History is never deleted without you. Choose what to do before saving fails.</span>
      <div role="progressbar" aria-label="History storage used" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} className="h-1.5 overflow-hidden rounded-full bg-background/60">
        <div className="h-full rounded-full bg-warn" style={{ width: `${pct}%` }} />
      </div>
    </ViewerBanner>}
    {banner === "anchor" && <ViewerBanner tone="info">This element changed. Select it again.</ViewerBanner>}
    {banner === "failed" && <ViewerBanner tone="bad">That did not go through. Nothing was changed; try again.</ViewerBanner>}
    {maySend && retryComment && <ViewerBanner tone="warn" title="The agent did not start" action={<Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" disabled={checkingRetry} onClick={() => void retry(retryComment)}>Retry</Button>}>
      The comment is saved. Sending it to the agent in this chat did not go through.</ViewerBanner>}
    {viewingOld && shown === sharedVersion && <ViewerBanner tone="info" icon={Eye}
      action={mayShare && <Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" disabled={share.isPending} onClick={() => doShare(latest)}>Share v{latest}</Button>}>
      You see the shared version v{shown}, the one people with the link get. Latest is v{latest}.
    </ViewerBanner>}
    {viewingOld && shown !== sharedVersion && <ViewerBanner tone="info" icon={Eye} action={<Button type="button" size="sm" variant="secondary" className="h-7 shrink-0" onClick={() => setViewing(undefined)}>Back to latest</Button>}>
      You are viewing v{shown}. Latest is v{latest}.
    </ViewerBanner>}
  </>

  const history = (library || historyOpen) && versions.length > 0 && <div ref={historyRef} tabIndex={-1} role="region" aria-label="Version history" className="rounded-card border bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
    <p className="border-b px-3 py-2 text-xs text-muted-foreground">{versions.length} {versions.length === 1 ? "version" : "versions"} · restore makes a new version, nothing is deleted</p>
    <ul>{versions.map(v => <li key={v.version} className="flex flex-wrap items-center gap-2.5 border-t px-3 py-2 first:border-t-0">
      <span data-slot="version-number" className="shrink-0 rounded-sm border bg-muted px-[5px] font-mono text-micro font-semibold text-foreground/80">v{v.version}</span>
      <div className="grid min-w-0 flex-[1_1_180px] grid-cols-[minmax(0,1fr)]">
        <b className="truncate text-sm font-semibold">{[authorLabel(v.actor, me), v.message].filter(Boolean).join(" · ") || `Version ${v.version}`}</b>
        <small className="text-xs text-muted-foreground">{[v.created_at && timeAgo(v.created_at), v.restored_from ? `Restored from v${v.restored_from}` : null].filter(Boolean).join(" · ")}</small>
      </div>
      {v.version === latest && <StatusPill tone="muted" label="Latest" />}
      {v.version === sharedVersion && <StatusPill tone="success" label="Shared" />}
      {v.version !== shown && <Button type="button" size="sm" variant="ghost" className="h-7" onClick={() => setViewing(v.version === latest ? undefined : v.version)}>View</Button>}
      {mayRestore && v.version !== latest && <Button type="button" size="sm" variant="ghost" className="h-7" disabled={restore.isPending} onClick={() => doRestore(v.version)}>Restore as new version</Button>}
    </li>)}</ul>
  </div>

  const pinNumber = data.comments.length + 1
  const pinCard = canWrite && (anchor || draft) && !viewingOld && <form aria-label="New comment" onSubmit={event => { event.preventDefault(); submit(maySend ? "send" : "save") }}
    className="z-[2] mt-2.5 flex flex-wrap items-start gap-2 rounded-card border bg-card px-2.5 py-2 shadow-md @md:absolute @md:right-[18px] @md:top-[18px] @md:mt-0 @md:max-w-[min(320px,calc(100%-36px))]">
    <span aria-hidden className={cn(PIN, "size-6 bg-primary text-xs text-primary-foreground")}>{pinNumber}</span>
    <div className="grid min-w-0 flex-[1_1_160px] gap-1.5">
      <b className="truncate text-xs font-semibold">{anchor ? `On ${anchor.selector}${anchor.quote ? ` · ${elementLabel(anchor.selector, anchor.quote)}` : ""} · v${anchor.version}` : "Select an element to attach this comment"}</b>
      <Textarea aria-label="Comment" value={draft} onChange={event => setDraft(event.target.value)} rows={3} placeholder="What should change here?" className="min-h-16 text-sm" />
    </div>
    <div className="flex w-full flex-wrap justify-end gap-1.5">
      <Button type="button" size="sm" variant="ghost" onClick={resetComposer}>Cancel</Button>
      {library && maySend
        ? <Button type="button" size="sm" variant="secondary" disabled={!anchor || !draft.trim() || comment.isPending} onClick={() => submit("queue")}>Add to queue</Button>
        : <Button type="button" size="sm" variant="secondary" disabled={!anchor || !draft.trim() || comment.isPending} onClick={() => submit("save")}>Save comment</Button>}
      {maySend && <Button type="submit" size="sm" disabled={!anchor || !draft.trim() || comment.isPending}>Send to agent</Button>}
    </div>
  </form>

  const canvas = <div ref={canvasRef} className="relative rounded-card bg-muted p-3.5">
    <div className={cn("overflow-hidden rounded-md border bg-white", fullscreen ? "h-[max(320px,calc(100dvh-14rem))]" : "h-[min(70vh,560px)] min-h-[320px]")}>
      {slidesMode === "unsupported" ? <p role="alert" className="p-4">This Slides format is unsupported.</p>
        : slidesMode === "slides" ? <ArtifactSlidesPreview workspaceId={workspaceId} pageId={data.page_id} codeVersion={shown} artifactKind={data.artifact_kind} path={screenPath ?? ""} title={`Preview of ${labelOf(screen)}`} selectMode={canWrite && selectMode && !viewingOld} onSelect={select} onNavigate={setScreenPath} />
        : designSystem ? <ArtifactDesignSystemPreview files={data.manifest.files} title={`Preview of ${data.name ?? data.slug ?? target.slug}`} />
        : artifactDashboards() && data.manifest.files.some(file => file.path === "dashboard.json")
        ? <ArtifactDashboardPreview workspaceId={workspaceId} pageId={data.page_id} codeVersion={shown} files={data.manifest.files} path={screen} selectMode={canWrite && selectMode && !viewingOld} title={`Preview of ${labelOf(screen)}`} onSelect={select} onNavigate={setScreenPath} />
        : <ArtifactPreviewFrame html={html} files={data.manifest.files} screens={screens} path={screen} selectMode={canWrite && selectMode && !viewingOld} title={`Preview of ${labelOf(screen)}`}
          highlight={highlight} onSelect={select} onNavigate={setScreenPath} />}
    </div>
    {pinCard}
  </div>

  const queueItems = data.comments.length === 0
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
    })}</ul>
  // In the library a comment waits until Send to agent starts it (decision 5); the chat pane sends from the pin card.
  const sendBar = library && maySend && sendToAgent && <div className="grid gap-1.5 border-t px-3 py-2.5">
    <Button type="button" size="sm" className="justify-self-start" disabled={waitingTasks.length === 0 || checkingRetry} onClick={() => void sendWaiting()}>
      <Send aria-hidden className="size-3.5" />Send {waitingTasks.length} to agent
    </Button>
    <p className="text-xs text-muted-foreground">A comment starts the agent only when you send it.</p>
  </div>
  const queueHead = <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
    <MessageSquare className="size-4 text-muted-foreground" aria-hidden />
    <b className="text-sm font-semibold">Comments for agent</b>
    <span className="text-xs tabular-nums text-muted-foreground">{data.comments.length}</span>
  </div>

  const queue = <div ref={queueRef} tabIndex={-1} role="region" aria-label="Comment queue" className="rounded-card border bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
    {queueHead}
    {queueItems}
    {sendBar}
  </div>

  // Beside Preview on a wide detail; a narrow one (app ≤ 1180 px) hides it and keeps the queue under Comments.
  const rail = library && activeTab === "preview" && <aside aria-label="Comments beside preview" className="hidden w-[300px] shrink-0 flex-col overflow-auto border-l @min-[56rem]:flex">
    {queueHead}
    <div className="min-h-0 flex-1">{queueItems}</div>
    {sendBar}
  </aside>

  const waitingCount = data.comments.filter(c => c.status === "pending").length
  const tabCount = (n: number) => n > 0 && <span aria-hidden className="ml-1.5 font-mono text-micro text-muted-foreground">{n}</span>

  return shell(header, <>
    {library && <div role="tablist" aria-label="Artifact detail" className="flex gap-1 border-b px-3 py-2" onKeyDown={event => {
      const tabs = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
      const index = tabs.indexOf(event.target as HTMLButtonElement)
      const next = event.key === "ArrowRight" ? (index + 1) % tabs.length : event.key === "ArrowLeft" ? (index + tabs.length - 1) % tabs.length : event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : -1
      if (next >= 0) { event.preventDefault(); tabs[next].focus(); tabs[next].click() }
    }}>
      {([['preview', 'Preview', 0], ['versions', 'History', versions.length], ['comments', 'Comments', waitingCount]] as const).map(([tab, label, count]) => <button key={tab} type="button" role="tab" aria-selected={activeTab === tab}
        id={`artifact-detail-tab-${tab}`} aria-controls={`artifact-detail-panel-${tab}`} onClick={() => setActiveTab(tab)}
        tabIndex={activeTab === tab ? 0 : -1}
        className={cn("rounded-md px-3 py-1.5 text-sm coarse:min-h-[44px]", activeTab === tab ? "bg-muted font-semibold" : "text-muted-foreground hover:text-foreground")}>
        {label}{tabCount(count)}
      </button>)}
    </div>}
    {/* In the library the version switch sits in the header; the bar stays for source editing and Share. */}
    {(!library || canWrite) && toolbar}
    {(!library || activeTab === "preview") && subToolbar}
    <div className="flex min-h-0 flex-1">
      <div className="min-h-0 min-w-0 flex-1 overflow-auto">
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
          {maySend && !library && <p className="flex items-center gap-1.5 text-xs text-muted-foreground"><Info className="size-3.5 shrink-0" aria-hidden />A comment starts the agent only when you choose Send to agent.</p>}
        </div>
      </div>
      {rail}
    </div>
  </>)
}
