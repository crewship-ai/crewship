/**
 * The public page wire (PRD `docs/prd/pages.md` §7.3).
 *
 * This mirrors `pagePublicPanelWire` / `pagePublicWire` in
 * `internal/api/pages_public.go`, and the mirroring is the point: the server
 * builds those types additively, field by field, so an outsider receives an
 * allow-list rather than a filtered copy of the internal document. Declaring
 * them again here means the client cannot read a field the server does not
 * send, and — more usefully — a reviewer can see the whole of what crosses the
 * boundary in one screen.
 *
 * READ THE FIELDS THAT ARE NOT HERE. There is no `owner`, no `producer`, no
 * `reason`, no `sla_seconds` and no `actions`, because the server never sends
 * them (§7.3.2 rules 1 and 5, §7.3.2b). Nothing in this directory hides a
 * field: §7.1 rule 5 is absolute — "a hidden-but-delivered panel is a data
 * leak" — so the client's job is to render what arrived, not to decide what
 * should have.
 */

import type { PanelState } from "@/components/features/pages/panels/types"

/** Server-attached provenance, present only when the publisher opted in. */
export interface PublicPanelProvenance {
  producer?: string | null
  run_id?: string | null
  produced_at?: string | null
}

/** One panel of a public page. */
export interface PublicPanel {
  id: string
  /** Untrusted until narrowed by the registry, exactly as internally. */
  schema: string
  title?: string
  span?: number
  state: PanelState
  /**
   * When the data were produced — always sent when anything ever was,
   * INCLUDING on a failed panel. §7.3.2b: show the age.
   */
  produced_at?: string
  /** Absent on a failed panel: the payload is where the failure text lives. */
  data?: unknown
  provenance?: PublicPanelProvenance | null
}

/** The public document. */
export interface PublicPage {
  slug: string
  name: string
  description?: string
  panels: PublicPanel[]
  generated_at: string
  show_provenance: boolean
  expires_at: string
}

/**
 * What the fetch settled on. `password` is not an error state — a protected
 * link is working exactly as published — so it is a first-class outcome rather
 * than a 401 the caller has to interpret.
 */
export type PublicPageStatus = "loading" | "ready" | "password" | "unavailable" | "error"
