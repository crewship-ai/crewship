"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useQuery } from "@tanstack/react-query"
import { Activity, Archive, ArrowLeft, Check, ChevronDown, ChevronUp, Inbox, ShieldX, X } from "lucide-react"
import { toast } from "@/lib/toast"

import type { WorkspaceRole } from "@/components/features/inbox/inbox-derive"
import { inboxArchivable, InboxMessageMenu } from "@/components/features/inbox/inbox-detail"
import { PageBanner } from "@/components/layout/app-banners"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { PageEmpty, PageError, PageLoading } from "@/components/layout/page-state"
import { SecondaryPanel } from "@/components/layout/secondary-panel"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUserPreference } from "@/hooks/use-user-preference"
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
  approvalEntry, EMPTY_INBOX_V2_FILTERS, entryKindPill, INBOX_SORTS, INBOX_V2_TYPES,
  groupAdvisories, inboxEntry, missionEntries, selectEntry, suppressedApprovalIDs,
  type InboxSort, type InboxV2Filters,
} from "./inbox-v2-derive"
import { requestedAgentOf, useInboxV2DeepLink } from "./inbox-v2-deeplink"
import { ATTENTION_LABELS, matchesInboxAttention, parseInboxAttention } from "./inbox-v2-attention"
import { entryIdentity, filterInboxEntries } from "./inbox-entry-identity"
import { DetailRail, RailSlotProvider, useWideDetail } from "./inbox-detail-frame"
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
  const attention = parseInboxAttention(params?.get("attention") ?? null)
  const requestedID = params?.get("item") ?? null
  // ?filter= (Z30) is search text; ?agent_id= / ?agent= name ONE agent and
  // become the Agent facet (INB-01) — see useInboxV2DeepLink.
  const requestedSearch = params?.get("filter") ?? ""
  const requestedAgent = requestedAgentOf(params)
  const [chosenView, setView] = useState<InboxV2View | null>(null)
  // `request:<id>` rather than `inbox:<id>`: the caller of ?item= knows an id,
  // not which source owns it, and an approval-queue deep link keyed
  // `approval:<id>` could never match. selectEntry resolves it against both.
  // useInboxV2DeepLink below keeps both this and `filters.search` in step with
  // the URL after mount.
  const [selectedKey, setSelectedKey] = useState<string | null>(requestedID ? `request:${requestedID}` : null)
  const [confirmation, setConfirmation] = useState<InboxV2Confirmation | null>(null)
  const [filters, setFilters] = useState<InboxV2Filters>({ ...EMPTY_INBOX_V2_FILTERS, search: requestedSearch, agent: requestedAgent })
  const isMobile = useIsMobile()
  const wide = useWideDetail()
  const [railSlot, setRailSlot] = useState<HTMLElement | null>(null)
  // View › Sort, remembered per user (PNL-04).
  const [storedSort, setSort] = useUserPreference<InboxSort>("inbox.sort", "urgency")
  const sort: InboxSort = INBOX_SORTS.some((s) => s.key === storedSort) ? storedSort : "urgency"
  // Phone only: the Overview opened from its row, as a page of its own.
  const [phoneOverview, setPhoneOverview] = useState(false)
  // Select mode (v67 panel): several rows, one bulk action.
  const [picking, setPicking] = useState(false)
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set())
  const roster = useInboxLookup(workspaceId)
  const wsStatus = useRealtimeStatusSafe()
  const live = wsStatus === "connected"

  useInboxV2DeepLink(requestedID, requestedSearch, setSelectedKey, setFilters, requestedAgent)

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
  const currentEntries = useMemo(
    () => attention ? [...feeds.action, ...feeds.updates].filter((entry) => matchesInboxAttention(entry, attention)) : feeds[view],
    [attention, feeds, view],
  )
  const visible = useMemo(() => filterInboxEntries(currentEntries, filters, lookup, sort), [currentEntries, filters, lookup, sort])
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
    setPhoneOverview(false)
    router.push(`/inbox?view=${next}`)
  }
  function showOverview() {
    setSelectedKey(null)
    setConfirmation(null)
    if (isMobile) setPhoneOverview(true)
    const query = new URLSearchParams(params?.toString())
    query.delete("item")
    router.push(query.size ? `/inbox?${query.toString()}` : "/inbox")
  }
  /**
   * A facet change narrows the list, so an open row may no longer be in it:
   * close it. Clearing the agent also takes it out of the URL — otherwise the
   * link that set it would bring the filter back on the next reload.
   */
  function changeFilters(next: InboxV2Filters) {
    setFilters(next)
    const dropAgent = next.agent !== filters.agent && !next.agent && Boolean(params?.get("agent_id") || params?.get("agent"))
    if (!selectedKey && !dropAgent) return
    if (selectedKey) {
      setSelectedKey(null)
      setConfirmation(null)
    }
    const query = new URLSearchParams(params?.toString())
    query.delete("item")
    if (dropAgent) {
      query.delete("agent_id")
      query.delete("agent")
    }
    router.push(query.size ? `/inbox?${query.toString()}` : "/inbox")
  }
  function closeEntry() {
    setSelectedKey(null)
    setConfirmation(null)
    const query = new URLSearchParams(params?.toString())
    query.delete("item")
    router.push(query.size ? `/inbox?${query.toString()}` : "/inbox")
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
  // No automatic preview (Harbor 2, ODCH-I05): with nothing chosen the pane
  // shows the Overview. A previewed first row put a live Approve button in
  // front of a reader who never picked that item. Explicit ?item= links
  // never fall back to a different row.
  const displayed = selectedKey ? selected : null
  const selectedInboxID = displayed?.source === "inbox" ? displayed.inboxItem?.id : null
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
    setPhoneOverview(false)
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

  // Where the reader is in the list they are working through ("4 / 16").
  const position = displayed ? visible.findIndex((entry) => entry.key === displayed.key) : -1
  const menuItem = displayed?.source === "inbox" ? detailedInbox.data ?? displayed.inboxItem ?? null : null
  const canArchive = displayed
    ? displayed.source === "group" || (menuItem != null && inboxArchivable(menuItem))
    : false
  const unreadVisibleIDs = useMemo(() => visible.flatMap((entry) => {
    if (entry.source === "group") return entry.groupedItems?.filter((item) => item.state === "unread").map((item) => item.id) ?? []
    return entry.inboxItem?.state === "unread" ? [entry.inboxItem.id] : []
  }), [visible])

  /** J / K: the next or previous row of the list, wrapping like the panel does. */
  function step(delta: 1 | -1) {
    if (visible.length === 0) return
    const from = position >= 0 ? position : delta > 0 ? -1 : visible.length
    const target = visible[(from + delta + visible.length) % visible.length]
    if (target && target.key !== displayed?.key) openEntry(target)
  }

  /** E: archive what is open — never an undecided decision (the server 409s). */
  async function archiveCurrent() {
    if (!displayed) return
    if (!canArchive) {
      toast.info("Decide first — an open decision leaves the inbox when it is decided")
      return
    }
    try {
      if (displayed.source === "group") await archiveGroup(displayed)
      else if (menuItem) await inboxArchive(inboxEntry(menuItem))
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Archive failed")
    }
  }

  // Keys (v67): J/K next/previous, E archive, R reply, 1–3 spaces. Bound once;
  // the handlers read the latest render through a ref.
  const keys = useRef({ step, archiveCurrent, showView })
  keys.current = { step, archiveCurrent, showView }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return
      const el = e.target as HTMLElement | null
      if (el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName))) return
      if (document.querySelector('[role="dialog"], [role="alertdialog"], [role="menu"]')) return
      const k = e.key.toLowerCase()
      if (k === "j" || k === "k") {
        e.preventDefault()
        keys.current.step(k === "j" ? 1 : -1)
      } else if (k === "e") {
        e.preventDefault()
        void keys.current.archiveCurrent()
      } else if (k === "r") {
        const pane = detailRef.current
        const box = pane?.querySelector("textarea")
        const answer = pane?.querySelector<HTMLButtonElement>('[data-testid="needs-human-actions"] button[aria-pressed="false"]')
        if (box) { e.preventDefault(); box.focus() }
        else if (answer) { e.preventDefault(); answer.click() }
      } else if (k === "1" || k === "2" || k === "3") {
        e.preventDefault()
        keys.current.showView((["action", "updates", "history"] as const)[Number(k) - 1])
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  // "Earlier from {sender}" for the wide-desktop rail: the sender's last three.
  const earlier = useMemo(() => {
    if (!displayed) return []
    const sender = entryIdentity(displayed, lookup).name
    return allEntries
      .filter((entry) => entry.key !== displayed.key && entryIdentity(entry, lookup).name === sender)
      .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
      .slice(0, 3)
  }, [allEntries, displayed, lookup])

  const pickedEntries = allEntries.filter((entry) => picked.has(entry.key))
  const pickedUnread = pickedEntries.flatMap((entry) => entry.source === "group"
    ? entry.groupedItems?.filter((item) => item.state === "unread").map((item) => item.id) ?? []
    : entry.inboxItem?.state === "unread" ? [entry.inboxItem.id] : [])
  // Archive takes only what may leave the inbox; an open decision stays
  // until it is decided (the PATCH would 409 on it).
  const pickedArchivable = pickedEntries.flatMap((entry) => entry.source === "group"
    ? entry.groupedItems?.map((item) => item.id) ?? []
    : entry.inboxItem && inboxArchivable(entry.inboxItem) ? [entry.inboxItem.id] : [])

  function togglePicking() {
    setPicking((on) => !on)
    setPicked(new Set())
  }

  async function bulkPicked(kind: "read" | "archive") {
    if (!workspaceId) return
    const ids = kind === "read" ? pickedUnread : pickedArchivable
    if (ids.length === 0) return
    const result = kind === "read" ? await inboxBulk(workspaceId, ids, "read") : await inboxBulk(workspaceId, ids, "resolved", "archived")
    if (!result.ok) return toast.error(result.error)
    const skipped = kind === "archive" ? pickedEntries.length - pickedEntries.filter((entry) => entry.source === "group" || (entry.inboxItem && inboxArchivable(entry.inboxItem))).length : 0
    toast.success(`${result.result.updated} ${kind === "read" ? "marked read" : "archived"}${skipped ? ` · ${skipped} open ${skipped === 1 ? "decision stays" : "decisions stay"} until decided` : ""}`)
    setPicked(new Set())
    await refreshAll()
  }

  const bulkBar = picking ? (
    <div className="flex items-center gap-1.5" data-testid="inbox-bulk">
      <span className="min-w-0 flex-1 truncate text-label text-muted-foreground">{picked.size ? `${picked.size} selected` : "Pick items"}</span>
      <Button size="sm" variant="outline" className="gap-1" disabled={pickedUnread.length === 0} onClick={() => void bulkPicked("read")}>
        <Check className="h-3.5 w-3.5" /> Mark read
      </Button>
      <Button size="sm" variant="outline" className="gap-1" disabled={pickedArchivable.length === 0} onClick={() => void bulkPicked("archive")}>
        <Archive className="h-3.5 w-3.5" /> Archive
      </Button>
      <Button size="icon" variant="ghost" className="h-8 w-8" aria-label="Done selecting" onClick={togglePicking}>
        <X className="h-4 w-4" />
      </Button>
    </div>
  ) : undefined

  const phoneDetail = isMobile && (Boolean(selectedKey) || phoneOverview)
  const firstLoad = !feedsSettled && allEntries.length === 0
  const inboxFailed = feedsSettled && allEntries.length === 0 && Boolean(active.error)
  const empty = feedsSettled && !sourceState.degraded && allEntries.length === 0
  const section = displayed
    ? entryKindPill(displayed).label
    : attention
      ? ATTENTION_LABELS[attention]
      : isMobile && !phoneOverview ? undefined : "Overview"

  const back = (
    <Button
      variant="ghost"
      size="icon"
      className="h-8 w-8"
      aria-label="Back to inbox"
      onClick={() => { if (selectedKey) closeEntry(); setPhoneOverview(false) }}
    >
      <ArrowLeft className="h-4 w-4" />
    </Button>
  )

  const topActions = displayed && !confirmation ? (
    <span className="flex items-center gap-0.5" data-testid="inbox-item-nav">
      {position >= 0 && (
        <span className="mr-1 hidden whitespace-nowrap text-label tabular-nums text-muted-foreground sm:inline" aria-label={`Item ${position + 1} of ${visible.length}`}>
          {position + 1} / {visible.length}
        </span>
      )}
      <Button variant="ghost" size="icon" className="h-8 w-8" aria-label="Previous (K)" title="Previous · K" disabled={visible.length === 0} onClick={() => step(-1)}>
        <ChevronUp className="h-4 w-4" />
      </Button>
      <Button variant="ghost" size="icon" className="h-8 w-8" aria-label="Next (J)" title="Next · J" disabled={visible.length === 0} onClick={() => step(1)}>
        <ChevronDown className="h-4 w-4" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        className="h-8 w-8"
        aria-label="Archive (E)"
        title={canArchive ? "Archive · E" : "Decide first — an open decision cannot be archived"}
        disabled={!canArchive}
        onClick={() => void archiveCurrent()}
      >
        <Archive className="h-4 w-4" />
      </Button>
      {menuItem && (
        <InboxMessageMenu
          item={menuItem}
          onArchive={() => inboxArchive(inboxEntry(menuItem))}
          onMarkUnread={() => { void markUnread(inboxEntry(menuItem)) }}
        />
      )}
    </span>
  ) : !displayed && !confirmation && unreadVisibleIDs.length > 0 ? (
    <Button size="sm" variant="outline" className="gap-1.5" onClick={() => void markVisibleRead()}>
      <Check className="h-3.5 w-3.5" /> Mark {unreadVisibleIDs.length} read
    </Button>
  ) : null

  return (
    <div className="relative flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] min-h-0 flex-col overflow-hidden bg-background">
      {/* The one page header (SHL-05): Inbox › {kind}, live state, and the
          item's navigation. Counts live in the Overview and the spaces. */}
      <SubBar
        icon={Inbox}
        title="Inbox"
        section={section}
        meta={live ? <StatusPill tone="success" live label="Live" className="ml-1 hidden sm:inline-flex" /> : undefined}
        leading={phoneDetail ? back : undefined}
        actions={topActions}
        ariaLabel="Inbox"
      />
      {sourceState.degraded && !inboxFailed && (
        // The page's one banner (KIT-04, bad tone): some source did not answer.
        <PageBanner
          tone="bad"
          icon={ShieldX}
          className="m-2 mb-0"
          action={<Button size="sm" variant="outline" onClick={() => void refreshAll()}>Retry</Button>}
        >
          This inbox may be incomplete — {sourceState.detail}
        </PageBanner>
      )}
      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        {/* The one SecondaryPanel (Harbor 2, #3350): 272 px, collapse remembered
            per user. On a phone the list IS the page while nothing is open
            (phoneInline), and an opened row or the Overview shows alone with
            Back in the bar (PNL-05). Tablet keeps the panel beside the detail. */}
        {!phoneDetail && <SecondaryPanel id="inbox" label="Inbox views" phoneInline footer={bulkBar}>
          <InboxV2Explorer
            view={view}
            onView={showView}
            viewCounts={{ action: feeds.action.length, updates: feeds.updates.length, history: feeds.history.length }}
            entries={currentEntries}
            visible={visible}
            attention={attention}
            onClearAttention={() => router.push("/inbox")}
            filters={filters}
            onFilters={changeFilters}
            selectedKey={displayed?.key ?? null}
            onOpen={openEntry}
            overviewSelected={!displayed && !confirmation && !isMobile}
            onOverview={showOverview}
            sort={sort}
            onSort={setSort}
            lookup={lookup}
            loading={firstLoad}
            picking={picking}
            picked={picked}
            onTogglePicking={togglePicking}
            onPick={(entry) => setPicked((current) => {
              const next = new Set(current)
              if (next.has(entry.key)) next.delete(entry.key)
              else next.add(entry.key)
              return next
            })}
          />
        </SecondaryPanel>}

        {!(isMobile && !phoneDetail) && <main ref={detailRef} tabIndex={-1} aria-label="Inbox detail" className="min-w-0 flex-1 overflow-y-auto">
          {inboxFailed ? (
            <PageError
              title="The inbox could not load"
              description="Nothing was changed. Decisions stay open until you can see them again."
              onRetry={() => void refreshAll()}
              detail={sourceState.detail}
            />
          ) : firstLoad && !selectedKey ? (
            <PageLoading variant="cards" />
          ) : empty && !selectedKey && !confirmation ? (
            <PageEmpty
              icon={Inbox}
              title="Nothing waits for you"
              description="Decisions, questions and reviews from agents land here."
              primary={{ label: "Open Activity", href: "/activity", icon: Activity }}
              cli="crewship inbox"
            />
          ) : (
            <div className={cn(displayed && !confirmation && "2xl:grid 2xl:grid-cols-[minmax(0,860px)_minmax(260px,300px)] 2xl:items-start 2xl:justify-center 2xl:gap-6 2xl:px-6")}>
            {/* Wide desktop: context chips and run steps render into the rail (INB-02). */}
            <RailSlotProvider value={wide && displayed && !confirmation ? railSlot : null}>
            <InboxV2Detail
              key={displayed?.key || "overview"}
              entry={displayed}
              overview={{
                action: feeds.action,
                updates: feeds.updates,
                history: feeds.history,
                lookup,
                onOpen: openEntry,
                onAttention: (next) => { setFilters((current) => ({ ...current, attention: next })); showView("action") },
                onCrew: (crewId) => setFilters((current) => ({ ...current, crew: crewId })),
                incomplete: sourceState.degraded,
                focus: attention ? { label: ATTENTION_LABELS[attention], entries: visible, onClear: () => router.push("/inbox") } : undefined,
              }}
              selectionMissing={selectionMissing}
              onOpenHistory={() => showView("history")}
              onBackToHandle={() => showView("action")}
              role={(role as WorkspaceRole | null) ?? null}
              detailedInboxItem={detailedInbox.data}
              detailLoading={detailedInbox.isFetching}
              confirmation={confirmation}
              nextReview={confirmation && confirmation.entry.actionable && next && !sourceState.degraded && !sourceState.loading ? {
                count: feeds.action.filter((entry) => entry.key !== confirmation.entry.key).length,
                onOpen: () => { setView("action"); openEntry(next) },
              } : undefined}
              onClearConfirmation={closeEntry}
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
            />
            </RailSlotProvider>
            {displayed && !confirmation && <DetailRail entry={displayed} lookup={lookup} earlier={earlier} onOpen={openEntry} slotRef={setRailSlot} />}
            </div>
          )}
        </main>}
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
