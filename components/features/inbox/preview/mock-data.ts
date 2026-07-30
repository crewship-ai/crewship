import type { InboxItem } from "@/hooks/use-inbox"

// Fixture set for the /inbox/preview design surface. Every row is shaped like
// a REAL producer's output — the payload keys are copied from the Go call
// sites (internal/pipeline/waitpoints.go, schedules.go, api/keeper_request.go,
// api/skills_author_handler.go, consolidate/proposed.go, chatnotify/notify.go,
// orchestrator/mission_tasks_completion.go), so the preview can only render
// what production could actually deliver. If a field isn't here, the real
// inbox doesn't have it either.

/**
 * InboxItem["kind"] is a four-member union, while inbox.AllKinds writes seven —
 * memory_consolidation, schedule_missed and schedule_circuit_breaker_tripped
 * have no place in the frontend type at all. Widening the real type would force
 * KIND_META to grow the three cards it is missing, which is the phase-3 fix and
 * a change to the live inbox; the preview keeps its own widened alias so it can
 * show those rows without touching production behaviour.
 */
export type PreviewInboxItem = Omit<InboxItem, "kind"> & { kind: string }

export type WorkspaceRole = "OWNER" | "ADMIN" | "MANAGER" | "MEMBER" | "VIEWER"

/** Mirrors internal/api roleRank — used by inboxVisibilityClause. */
export const ROLE_RANK: Record<WorkspaceRole, number> = {
  VIEWER: 1,
  MEMBER: 2,
  MANAGER: 3,
  ADMIN: 4,
  OWNER: 5,
}

/**
 * Mirrors internal/api/helpers.go canRole. "create" is MANAGER and up,
 * "manage" is OWNER/ADMIN only — the gap that makes a MANAGER-targeted skill
 * proposal undecidable by the very role it was addressed to.
 */
export function canRole(role: WorkspaceRole, action: "create" | "manage" | "read"): boolean {
  if (action === "read") return true
  if (action === "manage") return role === "OWNER" || role === "ADMIN"
  return role === "OWNER" || role === "ADMIN" || role === "MANAGER"
}

/** Mirrors inboxVisibilityClause: untargeted, personal, or role at/below rank. */
export function isVisibleTo(item: PreviewInboxItem, role: WorkspaceRole, userId: string): boolean {
  if (!item.target_user_id && !item.target_role) return true
  if (item.target_user_id && item.target_user_id === userId) return true
  if (!item.target_role) return false
  return ROLE_RANK[role] >= ROLE_RANK[item.target_role as WorkspaceRole]
}

/** Mirrors internal/notify/categories.go categoryByKind. */
export const CATEGORY_BY_KIND: Record<string, string> = {
  waitpoint: "agents.approval",
  escalation: "agents.escalation",
  failed_run: "routines.failed",
  message: "chat.replies",
  memory_consolidation: "memory",
  schedule_missed: "routines.missed",
  schedule_circuit_breaker_tripped: "routines.missed",
}

export const PREVIEW_USER_ID = "usr_preview"

/**
 * The fixtures are pinned to a fixed instant so "vyprší za 11 min" and
 * "před 4 min" stay stable — a preview whose relative times drift with the
 * wall clock stops being a design reference the day after it is written.
 */
export const PREVIEW_NOW = Date.parse("2026-07-30T14:31:00.000Z")

const now = PREVIEW_NOW
const at = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString()

function base(overrides: Partial<PreviewInboxItem> & Pick<PreviewInboxItem, "id" | "kind" | "title">): PreviewInboxItem {
  return {
    workspace_id: "ws_preview",
    source_id: `src_${overrides.id}`,
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: at(30),
    updated_at: at(30),
    target_role: "MANAGER",
    ...overrides,
  }
}

export const PREVIEW_ITEMS: PreviewInboxItem[] = [
  base({
    id: "ibx_wp_promote",
    kind: "waitpoint",
    title: "Schválit krok „promote“ v docs-publish",
    body_md:
      "Dokumentace je sestavená a proskenovaná.\n\nPublikuji do `crewship-ai/docs`?",
    sender_type: "pipeline",
    sender_name: "docs-publish",
    priority: "high",
    blocking: true,
    created_at: at(19),
    updated_at: at(19),
    payload: {
      pipeline_run_id: "run_cmqtg8x1k93",
      step_id: "promote",
      invoking_crew_id: "crew_quality",
      // 11 minutes out from the fixture's "now" — the countdown the real UI
      // never renders even though every waitpoint carries it.
      timeout_at: new Date(now + 11 * 60_000).toISOString(),
    },
  }),
  base({
    id: "ibx_esc_ghtoken",
    kind: "escalation",
    title: "Keeper: casey žádá GH_TOKEN (riziko 62)",
    body_md:
      "Agent narazil na chybějící přihlašovací údaj při kroku `git push`. Push selhal na 403.",
    sender_type: "system",
    sender_id: "keeper",
    sender_name: "Keeper",
    priority: "high",
    blocking: true,
    created_at: at(4),
    updated_at: at(4),
    target_user_id: PREVIEW_USER_ID,
    payload: {
      request_id: "kr_8812",
      request_type: "access",
      agent_id: "agt_casey",
      agent_name: "casey",
      credential_id: "cred_gh",
      credential_name: "GH_TOKEN",
      security_level: "HIGH",
      intent: "Publikovat vygenerovanou dokumentaci do crewship-ai/docs",
      risk_score: 62,
    },
  }),
  base({
    id: "ibx_skill_logparser",
    kind: "escalation",
    title: "Dovednost log-parser navržena k revizi",
    body_md: "Agent authored a new skill. Approve it to add it to the crew, or reject it.",
    sender_type: "agent",
    sender_id: "agt_casey",
    sender_name: "casey",
    avatar_seed: "casey",
    state: "read",
    priority: "high",
    blocking: true,
    created_at: at(1180),
    updated_at: at(1180),
    payload: {
      kind: "skill_proposal",
      crew_id: "crew_quality",
      file_name: "skill-log-parser.md",
      slug: "log-parser",
      scan_status: "clean",
    },
  }),
  base({
    id: "ibx_routine_sweep",
    kind: "escalation",
    title: "Rutina nightly-sweep čeká na schválení",
    body_md:
      "A routine was authored that needs approval before it can run. Approve it to activate the routine, or reject it.",
    sender_type: "pipeline",
    sender_name: "nightly-sweep",
    state: "read",
    priority: "high",
    blocking: true,
    created_at: at(17280),
    updated_at: at(17280),
    payload: {
      kind: "routine_proposal",
      slug: "nightly-sweep",
      pipeline_id: "pl_9931",
      author_crew_id: "crew_ops",
      risk_reasons: ["spouští shell", "má přístup k síti"],
    },
  }),
  base({
    id: "ibx_chat_atlas",
    kind: "message",
    title: "Atlas odpověděl v „migrace v167“",
    body_md: "Migrace prošla na čisté DB i na kopii dev2. Můžu to poslat do mainu?",
    sender_type: "agent",
    sender_id: "agt_atlas",
    sender_name: "atlas",
    avatar_seed: "atlas",
    created_at: at(62),
    updated_at: at(62),
    target_role: "",
    target_user_id: PREVIEW_USER_ID,
    payload: {
      chat_id: "cht_5521",
      agent_id: "agt_atlas",
      agent_slug: "atlas",
      chat_title: "migrace v167",
      chat_url: "/chat/atlas",
    },
  }),
  base({
    id: "ibx_issue_eng6",
    kind: "message",
    title: "ENG-6 připraveno k revizi",
    body_md: "Mission engine posunul ENG-6 do stavu REVIEW.",
    sender_type: "system",
    sender_name: "Mission engine",
    state: "read",
    created_at: at(2600),
    updated_at: at(2600),
    payload: {
      mission_id: "msn_331",
      issue_identifier: "ENG-6",
      new_status: "REVIEW",
    },
  }),
  base({
    id: "ibx_breaker_docs",
    kind: "schedule_circuit_breaker_tripped",
    title: "Routine docs-publish paused after 5 straight failures",
    body_md:
      "Schedule **docs-publish** failed 5 times in a row and has been auto-disabled to stop the spam / cost bleed.",
    sender_type: "pipeline",
    sender_name: "docs-publish",
    priority: "high",
    created_at: at(140),
    updated_at: at(140),
    payload: { schedule_id: "sch_cmqt8f2", consecutive_failures: 5 },
  }),
  base({
    id: "ibx_missed_sweep",
    kind: "schedule_missed",
    title: "Schedule missed 3 occurrence(s): nightly-sweep",
    body_md: "Schedule **nightly-sweep** was overdue and skipped 3 occurrence(s).",
    sender_type: "pipeline",
    sender_name: "nightly-sweep",
    state: "read",
    created_at: at(300),
    updated_at: at(300),
    payload: { schedule_id: "sch_991a", missed_count: 3, catchup_policy: "skip" },
  }),
  base({
    id: "ibx_memory_consol",
    kind: "memory_consolidation",
    title: "Memory consolidation: 12 rules pending review",
    body_md: "12 pravidel vzniklo z 340 prohledaných záznamů deníku.",
    sender_type: "system",
    sender_name: "consolidator",
    state: "read",
    created_at: at(380),
    updated_at: at(380),
    payload: {
      proposal_id: "prop_7712",
      proposal_path: ".proposed/consolidation-2026-07-30.md",
      rules_count: 12,
      entries_scanned: 340,
    },
  }),
  base({
    id: "ibx_routine_update",
    kind: "message",
    title: "nightly-deploy · krok build dokončen",
    body_md: "Krok `build` skončil úspěšně za 3 m 12 s.",
    sender_type: "pipeline",
    sender_name: "nightly-deploy",
    state: "read",
    created_at: at(120),
    updated_at: at(120),
    payload: {
      subkind: "routine_update",
      pipeline_run_id: "run_44a1",
      step_id: "build",
      crew_slug: "ops",
    },
  }),
]

export const PREVIEW_ARCHIVE: PreviewInboxItem[] = [
  base({
    id: "ibx_arch_1",
    kind: "escalation",
    title: "casey žádá GH_TOKEN pro crewship-ai/docs",
    sender_type: "agent",
    sender_name: "casey",
    avatar_seed: "casey",
    state: "resolved",
    created_at: "2026-07-14T09:04:00.000Z",
    updated_at: "2026-07-14T09:12:00.000Z",
    resolved_at: "2026-07-14T09:12:00.000Z",
    resolved_action: "approved",
    resolved_by_user_id: "pavel",
    payload: { escalation_type: "CREDENTIAL", credential_name: "GH_TOKEN" },
  }),
  base({
    id: "ibx_arch_2",
    kind: "escalation",
    title: "casey žádá GH_TOKEN pro crewship-ai/web",
    sender_type: "agent",
    sender_name: "casey",
    avatar_seed: "casey",
    state: "resolved",
    created_at: "2026-07-02T13:40:00.000Z",
    updated_at: "2026-07-02T16:40:00.000Z",
    resolved_at: "2026-07-02T16:40:00.000Z",
    resolved_action: "rejected",
    resolved_by_user_id: "pavel",
    payload: { escalation_type: "CREDENTIAL", credential_name: "GH_TOKEN" },
  }),
  base({
    id: "ibx_arch_3",
    kind: "escalation",
    title: "Keeper: GH_TOKEN vysoké riziko — DENY",
    sender_type: "system",
    sender_name: "Keeper",
    state: "resolved",
    created_at: "2026-06-28T10:02:00.000Z",
    updated_at: "2026-06-28T11:02:00.000Z",
    resolved_at: "2026-06-28T11:02:00.000Z",
    resolved_action: "archived",
    resolved_by_user_id: "jana",
    payload: { request_type: "access", credential_name: "GH_TOKEN" },
  }),
]
