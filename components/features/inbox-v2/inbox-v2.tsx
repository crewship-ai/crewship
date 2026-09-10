"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useQuery } from "@tanstack/react-query"
import { Inbox, ShieldX } from "lucide-react"
import { toast } from "sonner"

import type { WorkspaceRole } from "@/components/features/inbox/inbox-derive"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { SubBar } from "@/components/layout/sub-bar"
import { useApprovals, decideApproval } from "@/hooks/use-approvals"
import { useInbox, useInboxItem } from "@/hooks/use-inbox"
import { useRealtimeEvent, useRealtimeStatusSafe } from "@/hooks/use-realtime"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { inboxBulk } from "@/lib/api/inbox"
import { isAdminTier } from "@/lib/permissions/tiers"
import type { Mission } from "@/lib/types/mission"
import { cn } from "@/lib/utils"

import {
  approvalEntry, EMPTY_INBOX_V2_FILTERS, INBOX_V2_TYPES,
  groupAdvisories, inboxEntry, missionEntries, selectEntry, suppressedApprovalIDs,
  type InboxV2Filters,
} from "./inbox-v2-derive"
import { useInboxV2DeepLink } from "./inbox-v2-deeplink"
import { entryIdentity, filterInboxEntries } from "./inbox-entry-identity"
import { InboxV2Detail } from "./inbox-v2-detail"
import { InboxV2Explorer } from "./inbox-v2-explorer"
import { useInboxLookup } from "@/components/features/inbox/use-inbox-lookup"
import type { InboxRoutineRef, InboxV2Confirmation, InboxV2Entry, InboxV2View } from "./inbox-v2-types"

export function InboxV2() {
  const { workspaceId, role } = useWorkspace()
  const params = useSearchParams()
  const router = useRouter()
  const detailRef = useRef<HTMLElement>(null)
  const requestedView = params?.get("view") ?? null
  const requestedKind = params?.get("kind") ?? null
  const requestedID = params?.get("item") ?? null
  const requestedSearch = params?.get("agent") ?? params?.get("filter") ?? ""
  const [chosenView, setView] = useState<InboxV2View | null>(null)
  // `request:<id>` rather than `inbox:<id>`: the caller of ?item= knows an id,
  // not which source owns it, and an approval-queue deep link keyed
  // `approval:<id>` could never match. selectEntry resolves it against both.
  // useInboxV2DeepLink below keeps both this and `filters.search` in step with
  // the URL after mount.
  const [selectedKey, setSelectedKey] = useState<string | null>(requestedID ? `request:${requestedID}` : null)
  const [confirmation, setConfirmation] = useState<InboxV2Confirmation | null>(null)
  const [filters, setFilters] = useState<InboxV2Filters>({ ...EMPTY_INBOX_V2_FILTERS, search: requestedSearch })
  const [collapsed, setCollapsed] = useState(false)
  const roster = useInboxLookup(workspaceId)
  const wsStatus = useRealtimeStatusSafe()
  const live = wsStatus === "connected"

  useInboxV2DeepLink(requestedID, requestedSearch, setSelectedKey, setFilters)

  // B10 (#2364): ONE `state=all` fetch instead of two (`active` + `resolved`
  // used to each hit GET /api/v1/inbox separately, for the same workspace,
  // on every load). `active`/`resolved` below are in-memory slices of that
  // one result — patch/refresh/error/loading all forward to the single
  // underlying query, since useInbox's own PATCH reconciliation already
  // treats stateParam "all" as "keep every transition in this one cached
  // list" (hooks/use-inbox.ts). Approvals and the missions walk are
  // separate server truths this hook does not yet fold in — see the PR
  // description for what B10 shipped here and what is still open.
  const all = useInbox(workspaceId, "all", { loadAll: true })
  const active = useMemo(
    () => ({ ...all, items: all.items.filter((it) => it.state !== "resolved") }),
    [all],
  )
  const resolved = useMemo(
    () => ({ ...all, items: all.items.filter((it) => it.state === "resolved") }),
    [all],
  )
  // GET /api/v1/approvals is now roleManage (#2233) — a MEMBER/MANAGER
  // fetch would 403. Rather than surface that as a permanent error banner
  // in `sourceHealth` below, skip the fetch for a role that could never
  // read it and simply show no approval entries in the merged feed. Those
  // roles never had a Decide button here either (see the `role` prop
  // passed to InboxV2Detail), so this changes what shows up, not what a
  // MEMBER/MANAGER could previously act on.
  const approvals = useApprovals({
    status: "all",
    workspaceId,
    limit: 200,
    pollMs: 15_000,
    enabled: Boolean(workspaceId) && isAdminTier(role),
    loadAll: true,
  })
  const missions = useQuery<Mission[]>({
    queryKey: ["inbox-v2-missions", workspaceId ?? ""],
    queryFn: async ({ signal }) => {
      const rows: Mission[] = []
      const pageSize = 100
      let offset = 0
      do {
        const res = await apiFetch(
          `/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId!)}&limit=${pageSize}&offset=${offset}&include_tasks=true`,
          { signal },
        )
        if (!res.ok) throw new Error(`mission signals: ${res.status}`)
        const page = (await res.json()) as Mission[]
        rows.push(...page)
        if (page.length < pageSize) break
        offset += page.length
      } while (true)
      return rows
    },
    enabled: Boolean(workspaceId),
    retry: false,
    refetchInterval: 30_000,
  })

  const routines = useQuery<InboxRoutineRef[]>({
    queryKey: ["inbox-routine-names", workspaceId ?? ""],
    queryFn: async ({ signal }) => {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId!)}/pipelines`, { signal })
      if (!res.ok) throw new Error("Routine names unavailable")
      const data = await res.json()
      if (!Array.isArray(data)) throw new Error("Invalid routine names response")
      return data
    },
    enabled: Boolean(workspaceId),
    staleTime: 60_000,
    retry: false,
  })
  // Bounded identity lookup: never mistake a mission's crew lead for its assignee.
  // Older issues outside this window keep a crew identity instead of inventing an author.
  const issues = useQuery<Mission[]>({
    queryKey: ["inbox-issue-owners", workspaceId ?? ""],
    queryFn: async ({ signal }) => {
      const res = await apiFetch(`/api/v1/issues?workspace_id=${encodeURIComponent(workspaceId!)}&limit=100&sort=updated_at`, { signal })
      if (!res.ok) throw new Error("Issue owners unavailable")
      const data = await res.json()
      if (!Array.isArray(data)) throw new Error("Invalid issue owners response")
      return data
    },
    enabled: Boolean(workspaceId),
    staleTime: 30_000,
    retry: false,
  })
  useRealtimeEvent("issue.updated", () => { void issues.refetch() })
  useRealtimeEvent("issue.created", () => { void issues.refetch() })
  useRealtimeEvent("mission.updated", () => { void missions.refetch(); void issues.refetch() })

  const lookup = useMemo(() => ({
    ...roster,
    issueById: new Map((issues.data ?? []).map((issue) => [issue.id, issue])),
    missionById: new Map((missions.data ?? []).map((mission) => [mission.id, mission])),
    routineBySlug: new Map((routines.data ?? []).map((routine) => [routine.slug, routine])),
  }), [roster, missions.data, routines.data, issues.data])

  const allInbox = useMemo(() => [...active.items, ...resolved.items], [active.items, resolved.items])
  const suppressedApprovals = useMemo(
    () => suppressedApprovalIDs(allInbox, approvals.rows),
    [allInbox, approvals.rows],
  )

  const feeds = useMemo(() => {
    const inboxActive = active.items.map(inboxEntry)
    const inboxHistory = resolved.items.map(inboxEntry)
    const approvalRows = approvals.rows
      .filter((row) => !suppressedApprovals.has(row.id))
      .map(approvalEntry)
    // No mission-level suppression. The rule used to drop every actionable
    // task entry for a mission whenever ANY inbox row mentioned that mission
    // — and the only rows that carry mission_id are non-actionable notices
    // ("IS-42 ready for review"), so it could never remove a true duplicate,
    // only real work. Task-level dedupe needs a task_id no producer emits yet.
    const missionRows = missionEntries(missions.data ?? [])

    return {
      action: [
        ...inboxActive.filter((entry) => entry.actionable),
        ...approvalRows.filter((entry) => entry.actionable),
        ...missionRows.filter((entry) => entry.actionable),
      ],
      updates: groupAdvisories([
        ...inboxActive.filter((entry) => !entry.actionable),
        ...missionRows.filter((entry) => !entry.actionable),
      ]),
      history: [
        ...inboxHistory,
        ...approvalRows.filter((entry) => entry.historical),
      ],
    }
  }, [active.items, approvals.rows, missions.data, resolved.items, suppressedApprovals])

  const allEntries = useMemo(() => [...feeds.action, ...feeds.updates, ...feeds.history], [feeds])
  const selected = selectEntry(allEntries, selectedKey)
  const view: InboxV2View = selected ? selected.historical ? "history" : selected.actionable ? "action" : "updates" : chosenView ?? (feeds.action.length ? "action" : "updates")
  const visible = useMemo(() => filterInboxEntries(feeds[view], filters, lookup), [feeds, filters, view, lookup])
  useEffect(() => {
    setView(requestedView === "action" || requestedView === "updates" || requestedView === "history" ? requestedView : null)
  }, [requestedView])
  useEffect(() => {
    const type = INBOX_V2_TYPES.find((entry) => entry.key === requestedKind)?.key ?? null
    setFilters((current) => ({ ...current, type }))
  }, [requestedKind])
  useEffect(() => {
    if (selectedKey && detailRef.current) {
      detailRef.current.scrollTop = 0
      detailRef.current.focus({ preventScroll: true })
    }
  }, [selectedKey])
  function showView(next: InboxV2View) {
    setView(next)
    setSelectedKey(null)
    setConfirmation(null)
    router.push(`/inbox?view=${next}`)
  }
  // A deep link can name a row that is gone, belongs to another workspace, or
  // simply has not arrived yet — `active` and `resolved` are two independent
  // walks. Distinguish "still loading" from "not here", and never substitute
  // a different decision for the one that was asked for.
  const feedsSettled = !active.loading && !resolved.loading && !approvals.loading && !missions.isPending
  // Choose the initial view once; live arrivals must not switch a view someone is reading.
  useEffect(() => {
    if (chosenView === null && feedsSettled) setView(feeds.action.length ? "action" : "updates")
  }, [chosenView, feedsSettled, feeds.action.length])
  const selectionMissing = Boolean(selectedKey) && !selected && feedsSettled
  const selectedInboxID = selected?.source === "inbox" ? selected.inboxItem?.id : null
  const detailedInbox = useInboxItem(workspaceId, selectedInboxID)
  // A staged hire is one decision in two places: this waitpoint and a row in
  // the approvals queue that carries `inbox_item_id`. The queue row is the one
  // with a deny, so the twin is found here and its deny handed to the card.
  const hireTwin = useMemo(() => {
    if (!selectedInboxID) return null
    return approvals.rows.find((row) => row.payload?.inbox_item_id === selectedInboxID && row.status === "pending") ?? null
  }, [approvals.rows, selectedInboxID])

  const sourceState = sourceHealth({
    inboxError: active.error || resolved.error,
    approvalsError: approvals.error || (approvals.notConfigured ? "not configured" : null),
    missionsError: missions.error instanceof Error ? missions.error.message : null,
    loading: active.loading || resolved.loading || approvals.loading || missions.isFetching,
  })

  async function refreshAll() {
    await Promise.allSettled([all.refresh(), approvals.refresh(), missions.refetch(), issues.refetch(), routines.refetch()])
  }

  function complete(entry: InboxV2Entry, action: string) {
    setConfirmation({ entry, action, at: new Date().toISOString() })
  }

  async function inboxResolve(entry: InboxV2Entry, action: string) {
    const item = entry.inboxItem!
    await active.patch(item.id, "resolved", action)
    complete(entry, action)
    await refreshAll()
  }

  async function inboxArchive(entry: InboxV2Entry) {
    const item = entry.inboxItem!
    await active.patch(item.id, "resolved", "archived")
    complete(entry, "archived")
    await refreshAll()
  }

  async function markUnread(entry: InboxV2Entry) {
    const item = entry.inboxItem!
    const hook = item.state === "resolved" ? resolved : active
    await hook.patch(item.id, "unread")
    await refreshAll()
  }

  async function sourceRefresh(entry: InboxV2Entry, action?: string) {
    complete(entry, action || "resolved")
    await refreshAll()
  }

  async function approvalDecide(entry: InboxV2Entry, decision: "approved" | "denied", comment: string) {
    const row = entry.approval!
    await decideApproval(row.id, decision, comment, workspaceId)
    approvals.patchRow(row.id, {
      status: decision,
      decision_comment: comment,
      decided_at: new Date().toISOString(),
    })
    complete(entry, decision)
    await refreshAll()
  }

  async function archiveGroup(entry: InboxV2Entry) {
    if (!workspaceId) return
    const ids = entry.groupedItems?.map((item) => item.id) ?? []
    const result = await inboxBulk(workspaceId, ids, "resolved", "archived")
    if (!result.ok) throw new Error(result.error)
    toast.success(`${result.result.updated} grouped updates archived`)
    setSelectedKey(null)
    await refreshAll()
  }

  async function markVisibleRead() {
    if (!workspaceId) return
    const ids = visible.flatMap((entry) => {
      if (entry.source === "group") return entry.groupedItems?.filter((item) => item.state === "unread").map((item) => item.id) ?? []
      return entry.inboxItem?.state === "unread" ? [entry.inboxItem.id] : []
    })
    if (ids.length === 0) return
    const result = await inboxBulk(workspaceId, ids, "read")
    if (!result.ok) return toast.error(result.error)
    toast.success(`${result.result.updated} marked read`)
    await refreshAll()
  }

  async function denyHire() {
    if (!hireTwin) return
    await decideApproval(hireTwin.id, "denied", "Denied from inbox", workspaceId)
    approvals.patchRow(hireTwin.id, { status: "denied", decided_at: new Date().toISOString() })
    await refreshAll()
  }

  /** Offer another decision only after the current one is complete. */
  const next = useMemo(
    () => feeds.action.filter((entry) => entry.key !== confirmation?.entry.key).sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))[0] ?? null,
    [feeds.action, confirmation?.entry.key],
  )

  function openEntry(entry: InboxV2Entry) {
    setSelectedKey(entry.key)
    setConfirmation(null)
    const query = new URLSearchParams(params?.toString())
    query.set("item", entry.inboxItem?.id || entry.approval?.id || entry.key)
    router.push(`/inbox?${query.toString()}`)
    if (entry.source === "inbox" && entry.inboxItem?.state === "unread") {
      void active.patch(entry.inboxItem.id, "read").catch(() => {})
    }
    if (entry.source === "group") {
      const ids = entry.groupedItems?.filter((item) => item.state === "unread").map((item) => item.id) ?? []
      if (workspaceId && ids.length > 0) void inboxBulk(workspaceId, ids, "read").then(() => refreshAll())
    }
  }

  return (
    <div className="relative flex h-[calc(100dvh-3rem)] min-h-0 flex-col overflow-hidden bg-background">
      {/* The page header every other page has (README §2): what this is, the
          counts and whether it is live. The top bar
          used to read only "Crewship" on this route. */}
      <SubBar
        icon={Inbox}
        title="Inbox"
        description={sourceState.loading && allEntries.length === 0 ? "Loading inbox…" : `${feeds.action.length} need you · ${feeds.updates.length} updates · ${feeds.history.length} in history`}
        meta={<StatusPill tone={live ? "success" : "muted"} label={live ? "Live" : "Not live"} className="ml-1 hidden sm:inline-flex" />}
        ariaLabel="Inbox"

      />
        {sourceState.degraded && (
          <div className="flex items-center gap-2 border-b border-destructive/25 bg-destructive/[0.07] px-4 py-2">
            <ShieldX className="h-3.5 w-3.5 shrink-0 text-destructive" />
            <span className="text-xs text-destructive">This inbox may be incomplete — {sourceState.detail}</span>
            <button
              type="button"
              onClick={() => void refreshAll()}
              className="ml-auto text-xs font-medium text-destructive hover:underline"
            >
              Retry
            </button>
          </div>
        )}
    <div className="relative flex min-h-0 flex-1 overflow-hidden">
      {/* Same shell as routines-layout: a collapsible aside, w-9 when shut.
          The 190px view rail is gone — the views are a facet section inside
          this one column, the way Routines carries its status buckets. */}
      <aside
        className={cn(
          "shrink-0 overflow-hidden border-r border-border/60 bg-card",
          // Full width on a phone — a fixed 340px column left a dead strip
          // beside it, because the reading pane is hidden until a row is
          // opened. Desktop keeps the fixed column.
          collapsed ? "w-full lg:w-9" : "w-full lg:w-[280px]",
          selectedKey && "hidden lg:block",
        )}
      >
        {collapsed && (
          <div className="hidden h-full flex-col items-center pt-1.5 lg:flex">
            <SidebarCollapseButton collapsed onToggle={() => setCollapsed(false)} />
          </div>
        )}
        <div className={cn("h-full", collapsed && "lg:hidden")}>
          <InboxV2Explorer
            view={view}
            onView={showView}
            viewCounts={{ action: feeds.action.length, updates: feeds.updates.length, history: feeds.history.length }}
            entries={feeds[view]}
            visible={visible}
            filters={filters}
            onFilters={setFilters}
            selectedKey={selected?.key ?? null}
            onOpen={openEntry}
            onMarkAllRead={view === "updates" && active.unreadCount > 0 ? markVisibleRead : undefined}
            onToggleCollapse={() => setCollapsed(true)}
            lookup={lookup}
          />
        </div>
      </aside>

      <main ref={detailRef} tabIndex={-1} aria-label="Inbox detail" className={cn("min-w-0 flex-1 overflow-y-auto", !selectedKey && "hidden lg:block")}>
        {selectedKey && !confirmation && (
          <div className="sticky top-0 z-10 border-b border-border/60 bg-background px-3 py-2">
            <Button variant="ghost" size="sm" onClick={() => showView(view)}>← Back to inbox</Button>
          </div>
        )}
        <InboxV2Detail
          key={selected?.key || "overview"}
          entry={selected}
          selectionMissing={selectionMissing}
          role={(role as WorkspaceRole | null) ?? null}
          detailedInboxItem={detailedInbox.data}
          detailLoading={detailedInbox.isFetching}
          confirmation={confirmation}
          nextReview={confirmation && confirmation.entry.actionable && next && !sourceState.degraded && !sourceState.loading ? {
            count: feeds.action.filter((entry) => entry.key !== confirmation.entry.key).length,
            onOpen: () => { setView("action"); openEntry(next) },
          } : undefined}
          onClearConfirmation={() => showView(view)}
          onViewReceipt={(entry) => { setConfirmation(null); setView("history"); setSelectedKey(entry.key) }}
          onInboxResolve={async (item, action) => inboxResolve(inboxEntry(item), action)}
          onInboxArchive={async (item) => inboxArchive(inboxEntry(item))}
          onInboxMarkUnread={async (item) => markUnread(inboxEntry(item))}
          onInboxRefresh={async (item, action) => sourceRefresh(inboxEntry(item), action)}
          // The §12 act door (#2398). The hook flips the loaded "all" list to
          // resolved in place and the card renders its receipt, so the pane
          // reflects the action without a page-level resolve/confirm flow.
          onInboxAct={async (item, action, input) => all.actOnInboxItem(item.id, action, input)}
          onApprovalDecide={approvalDecide}
          onArchiveGroup={archiveGroup}
          lookup={lookup}
          onDenyHire={hireTwin ? denyHire : undefined}
          triage={{
            incomplete: sourceState.degraded,
            loading: sourceState.loading && allEntries.length === 0,
            action: feeds.action,
            updates: feeds.updates,
            history: feeds.history,
            onOpen: (entry) => {
              const holds = feeds.history.includes(entry) ? "history" : feeds.updates.includes(entry) ? "updates" : "action"
              setView(holds)
              openEntry(entry)
            },
            onCrew: (crewId) => {
              const target = feeds.action.some((entry) => entryIdentity(entry, lookup).crew?.id === crewId) ? "action" : "updates"
              showView(target)
              setFilters({ ...EMPTY_INBOX_V2_FILTERS, crew: crewId })
            },
          }}
        />
      </main>
    </div>
    </div>
  )
}

function sourceHealth(input: {
  inboxError: string | null
  approvalsError: string | null
  missionsError: string | null
  loading: boolean
}) {
  const failures = [
    input.inboxError && `Inbox events: ${input.inboxError}`,
    input.approvalsError && `Approval gates: ${input.approvalsError}`,
    input.missionsError && `Mission signals: ${input.missionsError}`,
  ].filter(Boolean) as string[]
  return {
    degraded: failures.length > 0,
    loading: input.loading,
    label: failures.length > 0 ? "Inbox may be incomplete" : input.loading ? "Checking all sources…" : "All sources connected",
    detail: failures.join(" · "),
  }
}
