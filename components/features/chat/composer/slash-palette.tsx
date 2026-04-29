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

interface DrawerApi {
  toggle: (tab?: DrawerTab) => void
}

interface SlashPaletteProps {
  onCommand?: (id: string, args?: string) => void
  /** Current chat agent slug (used for /new-session deeplinks). */
  agentSlug?: string
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

export function SlashPalette({ onCommand, agentSlug }: SlashPaletteProps) {
  const [open, setOpen] = useState(false)
  const router = useRouter()
  const toggleDrawer = useDrawerStore((s) => s.toggle)

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
