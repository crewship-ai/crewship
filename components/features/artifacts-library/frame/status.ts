import { AlertTriangle, Bot, Check, Clock, CloudOff, Info, Lock, LockOpen, Pencil, RotateCcw, type LucideIcon } from "lucide-react"
import type { StatusTone } from "@/lib/format-status"

/**
 * The status chip's vocabulary: the one place in an open artifact that says
 * who can do what right now. A kind reports one key from its own backend
 * state; it never invents one. Colour rule (Harbor 2 v87): green live,
 * grey saved or read-only, amber stale or held by someone else, violet an
 * agent or a proposal, red an error or conflict.
 */
export type FrameStatusKey =
  | "live" | "saving" | "saved" | "offline" | "readonly" | "free" | "editing" | "held"
  | "agent" | "proposal" | "draft" | "stale" | "refreshing" | "unconfirmed" | "error"

export interface FrameStatusDef {
  tone: StatusTone
  /** "dot" pulses (live), "spin" turns (in flight); otherwise a lucide glyph. */
  icon: "dot" | "spin" | LucideIcon
  label: string
  detail: string
}

export const FRAME_STATUS: Record<FrameStatusKey, FrameStatusDef> = {
  live: { tone: "success", icon: "dot", label: "Live", detail: "Everyone sees changes as they happen." },
  // Saving… stays until the server confirms the write; only then Saved.
  saving: { tone: "muted", icon: "spin", label: "Saving…", detail: "Your last change is on its way. It is not saved until the server confirms it." },
  saved: { tone: "muted", icon: Check, label: "Saved", detail: "The server confirmed your last change as the latest version." },
  offline: { tone: "warn", icon: CloudOff, label: "Waiting for connection", detail: "Changes stay on this device and send when Crewship is back." },
  readonly: { tone: "muted", icon: Lock, label: "Read-only", detail: "You can read this artifact. Ask its owner for edit access." },
  free: { tone: "success", icon: LockOpen, label: "Free to edit", detail: "Nobody is editing. Take the lock to edit." },
  editing: { tone: "blue", icon: Pencil, label: "You are editing", detail: "Others see a read-only preview until you release." },
  held: { tone: "warn", icon: Lock, label: "Someone is editing", detail: "You see a read-only preview until they release." },
  agent: { tone: "purple", icon: Bot, label: "Agent working", detail: "An agent holds the lock and edits. Every save is a new version." },
  proposal: { tone: "purple", icon: Bot, label: "Proposal waiting", detail: "Changes wait for your review. The file is unchanged until you accept." },
  draft: { tone: "warn", icon: RotateCcw, label: "Recovered draft", detail: "This browser kept a local draft. Nothing was overwritten." },
  stale: { tone: "warn", icon: Clock, label: "Stale", detail: "The last refresh did not succeed. Values keep their last confirmed state." },
  refreshing: { tone: "muted", icon: "spin", label: "Refreshing…", detail: "Showing the last confirmed data until the run finishes." },
  unconfirmed: { tone: "warn", icon: AlertTriangle, label: "Not confirmed yet", detail: "Your last edits are on this device. Crewship has not confirmed them." },
  error: { tone: "danger", icon: AlertTriangle, label: "Error", detail: "Something failed. Nothing was changed." },
}

/** What a kind reports: a key, optionally a more precise label or detail ("Jana is editing"). */
export interface FrameStatus {
  key: FrameStatusKey
  label?: string
  detail?: string
}

export const statusInfo = (status: FrameStatus): FrameStatusDef & { key: FrameStatusKey } => ({
  ...FRAME_STATUS[status.key], key: status.key,
  ...(status.label ? { label: status.label } : {}),
  ...(status.detail ? { detail: status.detail } : {}),
})

export const STATUS_INFO_ICON = Info

/** The native Craft editor's own state, as the host knows it (native-craft-editor-host). */
export interface CraftHostState {
  /** The server confirmed read access and the editor opened. */
  ready: boolean
  /** The save coordinator's phase: saving, saved, draft, uncertain… */
  phase?: string
  /** The last save the server acknowledged; set only after its reply. */
  savedVersion?: string
  dirty: boolean
  canEdit: boolean
  /** The caller holds the lease and the editor is writable. */
  editing: boolean
  holder: { kind: "human" | "agent"; name: string } | null
  holderIsMe: boolean
  openProposals: number
  recoveredDrafts: number
  offline?: boolean
}

/**
 * The chip for a native Word or Sheet. Strongest first: a waiting proposal or
 * recovered draft needs the person, then the save in flight, then who holds
 * the lock. Saved only once the server acknowledged a version.
 */
export function craftFrameStatus(state: CraftHostState): FrameStatus | null {
  if (!state.ready) return null
  if (state.openProposals > 0) return { key: "proposal", label: state.openProposals === 1 ? "Proposal waiting" : `${state.openProposals} proposals waiting` }
  if (state.recoveredDrafts > 0) return { key: "draft" }
  if (state.offline) return { key: "offline" }
  if (state.phase === "uncertain") return { key: "unconfirmed" }
  if (state.phase === "saving") return { key: "saving" }
  if (state.holder && !state.holderIsMe) {
    return state.holder.kind === "agent"
      ? { key: "agent", label: `${state.holder.name} is editing` }
      : { key: "held", label: `${state.holder.name} is editing` }
  }
  if (state.editing) {
    if (state.phase === "saved" && state.savedVersion) return { key: "saved", label: `Saved · v${state.savedVersion}` }
    if (state.dirty) return { key: "unconfirmed" }
    return { key: "editing" }
  }
  if (!state.canEdit) return { key: "readonly" }
  return { key: "free" }
}
