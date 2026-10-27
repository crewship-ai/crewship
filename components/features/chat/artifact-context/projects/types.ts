/**
 * Wire types for artifact projects (multi-screen HTML) and Craft edit leases
 * as the chat consumes them. Field names match the server JSON exactly.
 *
 * Artifact project routes: pages/{slug}/artifact (VersionStore +
 * comments). Fields marked "proposed" are requested in the coordination
 * contract and not served yet; the UI shows the matching control only when
 * the server sends the field, never a guess.
 */

export interface ArtifactFile { path: string; content: string; media_type: string }

export interface ArtifactManifest {
  version: number
  entrypoint: string
  files: ArtifactFile[]
  created_at?: string
  message?: string
  restored_from?: number | null
}

export interface ArtifactVersionInfo {
  version: number
  entry_point?: string
  message?: string
  actor?: string
  created_at?: string
  restored_from?: number | null
}

export interface ArtifactQuota { used_bytes: number; limit_bytes: number; warning: boolean; retention?: string }

/** proposed: queue state of a comment sent to the agent. */
export type ArtifactCommentStatus = "pending" | "working" | "answered" | "needs_clarification"

export interface ArtifactCommentReply { text: string; version: number; author?: string; created_at?: string }

export interface ArtifactComment {
  id: string
  version: number
  path: string
  /** A single `#id` selector, unique in that file. Empty for a whole-project comment. */
  selector: string
  quote: string
  text: string
  author?: string
  task_id?: string
  created_at?: string
  /** proposed */
  status?: ArtifactCommentStatus
  /** proposed: the version the agent produced for this comment. */
  answered_version?: number
  replies?: ArtifactCommentReply[]
}

export interface ArtifactProjectSnapshot {
  page_id: string
  /** proposed: display name and slug, so a chat card needs no second request. */
  name?: string
  slug?: string
  origin?: { chat_id: string; agent_id: string } | null
  enabled?: boolean
  can_write: boolean
  latest_version: number
  /** 0 when no version is shared. */
  shared_version: number
  manifest: ArtifactManifest
  /** Owner only; readers get an empty list. */
  versions: ArtifactVersionInfo[]
  quota?: ArtifactQuota
  comments: ArtifactComment[]
}

export interface ArtifactAnchor { version: number; path: string; selector: string; quote: string }

export interface CreateArtifactCommentBody extends ArtifactAnchor { text: string; send_to_agent: boolean }

/** Server error codes the UI distinguishes. */
export type ArtifactErrorCode =
  | "artifact_version_conflict"
  | "artifact_anchor_mismatch"
  | "artifact_history_quota"
  | "forbidden"
  | "not_found"
  | "invalid"
  | "unavailable"

/**
 * Result JSON of the agent tools `artifact_create`, `artifact_publish` and
 * `artifact_restore` as it appears in a chat tool_result part.
 */
export interface ArtifactToolResult { page_id?: string; slug?: string; version?: number; url?: string; actor?: string }

/** Card state derived from the snapshot; never from streaming text. */
export type ArtifactCardStatus = "draft" | "shared"

// ---- Craft edit lease (proposed HTTP surface over EditLease) ----

export interface CraftLeaseHolder { actor_id: string; display_name: string; kind: "human" | "agent" }

/** A pending request to take over the lease. */
export interface CraftLeaseRequest { request_id: string; actor_id: string; display_name: string; kind: "human" | "agent" }

export interface CraftLeaseStatus {
  /** null when the document is free. */
  holder: CraftLeaseHolder | null
  /** True only for the admission that holds the lease; a chat preview is its own admission, so never there. */
  mine: boolean
  /** Canonical decimal string; null before the first acquisition. */
  generation: string | null
  expires_at?: string
  can_write: boolean
  /** Pending handoff requests (wire name handoff_requested_by). */
  requests: CraftLeaseRequest[]
}

export interface CraftProposal {
  id: string
  author: CraftLeaseHolder
  base_version: number
  summary: string
  created_at: string
  state: "open" | "accepted" | "rejected" | "stale"
}
