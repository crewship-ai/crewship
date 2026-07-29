import {
  Activity, AtSign, BookOpen, Brain, CircleDot, FolderTree, Inbox, Key, LayoutDashboard,
  MessageSquare, Play, Plug, ScrollText, Settings, ShieldCheck, Store, Users, Zap,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

/**
 * One icon per concept, for the whole product.
 *
 * The nav rail had already picked these — Issues is a CircleDot, Routines a
 * ScrollText, Skills a Zap, Credentials a Key, Integrations a Plug — and every
 * other surface then picked again from memory. The agent overview ended up
 * showing Routines as a Workflow, Skills as Sparkles and Tools as a Wrench, so
 * the same four concepts wore different faces depending on which screen you
 * were looking at. An icon that changes between screens stops being a symbol
 * and becomes decoration.
 *
 * app-sidebar.tsx consumes this map rather than keeping its own copy, so this
 * is the definition and not a second opinion about it.
 *
 * Adding a concept: check whether it already has an icon SOMEWHERE before
 * inventing one. That check is the entire point of this file.
 */
export const CONCEPT_ICON = {
  // ── Nav-level concepts (these came from the rail) ──
  dashboard: LayoutDashboard,
  inbox: Inbox,
  issues: CircleDot,
  routines: ScrollText,
  activity: Activity,
  journal: BookOpen,
  crews: Users,
  skills: Zap,
  credentials: Key,
  integrations: Plug,
  marketplace: Store,
  settings: Settings,
  admin: ShieldCheck,

  // ── Concepts that live inside a detail screen ──
  /** What starts an agent: a schedule, a webhook, a peer, a person. */
  triggers: Play,
  /** Unattended executions. Opens the journal, so it wears the journal's icon. */
  runs: BookOpen,
  /** Conversations with a person. Opens chat. */
  sessions: MessageSquare,
  /** Messages from other agents. Opens the inbox. */
  peers: AtSign,
  /** Where notifications go out. */
  channels: Inbox,
  /** What the agent remembers between sessions. */
  memory: Brain,
  /** The agent's files. */
  workspace: FolderTree,
  /** Tools reached through a connector — same concept as Integrations. */
  tools: Plug,
} as const satisfies Record<string, LucideIcon>

export type ConceptKey = keyof typeof CONCEPT_ICON
