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

/**
 * The three arrangements this preview exists to compare. The rail is the same
 * in all of them — what differs is what sits to the right of it.
 *
 *   split  — mail-client: a list column and a reading pane.
 *   table  — the /routines catalog shape: dense rows, detail in a drawer.
 *   stream — detail-kit cards, decision on the card, no reading pane.
 */
export type LayoutStyle = "split" | "table" | "stream"

/**
 * Who a row is about or from.
 *
 * The distinction the product cares about is machine vs human: an agent, a
 * routine and the system act on their own, a user is a person who answered.
 * Rendering them alike is what makes "casey requested GH_TOKEN" and "pavel
 * approved it" read as the same kind of thing when they are opposites.
 */
export type ActorKind = "agent" | "user" | "routine" | "system" | "crew"

export interface Actor {
  kind: ActorKind
  id: string
  label: string
  /** Avatar seed — agents only; everything else draws a glyph. */
  seed?: string
}

/** A facet row in the rail's bottom list. */
export interface SubjectFacet extends Actor {
  count: number
}
