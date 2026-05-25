"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { useHotkeys } from "react-hotkeys-hook"
import {
  MessageSquarePlus,
  Eraser,
  GitBranch,
  FileCode,
  Play,
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
  type SlashCommand as ServerSlashCommand,
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
    shortcut: "⌘N",
    group: "chat",
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
    run: ({ onCommand, close }) => {
      onCommand?.("regenerate")
      close()
    },
  },
  {
    id: "branch",
    label: "Branch from last response",
    hint: "Create alternate reply",
    icon: GitBranch,
    group: "chat",
    run: ({ onCommand, close }) => {
      onCommand?.("branch")
      close()
    },
  },
  {
    id: "search",
    label: "Search in conversation",
    icon: Search,
    shortcut: "⌘F",
    group: "tools",
    run: ({ onCommand, close }) => {
      onCommand?.("search")
      close()
    },
  },
  {
    id: "export",
    label: "Export conversation",
    hint: "Markdown / PDF / share link",
    icon: Download,
    shortcut: "⌘E",
    group: "tools",
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
    run: ({ drawer, close }) => {
      drawer.toggle("files")
      close()
    },
  },
  {
    id: "open-context",
    label: "Open Context panel",
    icon: Sparkles,
    shortcut: "⌘4",
    group: "view",
    run: ({ drawer, close }) => {
      drawer.toggle("context")
      close()
    },
  },
  {
    id: "toggle-drawer",
    label: "Toggle right panel",
    icon: PanelRight,
    shortcut: "⌘B",
    group: "view",
    run: ({ drawer, close }) => {
      drawer.toggle()
      close()
    },
  },
  {
    id: "run-task",
    label: "Run task…",
    hint: "Hand off to subagent",
    icon: Play,
    group: "tools",
    run: ({ onCommand, close }) => {
      onCommand?.("run-task")
      close()
    },
  },
]

const GROUP_LABELS: Record<SlashCommand["group"], string> = {
  chat: "Chat",
  view: "View",
  tools: "Tools",
  navigation: "Navigation",
}

export function SlashPalette({
  onCommand,
  agentSlug,
  workspaceId,
  onAction,
}: SlashPaletteProps) {
  const [open, setOpen] = useState(false)
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
    () => setOpen((v) => !v),
    { preventDefault: true, enableOnFormTags: true, enableOnContentEditable: true },
  )

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [open])

  const ctx: SlashRunCtx = {
    router,
    drawer: { toggle: toggleDrawer },
    agentSlug,
    onCommand,
    close: () => setOpen(false),
  }

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
        {actions.length > 0 && (
          <>
            <CommandGroup heading={`Actions (${actions.length})`}>
              {actions.map((cmd) => {
                const Icon = (cmd.icon && ICON_BY_NAME[cmd.icon]) || Sparkles
                return (
                  <CommandItem
                    key={`server-${cmd.id}`}
                    value={`${cmd.label} ${cmd.label_cs ?? ""}`}
                    onSelect={() => {
                      onAction?.(cmd)
                      setOpen(false)
                    }}
                  >
                    <Icon className="h-4 w-4" />
                    <span>{cmd.label}</span>
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
                return (
                  <CommandItem
                    key={cmd.id}
                    value={`${cmd.label} ${cmd.hint ?? ""}`}
                    onSelect={() => cmd.run(ctx)}
                  >
                    <Icon className="h-4 w-4" />
                    <span>{cmd.label}</span>
                    {cmd.hint && (
                      <span className="ml-2 text-xs text-muted-foreground truncate">
                        {cmd.hint}
                      </span>
                    )}
                    {cmd.shortcut && <CommandShortcut>{cmd.shortcut}</CommandShortcut>}
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
