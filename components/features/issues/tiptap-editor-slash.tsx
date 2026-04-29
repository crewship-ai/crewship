"use client"

import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react"
import type { ComponentType } from "react"
import { Extension } from "@tiptap/core"
import type { Editor, Range } from "@tiptap/core"
import Suggestion from "@tiptap/suggestion"
import {
  Heading1,
  Heading2,
  Heading3,
  List,
  ListOrdered,
  CheckSquare,
  FileCode,
  Quote,
  Table as TableIcon,
  Minus,
  ImagePlus,
} from "lucide-react"
import { cn } from "@/lib/utils"

// Slash-command machinery extracted from the main editor file. The
// SlashCommandList component itself stays unexported — only the
// extension factory is consumed elsewhere.

interface SlashCommandItem {
  title: string
  description: string
  icon: ComponentType<{ className?: string }>
  command: (opts: { editor: Editor; range: Range }) => void
}

const SLASH_ITEMS: SlashCommandItem[] = [
  {
    title: "Heading 1",
    description: "Large heading",
    icon: Heading1,
    command: ({ editor, range }) =>
      editor
        .chain()
        .focus()
        .deleteRange(range)
        .setNode("heading", { level: 1 })
        .run(),
  },
  {
    title: "Heading 2",
    description: "Medium heading",
    icon: Heading2,
    command: ({ editor, range }) =>
      editor
        .chain()
        .focus()
        .deleteRange(range)
        .setNode("heading", { level: 2 })
        .run(),
  },
  {
    title: "Heading 3",
    description: "Small heading",
    icon: Heading3,
    command: ({ editor, range }) =>
      editor
        .chain()
        .focus()
        .deleteRange(range)
        .setNode("heading", { level: 3 })
        .run(),
  },
  {
    title: "Bullet List",
    description: "Unordered list",
    icon: List,
    command: ({ editor, range }) =>
      editor.chain().focus().deleteRange(range).toggleBulletList().run(),
  },
  {
    title: "Ordered List",
    description: "Numbered list",
    icon: ListOrdered,
    command: ({ editor, range }) =>
      editor.chain().focus().deleteRange(range).toggleOrderedList().run(),
  },
  {
    title: "Task List",
    description: "Checklist with checkboxes",
    icon: CheckSquare,
    command: ({ editor, range }) =>
      editor.chain().focus().deleteRange(range).toggleTaskList().run(),
  },
  {
    title: "Code Block",
    description: "Syntax-highlighted code",
    icon: FileCode,
    command: ({ editor, range }) =>
      editor.chain().focus().deleteRange(range).toggleCodeBlock().run(),
  },
  {
    title: "Blockquote",
    description: "Indented quote block",
    icon: Quote,
    command: ({ editor, range }) =>
      editor.chain().focus().deleteRange(range).toggleBlockquote().run(),
  },
  {
    title: "Table",
    description: "3x3 table with header",
    icon: TableIcon,
    command: ({ editor, range }) =>
      editor
        .chain()
        .focus()
        .deleteRange(range)
        .insertTable({ rows: 3, cols: 3, withHeaderRow: true })
        .run(),
  },
  {
    title: "Divider",
    description: "Horizontal rule",
    icon: Minus,
    command: ({ editor, range }) =>
      editor.chain().focus().deleteRange(range).setHorizontalRule().run(),
  },
  {
    title: "Image",
    description: "Insert image from URL",
    icon: ImagePlus,
    command: ({ editor, range }) => {
      editor.chain().focus().deleteRange(range).run()
      const url = window.prompt("Image URL:")
      if (!url) return
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      ;(editor.chain().focus() as any).setImage({ src: url }).run()
    },
  },
]

// ---------------------------------------------------------------------------
// Slash command suggestion dropdown (rendered via React ref)
// ---------------------------------------------------------------------------
interface SlashCommandListProps {
  items: SlashCommandItem[]
  command: (item: SlashCommandItem) => void
}

const SlashCommandList = forwardRef<
  { onKeyDown: (props: { event: KeyboardEvent }) => boolean },
  SlashCommandListProps
>(function SlashCommandList({ items, command }, ref) {
  const [selectedIndex, setSelectedIndex] = useState(0)
  const scrollRef = useRef<HTMLDivElement>(null)

  // Reset selection when items change
  useEffect(() => {
    setSelectedIndex(0)
  }, [items])

  // Scroll selected item into view
  useEffect(() => {
    const container = scrollRef.current
    if (!container) return
    const selected = container.children[selectedIndex] as HTMLElement | undefined
    if (selected) {
      selected.scrollIntoView({ block: "nearest" })
    }
  }, [selectedIndex])

  useImperativeHandle(ref, () => ({
    onKeyDown: ({ event }: { event: KeyboardEvent }) => {
      if (event.key === "ArrowUp") {
        setSelectedIndex((prev) => (prev <= 0 ? items.length - 1 : prev - 1))
        return true
      }
      if (event.key === "ArrowDown") {
        setSelectedIndex((prev) => (prev >= items.length - 1 ? 0 : prev + 1))
        return true
      }
      if (event.key === "Enter") {
        const item = items[selectedIndex]
        if (item) command(item)
        return true
      }
      return false
    },
  }))

  if (items.length === 0) {
    return (
      <div className="bg-[#1a1a2e] border border-white/[0.12] rounded-lg shadow-2xl p-3 text-xs text-muted-foreground/50">
        No matching commands
      </div>
    )
  }

  return (
    <div
      ref={scrollRef}
      className="bg-[#1a1a2e] border border-white/[0.12] rounded-lg shadow-2xl py-1 max-h-[280px] overflow-y-auto w-[220px] scrollbar-thin scrollbar-thumb-white/10"
    >
      {items.map((item, index) => {
        const Icon = item.icon
        return (
          <button
            key={item.title}
            type="button"
            onClick={() => command(item)}
            className={cn(
              "flex items-center gap-2.5 w-full text-left px-3 py-1.5 text-xs transition-colors",
              index === selectedIndex
                ? "bg-white/[0.1] text-foreground"
                : "text-muted-foreground hover:bg-white/[0.06]",
            )}
          >
            <Icon className="h-3.5 w-3.5 shrink-0 opacity-60" />
            <div className="min-w-0">
              <div className="font-medium truncate">{item.title}</div>
              <div className="text-[10px] text-muted-foreground/50 truncate">
                {item.description}
              </div>
            </div>
          </button>
        )
      })}
    </div>
  )
})

// ---------------------------------------------------------------------------
// Slash commands extension
// ---------------------------------------------------------------------------
function createSlashCommandsExtension() {
  return Extension.create({
    name: "slashCommands",

    addOptions() {
      return {
        suggestion: {
          char: "/",
          command: ({
            editor,
            range,
            props,
          }: {
            editor: Editor
            range: Range
            props: SlashCommandItem
          }) => {
            props.command({ editor, range })
          },
          items: ({ query }: { query: string }) => {
            return SLASH_ITEMS.filter((item) =>
              item.title.toLowerCase().includes(query.toLowerCase()),
            )
          },
          render: () => {
            let component: HTMLDivElement | null = null
            let reactRoot: ReturnType<
              typeof import("react-dom/client").createRoot
            > | null = null
            let refHandle: {
              onKeyDown: (props: { event: KeyboardEvent }) => boolean
            } | null = null

            return {
              onStart: (props: {
                clientRect?: (() => DOMRect | null) | null
                items: SlashCommandItem[]
                command: (item: SlashCommandItem) => void
              }) => {
                component = document.createElement("div")
                component.style.position = "absolute"
                component.style.zIndex = "50"
                document.body.appendChild(component)

                const rect = props.clientRect?.()
                if (rect) {
                  component.style.left = `${rect.left}px`
                  component.style.top = `${rect.bottom + 4}px`
                }

                // Dynamic import to avoid SSR issues
                import("react-dom/client").then(({ createRoot }) => {
                  if (!component) return
                  reactRoot = createRoot(component)
                  reactRoot.render(
                    <SlashCommandList
                      items={props.items}
                      command={props.command}
                      ref={(handle) => {
                        refHandle = handle
                      }}
                    />,
                  )
                })
              },

              onUpdate: (props: {
                clientRect?: (() => DOMRect | null) | null
                items: SlashCommandItem[]
                command: (item: SlashCommandItem) => void
              }) => {
                if (!component) return

                const rect = props.clientRect?.()
                if (rect) {
                  component.style.left = `${rect.left}px`
                  component.style.top = `${rect.bottom + 4}px`
                }

                reactRoot?.render(
                  <SlashCommandList
                    items={props.items}
                    command={props.command}
                    ref={(handle) => {
                      refHandle = handle
                    }}
                  />,
                )
              },

              onKeyDown: (props: { event: KeyboardEvent }) => {
                if (props.event.key === "Escape") {
                  reactRoot?.unmount()
                  component?.remove()
                  component = null
                  reactRoot = null
                  refHandle = null
                  return true
                }
                return refHandle?.onKeyDown(props) ?? false
              },

              onExit: () => {
                reactRoot?.unmount()
                component?.remove()
                component = null
                reactRoot = null
                refHandle = null
              },
            }
          },
        },
      }
    },

    addProseMirrorPlugins() {
      return [
        Suggestion({
          editor: this.editor,
          ...this.options.suggestion,
        }),
      ]
    },
  })
}

export type { SlashCommandItem }
export { createSlashCommandsExtension }
