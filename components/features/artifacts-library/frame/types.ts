import type { LucideIcon } from "lucide-react"
import type { FrameStatus } from "./status"

/** Who may see the artifact at all. Each gate has its own page; content never shows behind one. */
export type FrameAccess = "ok" | "signedout" | "denied" | "missing"

/** Ask answers and changes nothing; Propose waits for review; Edit changes directly and every change is a version. */
export type AgentMode = "ask" | "propose" | "edit"

/**
 * A mode the drawer can send itself. A mode whose form belongs to the kind
 * (the Craft agent task, a comment queue) is registered from the body with
 * <ArtifactAgentMode mode="…"> instead, and only while its backend admits it.
 */
export interface AgentSend {
  /** The existing backend path, for the permission line and the contract doc. */
  path: string
  send: (prompt: string, scope: string) => Promise<boolean>
}

export interface AgentSupport {
  /** Ask: opens the existing chat with this artifact as context (`/chat?page=`), the prompt as an unsent draft. */
  ask?: { chatHref: (prompt: string) => string }
  propose?: AgentSend
  edit?: AgentSend
  /** Scope chips; the first is the default. */
  scopes?: { id: string; label: string }[]
  suggestions?: string[]
  /** One line on what the agent may read and change, from the backend's own rule. */
  permission?: string
}

/** One title-menu entry. An action the backend lacks is left out; one this person may not use stays, disabled, with its reason. */
export interface FrameAction {
  id: string
  label: string
  icon?: LucideIcon
  onSelect?: () => void
  /** Set when the action exists but this person cannot use it now. */
  disabledReason?: string
  danger?: boolean
  /** A separator before this entry. */
  separated?: boolean
  shortcut?: string
}

/**
 * The frame's source of truth for ONE open artifact. The frame renders only
 * what this says is supported; nothing is mocked. Drawer tabs (Comments,
 * History, a kind's own) appear when the body mounts content for them with
 * <ArtifactDrawerSection>, which it does only when its backend serves them.
 */
export interface FrameCapabilities {
  access: FrameAccess
  /** The caller may change it (server-confirmed; unknown is false). */
  canEdit: boolean
  /** The chip; null hides it (nothing confirmed yet). */
  status: FrameStatus | null
  /** Null hides the Agent tab. */
  agent: AgentSupport | null
  /** Kind or instance actions for the title menu, after the common ones. */
  actions?: FrameAction[]
}

export const NO_FRAME_CAPABILITIES: FrameCapabilities = { access: "ok", canEdit: false, status: null, agent: null }

/** Slot names: same place, same purpose in every kind (Harbor 2 v87 `ART.slot`). */
export type SlotName = "view" | "output" | "tools" | "viewport" | "nav" | "panel"

export type FrameLayout = "desktop" | "tablet" | "phone"

/** Common drawer tabs, in order after the kind's own. */
export const COMMON_TABS = ["agent", "comments", "history", "info"] as const
export type CommonTab = typeof COMMON_TABS[number]
