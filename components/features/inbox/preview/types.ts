// Shared vocabulary for the inbox preview surface.

/** Which list the page is showing. Chosen in the rail, not in a tab strip. */
export type InboxView = "inbox" | "unread" | "archived"

/**
 * Smart buckets, derived from kind + payload exactly as inbox-list.tsx's
 * groupOf does today, plus one the current UI drops on the floor: routine
 * progress updates carry payload.subkind = "routine_update" so they stop
 * drowning approvals, and nothing reads it.
 */
export type Bucket = "decisions" | "replies" | "review" | "routines" | "other"

/** An agent or routine the rows are ABOUT — the rail's bottom list. */
export interface SubjectFacet {
  id: string
  label: string
  kind: "agent" | "pipeline"
  count: number
}
