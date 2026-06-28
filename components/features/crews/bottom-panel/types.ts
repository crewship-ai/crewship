// Types shared between the BottomPanel router and its tab components.
// Kept in a .ts (no JSX) so they can be imported anywhere — including
// hooks and helpers — without dragging React in.

// Crew/agent tabs (the original set) + the context-aware tabs added for
// the Issues / Routines / Activity docks. A page passes only the subset
// that makes sense for its entity via BottomPanelProps.tabs.
export type BottomTab =
  // crew / agent
  | "messages" | "exec" | "yaml" | "docker" | "files" | "terminal"
  // issue / mission
  | "activity" | "runs" | "changes" | "comments"
  // routine
  | "schedule"
  // run / activity
  | "logs" | "trace"

export interface ContainerStatus {
  name: string
  image: string
  status: string
  cpu_percent?: number | null
  memory_mb?: number | null
  agent_count?: number | null
}

// Real API shape from internal/api/agent_inbox.go (verified 2026-04-28):
// peer_messages: { id, from_agent_name, from_agent_slug, to_agent_name?,
//                  question, status, created_at, direction }
// escalations are NOT in the response — only escalations_open (count).
export interface PeerMessage {
  id: string
  from_agent_name: string
  from_agent_slug: string
  to_agent_name?: string | null
  to_agent_slug?: string | null
  question: string
  response?: string | null
  status: string
  created_at: string
  direction: "incoming" | "outgoing"
  escalated?: boolean
  duration_ms?: number | null
}

export interface FileEntry {
  name: string
  /** Full storage-rooted path returned by the list endpoint —
   *  `<crewID>/<slug>/<rest>`. Use this verbatim when issuing
   *  download / save / subdir queries; the IPC layer expects the
   *  full path (prefix-checks against crewID). */
  path?: string
  size?: number
  is_dir?: boolean
  modified?: string
  mod_time?: string
}

export interface LogEntry {
  // The actual shape is sidecar-defined; we render whatever string fields
  // we recognise. Most rows will have at minimum a timestamp + message.
  ts?: string
  timestamp?: string
  level?: string
  message?: string
  msg?: string
  text?: string
  [k: string]: unknown
}

export type BottomPanelContext =
  | { kind: "agent"; agentId: string; agentSlug: string; agentName: string; crewId: string | null; crewSlug: string | null }
  | { kind: "crew"; crewId: string; crewSlug: string }
  // An issue/mission in focus on the Issues page. crewId/crewSlug are the
  // owning crew (needed because issue routes are nested under the crew).
  | { kind: "mission"; missionId: string; identifier: string; title?: string; crewId: string; crewSlug: string }
  // A routine (pipeline + schedule) in focus on the Routines page.
  | { kind: "routine"; slug: string; pipelineId?: string | null; scheduleId?: string | null; name?: string }
  // A single run in focus on the Activity page.
  | { kind: "run"; runId: string; pipelineId?: string | null; pipelineSlug?: string | null }
  | null

export interface BottomPanelProps {
  workspaceId: string
  /** Currently selected entity context. Null when no selection — panel
   *  shows workspace-wide data. */
  context: BottomPanelContext
  /** Per-page tab set. When omitted the panel falls back to the original
   *  crew/agent tab list, so the Crews page keeps its exact behaviour.
   *  Issues / Routines / Activity pass their own context-relevant subset. */
  tabs?: BottomTab[]
  /** Optional initial tab + open state — lets parent (e.g. crew Files
   *  button) jump directly to a tab and expand. */
  initialTab?: BottomTab
  initialOpen?: boolean
  /** Notified when panel open state changes so parent can persist if desired. */
  onOpenChange?: (open: boolean) => void
}
