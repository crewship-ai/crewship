"use client"
import { useEffect } from "react"
import { AppWindow, Lock, MessageSquare } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { useArtifactViewer } from "./artifact-viewer-context"
import { useArtifactProjectSnapshot } from "./use-artifact-project-data"
import type { ArtifactToolActivity } from "./tool-result"
import type { ArtifactProjectSnapshot } from "./types"

interface ArtifactChatCardProps {
  slug: string
  fallbackName?: string
  /** The tool step that produced this card; a newer version than the cached snapshot triggers a refetch. */
  activity?: ArtifactToolActivity | null
  /** Stable id of this card (its tool call id); the viewer rings only the card that opened it. */
  source?: string
}

const CARD = "flex w-full min-w-0 max-w-[520px] flex-wrap items-center gap-2.5 rounded-xl border bg-card py-2 pl-2 pr-2.5"
const CHIP = "rounded-full px-1.5 text-[11px] font-semibold leading-[1.6]"

function KindTile({ restricted }: { restricted?: boolean }) {
  const Icon = restricted ? Lock : AppWindow
  return <span aria-hidden="true" className={cn("flex size-8 shrink-0 items-center justify-center rounded-[28%]",
    restricted ? "bg-muted text-muted-foreground" : "bg-primary/10 text-primary")}>
    <Icon className="size-4" />
  </span>
}

function StateChips({ snapshot }: { snapshot: ArtifactProjectSnapshot }) {
  const { shared_version: shared, latest_version: latest } = snapshot
  // Draft whenever the newest version is not the shared one, even if an older one is.
  if (shared > 0 && shared === latest) return <span className={cn(CHIP, "bg-success/10 text-success")}>Shared</span>
  return <>
    <span className={cn(CHIP, "bg-muted text-muted-foreground ring-1 ring-inset ring-border")}>Draft</span>
    {shared > 0 && <span className={cn(CHIP, "bg-success/10 text-success")}>Shared v{shared}</span>}
  </>
}

/** Chat card for an artifact project. Everything shown comes from the live snapshot, never from tool text. */
export function ArtifactChatCard({ slug, fallbackName, activity, source }: ArtifactChatCardProps) {
  const viewer = useArtifactViewer()
  const query = useArtifactProjectSnapshot(viewer?.workspaceId ?? "", slug)
  const { data, error, isPending, refetch } = query
  const stepVersion = activity?.state === "done" ? activity.version : undefined
  const cachedVersion = data?.latest_version
  useEffect(() => {
    if (stepVersion !== undefined && cachedVersion !== undefined && stepVersion > cachedVersion) void refetch()
  }, [stepVersion, cachedVersion, refetch])

  if (!viewer) return null
  const isOpen = viewer.target?.slug === slug && viewer.target.source === source
  const from = source === undefined ? {} : { source }

  if (isPending) {
    return <div className={CARD} aria-busy="true" aria-label="Loading project">
      <Skeleton className="size-8 rounded-[28%]" />
      <div className="flex min-w-0 flex-1 flex-col gap-1.5"><Skeleton className="h-4 w-40" /><Skeleton className="h-3 w-28" /></div>
    </div>
  }

  if (error && (error.status === 403 || error.status === 404)) {
    // No name: the reader may not learn what the project is called.
    return <div className={CARD} data-testid="artifact-card-restricted">
      <KindTile restricted />
      <p className="min-w-0 flex-1 truncate text-sm text-muted-foreground">Restricted item · you don’t have access</p>
    </div>
  }

  if (error || !data) {
    return <div className={CARD}>
      <KindTile />
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-semibold">{fallbackName ?? slug}</p>
        <p className="text-xs text-muted-foreground">Couldn’t load</p>
      </div>
      <Button type="button" size="xs" variant="outline" onClick={() => void refetch()}>Retry</Button>
    </div>
  }

  const title = data.name ?? fallbackName ?? slug
  return <article aria-label={title} data-open={isOpen || undefined}
    className={cn(CARD, isOpen && "ring-2 ring-primary")}>
    <KindTile />
    <div className="grid min-w-0 flex-[1_1_140px] gap-0.5">
      <p className="flex min-w-0 items-center gap-1.5 text-sm font-semibold">
        <span className="truncate">{title}</span>
        <span className="shrink-0 rounded-[5px] border bg-muted px-[5px] font-mono text-[10px] font-semibold text-foreground/80">v{data.latest_version}</span>
      </p>
      <p className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs text-muted-foreground" data-testid="artifact-card-kind">
        <span>Project</span><StateChips snapshot={data} />
      </p>
    </div>
    <div className="flex flex-none items-center gap-1">
      <Button type="button" size="xs" variant="ghost" aria-label={`Comment on ${title}`}
        onClick={() => viewer.open({ slug, tab: "comments", ...from })}><MessageSquare />Comment</Button>
      <Button type="button" size="xs" variant="secondary" aria-label={`Open ${title}`}
        onClick={() => viewer.open({ slug, ...from })}>Open</Button>
    </div>
  </article>
}
