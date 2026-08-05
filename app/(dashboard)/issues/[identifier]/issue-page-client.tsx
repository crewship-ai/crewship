"use client"

// /issues/<identifier> — the canonical issue deep link.
//
// It used to be a second, entirely separate issue screen: its own header, its
// own 320px sidebar, its own Tiptap wiring, its own comment box — none of it
// shared with the detail you got by clicking a row inside /issues. Same issue,
// two screens, and which one you saw depended on how you arrived.
//
// What is left here is the part that is genuinely about being a PAGE: where
// Back goes, and the link you copy. The issue itself is IssueDetailSurface,
// the same component the centre pane of /issues renders.

import { useCallback } from "react"
import { useRouter } from "next/navigation"
import { ArrowLeft, ChevronRight, Link2 } from "lucide-react"
import { toast } from "sonner"

import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"
import { parseReturnTo } from "@/lib/return-to"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useUrlSegment } from "@/lib/use-url-segment"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { Skeleton } from "@/components/ui/skeleton"
import { IssueDetailSurface } from "@/components/features/issues/issue-detail-surface"

// Identifier read from the URL, not useParams() — see useUrlSegment for the
// static-export "_" placeholder bug this avoids. Module scope so the regex
// reference stays stable across renders.
const ISSUE_PATH_RE = /^\/issues\/([^/]+)\/?$/

export function IssuePageClient() {
  const router = useRouter()

  // An issue is almost always opened FROM somewhere — an agent's Issues cell,
  // a routine, the board. The origin rides in the URL so Back returns there,
  // and survives a refresh or a pasted link, which router.back() would not.
  // parseReturnTo rejects anything that is not an in-app path.
  const [fromParam] = useShallowSearchParam("from")
  const [fromLabelParam] = useShallowSearchParam("fromLabel")
  const origin = parseReturnTo(fromParam, fromLabelParam)
  const back = useCallback(() => router.push(origin?.href ?? "/issues"), [router, origin?.href])

  const identifier = useUrlSegment(ISSUE_PATH_RE)
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { data: session } = useSession()

  const copyLink = useCallback(() => {
    navigator.clipboard
      .writeText(window.location.href)
      .then(() => toast.success("Link copied to clipboard"))
      .catch(() => toast.error("Failed to copy link"))
  }, [])

  return (
    <div className="flex h-full flex-col bg-background">
      {/* One line of page chrome: where Back goes, and the link to share. The
          issue's own identity — title, identifier, crew, status — is the
          first card below, so the header does not repeat it. */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2">
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 text-muted-foreground hover:text-foreground"
          onClick={back}
          aria-label={origin ? `Back to ${origin.label}` : "Back to issues"}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <nav className="flex min-w-0 items-center gap-1 text-[12px] text-muted-foreground">
          <button className="transition-colors hover:text-foreground" onClick={back}>
            {origin?.label ?? "Issues"}
          </button>
          <ChevronRight className="h-3 w-3 shrink-0" />
          <span className="truncate font-mono text-foreground/85">{identifier ?? "…"}</span>
        </nav>
        <div className="ml-auto flex items-center gap-1">
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-muted-foreground hover:text-foreground"
                onClick={copyLink}
                aria-label="Copy link"
              >
                <Link2 className="h-3.5 w-3.5" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Copy link</TooltipContent>
          </Tooltip>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {wsLoading || !workspaceId || !identifier ? (
          <div className="flex flex-col gap-4 p-4">
            <Skeleton className="h-[132px] w-full rounded-xl" />
            <Skeleton className="h-[64px] w-full rounded-xl" />
          </div>
        ) : (
          <IssueDetailSurface
            workspaceId={workspaceId}
            identifier={identifier}
            viewerInitial={session?.user?.name ?? session?.user?.email ?? "U"}
          />
        )}
      </div>
    </div>
  )
}
