"use client"

// /issues/<identifier> — the canonical issue deep link.
//
// It used to be a second, entirely separate issue screen: its own header, its
// own 320px sidebar, its own Tiptap wiring, its own comment box — none of it
// shared with the detail you got by clicking a row inside /issues. Same issue,
// two screens, and which one you saw depended on how you arrived.
//
// What is left here is the part that is genuinely about being a PAGE: the
// Issues panel beside it, where Back goes, ↑/↓ to the previous and next
// issue, the ⋯ menu and ✕ (v67 I-FULL, issues.js:182-187). The issue itself
// is IssueDetailSurface, the same component the peek's full page renders.

import { useAccessMode } from "@/hooks/use-access-mode"
import { RestrictedIssues } from "@/components/features/issues/restricted-issues"
import { useCallback, useMemo, useState } from "react"
import { useRouter } from "next/navigation"
import { ArrowLeft, ChevronDown, ChevronUp, CircleDot, Link2, X } from "lucide-react"
import { toast } from "@/lib/toast"

import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"
import { buildReturnTo, parseReturnTo } from "@/lib/return-to"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useUrlSegment } from "@/lib/use-url-segment"
import { SubBar, SubBarIconButton } from "@/components/layout/sub-bar"
import { PageLoading } from "@/components/layout/page-state"
import { IssueDetailSurface } from "@/components/features/issues/issue-detail-surface"
import { IssueOptionsMenu } from "@/components/features/issues/issue-options-menu"
import type { GroupBy, OrderBy } from "@/components/features/issues/issues-view-model"
import { IssuePagePanel, useIssuePageLists } from "@/components/features/orchestration/issue-page-panel"
import { issueNeighbours, issueSequence } from "@/components/features/orchestration/issues-page-links"
import { PageSaveBar, PageSaveProvider, usePageSaveGuard } from "@/components/ui/page-save-bar"
import type { Mission } from "@/lib/types/mission"

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
  const isMobile = useIsMobile()

  // An issue is almost always opened FROM somewhere — an agent's Issues cell,
  // a routine, the board. The origin rides in the URL so Back returns there,
  // and survives a refresh or a pasted link, which router.back() would not.
  // parseReturnTo rejects anything that is not an in-app path.
  const [fromParam] = useShallowSearchParam("from")
  const [fromLabelParam] = useShallowSearchParam("fromLabel")
  const origin = parseReturnTo(fromParam, fromLabelParam)
  const guard = usePageSaveGuard()
  const go = useCallback((href: string) => guard(() => router.push(href)), [guard, router])
  const back = useCallback(() => go(origin?.href ?? "/issues"), [go, origin?.href])
  // ✕ is "back to the list": the list the reader came from, else all issues.
  const toList = useCallback(() => go(origin?.href.startsWith("/issues") ? origin.href : "/issues"), [go, origin?.href])

  const identifier = useUrlSegment(ISSUE_PATH_RE)
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { data: session } = useSession()
  const me = session?.user?.id ?? null

  // The panel's lists, and ↑/↓ through the list the reader came from, in
  // their own grouping and order.
  const lists = useIssuePageLists(workspaceId)
  const [groupBy] = useUserPreference<GroupBy>("issues.list.groupBy", "status")
  const [orderBy] = useUserPreference<OrderBy>("issues.list.order", "manual")
  const neighbours = useMemo(
    () => (identifier ? issueNeighbours(issueSequence(lists.issues, origin?.href, { groupBy, orderBy }), identifier) : { prev: null, next: null }),
    [lists.issues, origin?.href, groupBy, orderBy, identifier],
  )
  const step = (to: string | null) => {
    if (!to) return
    const keep = origin ? `?${buildReturnTo(origin.href, origin.label)}` : ""
    go(`/issues/${encodeURIComponent(to)}${keep}`)
  }

  // The ⋯ menu acts on the issue the surface loaded, then has it reload.
  const [loaded, setLoaded] = useState<Mission | null>(null)
  const [reloadKey, setReloadKey] = useState(0)

  const copyLink = useCallback(() => {
    navigator.clipboard
      .writeText(window.location.href)
      .then(() => toast.success("Link copied"))
      .catch(() => toast.error("Could not copy the link"))
  }, [])

  return (
    <div className="flex h-full flex-col bg-background">
      {/* The page's chrome is the top bar: where Back goes, the issue's ID as
          the last crumb, ↑/↓, the link to share, ⋯ and ✕. The issue's own
          identity — title, crew, status — is the first card below. */}
      <SubBar
        icon={CircleDot}
        title={origin?.label ?? "Issues"}
        section={<span className="font-mono">{identifier ?? "…"}</span>}
        ariaLabel="Issue"
        leading={
          <SubBarIconButton icon={ArrowLeft} onClick={back} aria-label={origin ? `Back to ${origin.label}` : "Back to issues"} />
        }
        actions={
          <>
            {!isMobile && (
              <>
                <SubBarIconButton icon={ChevronUp} onClick={() => step(neighbours.prev)} disabled={!neighbours.prev} aria-label="Previous issue" title="Previous issue" />
                <SubBarIconButton icon={ChevronDown} onClick={() => step(neighbours.next)} disabled={!neighbours.next} aria-label="Next issue" title="Next issue" />
                <SubBarIconButton icon={Link2} onClick={copyLink} aria-label="Copy link" title="Copy link" />
              </>
            )}
            {loaded && workspaceId && (
              <IssueOptionsMenu
                issue={loaded}
                projects={lists.projects}
                workspaceId={workspaceId}
                onChanged={() => setReloadKey((n) => n + 1)}
                onDeleted={() => router.push(origin?.href.startsWith("/issues") ? origin.href : "/issues")}
              />
            )}
            {!isMobile && <SubBarIconButton icon={X} onClick={toList} aria-label="Back to list" title="Back to list" />}
          </>
        }
      />

      <div className="relative flex min-h-0 flex-1">
        <IssuePagePanel lists={lists} me={me} navigate={go} />
        <div className="relative min-h-0 min-w-0 flex-1">
          <div className="h-full overflow-y-auto pb-20">
            {wsLoading || !workspaceId || !identifier ? (
              <PageLoading variant="list" rows={4} />
            ) : (
              <IssueDetailSurface
                workspaceId={workspaceId}
                identifier={identifier}
                viewerInitial={session?.user?.name ?? session?.user?.email ?? "U"}
                viewerId={me}
                reloadKey={reloadKey}
                onLoaded={setLoaded}
              />
            )}
          </div>
          <PageSaveBar />
        </div>
      </div>
    </div>
  )
}
