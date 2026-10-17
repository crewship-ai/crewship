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

import { useAccessMode } from "@/hooks/use-access-mode"
import { RestrictedIssues } from "@/components/features/issues/restricted-issues"
import { useCallback } from "react"
import { useRouter } from "next/navigation"
import { ArrowLeft, CircleDot, Link2 } from "lucide-react"
import { toast } from "@/lib/toast"

import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"
import { parseReturnTo } from "@/lib/return-to"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useUrlSegment } from "@/lib/use-url-segment"
import { SubBar, SubBarIconButton } from "@/components/layout/sub-bar"
import { PageLoading } from "@/components/layout/page-state"
import { IssueDetailSurface } from "@/components/features/issues/issue-detail-surface"
import { PageSaveBar, PageSaveProvider, usePageSaveGuard } from "@/components/ui/page-save-bar"

// Identifier read from the URL, not useParams() — see useUrlSegment for the
// static-export "_" placeholder bug this avoids. Module scope so the regex
// reference stays stable across renders.
const ISSUE_PATH_RE = /^\/issues\/([^/]+)\/?$/

export function IssuePageClient() {
  const mode = useAccessMode()
  const identifier = useUrlSegment(ISSUE_PATH_RE)
  if (mode === "loading") return null
  if (mode === "restricted") return identifier ? <RestrictedIssues identifier={identifier} /> : null
  // The issue's edits wait for one Save bar; Back asks while any wait.
  return (
    <PageSaveProvider>
      <IssuePage />
    </PageSaveProvider>
  )
}

function IssuePage() {
  const router = useRouter()

  // An issue is almost always opened FROM somewhere — an agent's Issues cell,
  // a routine, the board. The origin rides in the URL so Back returns there,
  // and survives a refresh or a pasted link, which router.back() would not.
  // parseReturnTo rejects anything that is not an in-app path.
  const [fromParam] = useShallowSearchParam("from")
  const [fromLabelParam] = useShallowSearchParam("fromLabel")
  const origin = parseReturnTo(fromParam, fromLabelParam)
  const guard = usePageSaveGuard()
  const back = useCallback(() => guard(() => router.push(origin?.href ?? "/issues")), [guard, router, origin?.href])

  const identifier = useUrlSegment(ISSUE_PATH_RE)
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { data: session } = useSession()

  const copyLink = useCallback(() => {
    navigator.clipboard
      .writeText(window.location.href)
      .then(() => toast.success("Link copied"))
      .catch(() => toast.error("Could not copy the link"))
  }, [])

  return (
    <div className="flex h-full flex-col bg-background">
      {/* The page's chrome is the top bar: where Back goes, the issue's ID as
          the last crumb, and the link to share. The issue's own identity —
          title, crew, status — is the first card below. */}
      <SubBar
        icon={CircleDot}
        title={origin?.label ?? "Issues"}
        section={<span className="font-mono">{identifier ?? "…"}</span>}
        ariaLabel="Issue"
        leading={
          <SubBarIconButton icon={ArrowLeft} onClick={back} aria-label={origin ? `Back to ${origin.label}` : "Back to issues"} />
        }
        actions={<SubBarIconButton icon={Link2} onClick={copyLink} aria-label="Copy link" title="Copy link" />}
      />

      <div className="relative min-h-0 flex-1">
      <div className="h-full overflow-y-auto pb-20">
        {wsLoading || !workspaceId || !identifier ? (
          <PageLoading variant="list" rows={4} />
        ) : (
          <IssueDetailSurface
            workspaceId={workspaceId}
            identifier={identifier}
            viewerInitial={session?.user?.name ?? session?.user?.email ?? "U"}
          />
        )}
      </div>
      <PageSaveBar />
      </div>
    </div>
  )
}
