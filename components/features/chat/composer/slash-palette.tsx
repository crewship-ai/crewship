"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { useHotkeys } from "react-hotkeys-hook"
import {
  MessageSquarePlus,
  Eraser,
  FileCode,
  Search,
  Download,
  Undo2,
  PanelRight,
  Sparkles,
  CalendarClock,
  AlertCircle,
  Brain,
  Key,
  type LucideIcon,
} from "lucide-react"

import {
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandSeparator,
  CommandShortcut,
} from "@/components/ui/command"
import { useDrawerStore, type DrawerTab } from "@/stores/drawer-store"
import {
  useSlashCommands,
  type SlashActionSchema as ServerSlashCommand,
} from "@/hooks/use-slash-commands"

interface DrawerApi {
  toggle: (tab?: DrawerTab) => void
}

interface SlashPaletteProps {
  onCommand?: (id: string, args?: string) => void
  /** Current chat agent slug (used for /new-session deeplinks). */
  agentSlug?: string
  /** Active workspace — required for the server-driven actions group.
   *  Omit on surfaces that don't have a workspace context yet (e.g.
   *  pre-onboarding); the palette renders without the actions group. */
  workspaceId?: string
  /** Invoked when the user picks a server-driven action. The parent
   *  chat panel owns the SlashActionModal lifecycle so the form can
   *  read conversation context for pre-fill. */
  onAction?: (command: ServerSlashCommand) => void
  /** Rows the host cannot honour *right now*, id → reason (e.g. nothing
   *  to regenerate in an empty conversation). Rendered disabled with the
   *  reason on screen, exactly like a statically-disabled row. This is
   *  the runtime half of the honesty contract below; the static half is
   *  CLIENT_ACTION_CONTRACT. */
  disabledCommands?: Record<string, string>
  /** Controlled open state. Omit to let the palette own it (⌘K). */
  open?: boolean
  onOpenChange?: (open: boolean) => void
}

// ── The honesty contract (audit P0.8) ───────────────────────────────────────
//
// A command that closes the palette and does nothing is the same class of
// defect as a clickable-looking <span>: the UI states a capability the system
// does not have. So every action this palette can put on screen — the
// client-side rows below AND the server-driven catalogue in
// internal/api/slash_commands_handler.go — is classified as exactly one of:
//
//   · enabled  — selecting it performs the effect its label advertises;
//   · disabled — rendered, visibly disabled, with the reason on screen;
//   · hidden   — not rendered at all.
//
// __tests__/slash-palette-contract.test.tsx enumerates both sources and fails
// on anything unclassified, including a new entry added to the Go catalogue.
// Adding a row here without an entry below is therefore a red test, not a
// silent no-op.

export type SlashActionClassification =
  | { state: "enabled" }
  | { state: "disabled"; reason: string }
  | { state: "hidden"; reason: string }

/** Classification of every client-side row this file has ever offered.
 *  Hidden entries stay listed on purpose: the reason is the record of why the
 *  row went away, and the contract test asserts they are not rendered. */
export const CLIENT_ACTION_CONTRACT: Record<string, SlashActionClassification> = {
  // Creating a session is the chat PAGE's job: it POSTs the chat, refetches
  // the session list and selects the new row. `router.push("/chat/<slug>")`
  // does none of that — the page holds the active session in local state and
  // does not re-read the URL on a client navigation, so the old code changed
  // the address bar and nothing else.
  "new-session": {
    state: "disabled",
    reason: "Use New session in the chat header",
  },
  clear: { state: "enabled" },
  regenerate: { state: "enabled" },
  // Nothing in the client or the API creates an alternate reply from a turn:
  // no branch endpoint, no branch state in useChat, no handler in ChatPanel.
  branch: {
    state: "hidden",
    reason: "No alternate-reply path exists in the client or the API",
  },
  search: { state: "enabled" },
  export: { state: "enabled" },
  "open-files": { state: "enabled" },
  // The Context tab moved to the agent canvas. The rail no longer renders a
  // button for it and RightRail rewrites a persisted "context" tab back to
  // "files" on mount, so this row opened the Files panel under another name.
  "open-context": {
    state: "hidden",
    reason: "The Context tab moved to the agent canvas; this opened Files",
  },
  "toggle-drawer": { state: "enabled" },
  // "Hand off to subagent" — chat has no subagent hand-off. Delegation
  // arrives as events from the backend; there is no client action that starts
  // one, and ChatPanel never had a handler for this id.
  "run-task": {
    state: "hidden",
    reason: "Chat cannot start a subagent hand-off",
  },
}

/** Classification of the server catalogue. Keyed by the ids in
 *  internal/api/slash_commands_handler.go — capability filtering happens
 *  server-side, so an entry reaching the client only means the caller MAY run
 *  it, not that this build knows how. */
export const SERVER_ACTION_CONTRACT: Record<string, SlashActionClassification> = {
  // "Create routine from this conversation" cannot be honoured. The endpoint
  // it maps to (POST /workspaces/{ws}/pipeline-schedules) SCHEDULES an
  // existing pipeline: resolveSchedulePipelineID rejects any body without
  // target_pipeline_id/slug, so name+cron+timezone is a guaranteed 400. The
  // missing piece is the transcript→routine step, which does not exist —
  // see the audit's §5.2 for why its shape is still an open question.
  routine: {
    state: "disabled",
    reason: "Needs an existing routine to schedule — a transcript is not one",
  },
  // Issue creation is crew-scoped: the only POST route is
  // /api/v1/crews/{crewId}/issues (POST /api/v1/issues is not registered).
  // Chat carries an agent, not a crew, and the panel is not allowed to fetch
  // the agent record on mount — chat-panel-ask-forms.test.tsx pins that this
  // surface makes no such request. Wiring this needs the crew id passed down
  // from the chat page, which already has it in the roster it fetched.
  issue: {
    state: "disabled",
    reason: "Issue create is crew-scoped and chat has no crew id yet",
  },
  skill: { state: "enabled" },
  credential: { state: "enabled" },
}

/** A catalogue entry from a server newer than this build. It has no endpoint
 *  mapping in slash-action-modal.tsx, so it is offered as what it is. */
const UNCLASSIFIED_SERVER_ACTION: SlashActionClassification = {
  state: "disabled",
  reason: "This build doesn't know how to run this action",
}

function classifyClient(id: string): SlashActionClassification {
  return CLIENT_ACTION_CONTRACT[id] ?? UNCLASSIFIED_SERVER_ACTION
}

function classifyServer(id: string): SlashActionClassification {
  return SERVER_ACTION_CONTRACT[id] ?? UNCLASSIFIED_SERVER_ACTION
}

// Server-driven icon resolution. The catalog uses lucide icon names;
// we map them to components here so the registry can stay stringly-
// typed on the wire. Unknown icon names fall back to Sparkles so an
// unrecognised entry still renders.
const ICON_BY_NAME: Record<string, LucideIcon> = {
  "calendar-clock": CalendarClock,
  "alert-circle": AlertCircle,
  brain: Brain,
  sparkles: Sparkles,
  key: Key,
}

interface SlashCommand {
  id: string
  label: string
  hint?: string
  icon: React.ComponentType<{ className?: string }>
  shortcut?: string
  group: "chat" | "view" | "tools" | "navigation"
  /** Who performs the effect. "panel" rows call `onCommand` and the host
   *  (ChatPanel) does the work — chat-panel-slash-actions.test.tsx walks
   *  PANEL_HANDLED_COMMAND_IDS and fails until the host implements one. */
  handledBy: "panel" | "palette"
  run: (ctx: SlashRunCtx) => void | Promise<void>
}

interface SlashRunCtx {
  router: ReturnType<typeof useRouter>
  drawer: DrawerApi
  agentSlug?: string
  onCommand?: (id: string, args?: string) => void
  close: () => void
}

const COMMANDS: SlashCommand[] = [
  {
    id: "new-session",
    label: "New session",
    hint: "Start a fresh chat",
    icon: MessageSquarePlus,
    group: "chat",
    handledBy: "palette",
    // Disabled by the contract above — kept so the row can explain itself
    // rather than vanishing from a palette people have learned.
    run: ({ router, agentSlug, close }) => {
      if (agentSlug) router.push(`/chat/${agentSlug}`)
      close()
    },
  },
  {
    id: "clear",
    label: "Clear conversation",
    hint: "Wipe visible turns (keeps history)",
    icon: Eraser,
    group: "chat",
    handledBy: "panel",
    run: ({ onCommand, close }) => {
      onCommand?.("clear")
      close()
    },
  },
  {
    id: "regenerate",
    label: "Regenerate last response",
    icon: Undo2,
    group: "chat",
    handledBy: "panel",
    run: ({ onCommand, close }) => {
      onCommand?.("regenerate")
      close()
    },
  },
  {
    id: "search",
    label: "Search in conversation",
    icon: Search,
    shortcut: "⌘F",
    group: "tools",
    handledBy: "panel",
    run: ({ onCommand, close }) => {
      onCommand?.("search")
      close()
    },
  },
  {
    id: "export",
    label: "Export conversation",
    hint: "Markdown / copy",
    icon: Download,
    shortcut: "⌘E",
    group: "tools",
    handledBy: "panel",
    run: ({ onCommand, close }) => {
      onCommand?.("export")
      close()
    },
  },
  {
    id: "open-files",
    label: "Open Files panel",
    icon: FileCode,
    shortcut: "⌘1",
    group: "view",
    handledBy: "palette",
    run: ({ drawer, close }) => {
      drawer.toggle("files")
      close()
    },
  },
  {
    id: "toggle-drawer",
    label: "Toggle right panel",
    icon: PanelRight,
    shortcut: "⌘B",
    group: "view",
    handledBy: "palette",
    run: ({ drawer, close }) => {
      drawer.toggle()
      close()
    },
  },
]

/** Ids the palette can put on screen. The contract test asserts each one is
 *  classified, and that nothing classified `hidden` is in here. */
export const CLIENT_COMMAND_IDS: string[] = COMMANDS.map((c) => c.id)

/** Enabled rows whose effect the HOST must implement. Walked by
 *  chat-panel-slash-actions.test.tsx. */
export const PANEL_HANDLED_COMMAND_IDS: string[] = COMMANDS
  .filter((c) => c.handledBy === "panel" && classifyClient(c.id).state === "enabled")
  .map((c) => c.id)

const GROUP_LABELS: Record<SlashCommand["group"], string> = {
  chat: "Chat",
  view: "View",
  tools: "Tools",
  navigation: "Navigation",
}

/** Why a row cannot be run right now, or null when it can. Runtime reasons
 *  from the host win over the static classification only in the sense that
 *  either one is enough to disable — neither can re-enable the other. */
function reasonFor(
  classification: SlashActionClassification,
  runtimeReason: string | undefined,
): string | null {
  if (classification.state === "disabled") return classification.reason
  if (runtimeReason) return runtimeReason
  return null
}

export function SlashPalette({
  onCommand,
  agentSlug,
  workspaceId,
  onAction,
  disabledCommands,
  open: openProp,
  onOpenChange,
}: SlashPaletteProps) {
  const [internalOpen, setInternalOpen] = useState(false)
  const open = openProp ?? internalOpen
  const setOpen = (next: boolean) => {
    if (openProp === undefined) setInternalOpen(next)
    onOpenChange?.(next)
  }
  const router = useRouter()
  const toggleDrawer = useDrawerStore((s) => s.toggle)

  // Server-driven actions — capability-filtered per the caller's
  // workspace_members.capabilities row. Empty list (= no grants) just
  // means the actions group doesn't render; chat/view/tools/navigation
  // groups continue to work unchanged so the palette is never broken
  // by a chat-only user.
  const { data: actions = [] } = useSlashCommands(workspaceId)

  useHotkeys(
    ["mod+k"],
    () => setOpen(!open),
    { preventDefault: true, enableOnFormTags: true, enableOnContentEditable: true },
    [open, openProp],
  )

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, openProp])

  const ctx: SlashRunCtx = {
    router,
    drawer: { toggle: toggleDrawer },
    agentSlug,
    onCommand,
    close: () => setOpen(false),
  }

  // Actions classified `hidden` never reach the list, so the group's count is
  // the count of what is actually on screen.
  const visibleActions = actions.filter((cmd) => classifyServer(cmd.id).state !== "hidden")

  const grouped = COMMANDS.reduce<Record<string, SlashCommand[]>>((acc, cmd) => {
    ;(acc[cmd.group] ??= []).push(cmd)
    return acc
  }, {})

  return (
    <CommandDialog open={open} onOpenChange={setOpen} title="Command palette" description="Run a command">
      <CommandInput placeholder="Type a command or search…" />
      <CommandList>
        <CommandEmpty>No commands match.</CommandEmpty>
        {/* Server-driven actions group — renders first so capability-
            granted actions are the high-signal items at the top of
            the palette. Hidden entirely when the user has no grants
            (the rest of the palette is unaffected). */}
        {visibleActions.length > 0 && (
          <>
            <CommandGroup heading={`Actions (${visibleActions.length})`}>
              {visibleActions.map((cmd) => {
                const Icon = (cmd.icon && ICON_BY_NAME[cmd.icon]) || Sparkles
                const reason = reasonFor(classifyServer(cmd.id), disabledCommands?.[cmd.id])
                return (
                  <CommandItem
                    key={`server-${cmd.id}`}
                    data-testid={`slash-action-${cmd.id}`}
                    value={`${cmd.label} ${cmd.label_cs ?? ""}`}
                    disabled={reason !== null}
                    onSelect={() => {
                      if (reason !== null) return
                      onAction?.(cmd)
                      setOpen(false)
                    }}
                  >
                    <Icon className="h-4 w-4" />
                    <span>{cmd.label}</span>
                    {reason !== null && (
                      <span data-slash-reason className="ml-auto text-xs text-muted-foreground truncate">
                        {reason}
                      </span>
                    )}
                  </CommandItem>
                )
              })}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}
        {Object.entries(grouped).map(([group, list], gi) => (
          <div key={group}>
            {gi > 0 && <CommandSeparator />}
            <CommandGroup heading={GROUP_LABELS[group as SlashCommand["group"]]}>
              {list.map((cmd) => {
                const Icon = cmd.icon
                const reason = reasonFor(classifyClient(cmd.id), disabledCommands?.[cmd.id])
                return (
                  <CommandItem
                    key={cmd.id}
                    data-testid={`slash-item-${cmd.id}`}
                    value={`${cmd.label} ${cmd.hint ?? ""}`}
                    disabled={reason !== null}
                    onSelect={() => {
                      if (reason !== null) return
                      cmd.run(ctx)
                    }}
                  >
                    <Icon className="h-4 w-4" />
                    <span>{cmd.label}</span>
                    {reason === null && cmd.hint && (
                      <span className="ml-2 text-xs text-muted-foreground truncate">
                        {cmd.hint}
                      </span>
                    )}
                    {reason !== null && (
                      <span data-slash-reason className="ml-auto text-xs text-muted-foreground truncate">
                        {reason}
                      </span>
                    )}
                    {reason === null && cmd.shortcut && (
                      <CommandShortcut>{cmd.shortcut}</CommandShortcut>
                    )}
                  </CommandItem>
                )
              })}
            </CommandGroup>
          </div>
        ))}
      </CommandList>
    </CommandDialog>
  )
}
