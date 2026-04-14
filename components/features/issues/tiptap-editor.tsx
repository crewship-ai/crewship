"use client"

import {
  useEffect,
  useState,
  useCallback,
  useRef,
  forwardRef,
  useImperativeHandle,
} from "react"
import type { ComponentType, ReactNode } from "react"
import { marked } from "marked"
import DOMPurify from "dompurify"
import TurndownService from "turndown"
import {
  useEditor,
  EditorContent,
  NodeViewContent,
  NodeViewWrapper,
  ReactNodeViewRenderer,
} from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import Placeholder from "@tiptap/extension-placeholder"
import Link from "@tiptap/extension-link"
import Underline from "@tiptap/extension-underline"
import TaskList from "@tiptap/extension-task-list"
import TaskItem from "@tiptap/extension-task-item"
import Highlight from "@tiptap/extension-highlight"
import { Table } from "@tiptap/extension-table"
import TableRow from "@tiptap/extension-table-row"
import TableCell from "@tiptap/extension-table-cell"
import TableHeader from "@tiptap/extension-table-header"
import CodeBlockLowlight from "@tiptap/extension-code-block-lowlight"
import Image from "@tiptap/extension-image"
import Typography from "@tiptap/extension-typography"
import { TextStyle } from "@tiptap/extension-text-style"
import { Color } from "@tiptap/extension-color"
import TextAlign from "@tiptap/extension-text-align"
import { Extension } from "@tiptap/core"
import Suggestion from "@tiptap/suggestion"
import { all, createLowlight } from "lowlight"
import "highlight.js/styles/github-dark.css"
import { cn } from "@/lib/utils"
import {
  Bold,
  Italic,
  Underline as UnderlineIcon,
  Strikethrough,
  Code,
  Heading1,
  Heading2,
  Heading3,
  List,
  ListOrdered,
  CheckSquare,
  Quote,
  Minus,
  Link2,
  Table as TableIcon,
  Undo,
  Redo,
  FileCode,
  ChevronDown,
  ImagePlus,
  Highlighter,
  AlignLeft,
  AlignCenter,
  AlignRight,
  Type,
} from "lucide-react"
import type { Editor, Range } from "@tiptap/core"

// ---------------------------------------------------------------------------
// Lowlight setup
// ---------------------------------------------------------------------------
const lowlight = createLowlight(all)

// ---------------------------------------------------------------------------
// Turndown setup (HTML -> Markdown)
// ---------------------------------------------------------------------------
const turndown = new TurndownService({
  headingStyle: "atx",
  codeBlockStyle: "fenced",
  bulletListMarker: "-",
  emDelimiter: "*",
  strongDelimiter: "**",
})

// Task list rules
turndown.addRule("taskListChecked", {
  filter: (node) =>
    node.nodeName === "LI" &&
    node.getAttribute("data-checked") === "true",
  replacement: (content) => `- [x] ${content.trim()}\n`,
})

turndown.addRule("taskListUnchecked", {
  filter: (node) =>
    node.nodeName === "LI" &&
    node.getAttribute("data-checked") === "false",
  replacement: (content) => `- [ ] ${content.trim()}\n`,
})

// Table rule
turndown.addRule("table", {
  filter: "table",
  replacement: (_content, node) => {
    const el = node as HTMLTableElement
    const rows = Array.from(el.querySelectorAll("tr"))
    if (rows.length === 0) return ""

    const headerRow = rows[0]
    const headerCells = Array.from(
      headerRow.querySelectorAll("th, td"),
    ).map((c) => c.textContent?.trim() || "")

    let result = "| " + headerCells.join(" | ") + " |\n"
    result += "| " + headerCells.map(() => "---").join(" | ") + " |\n"

    for (const row of rows.slice(1)) {
      const cells = Array.from(row.querySelectorAll("td, th")).map(
        (c) => c.textContent?.trim() || "",
      )
      result += "| " + cells.join(" | ") + " |\n"
    }

    return "\n" + result + "\n"
  },
})

// Table sub-elements (prevent default processing)
turndown.addRule("tableCell", {
  filter: ["th", "td"],
  replacement: (content) => content.trim(),
})

turndown.addRule("tableRow", {
  filter: "tr",
  replacement: () => "",
})

turndown.addRule("tableSection", {
  filter: ["thead", "tbody", "tfoot"],
  replacement: () => "",
})

// Strikethrough
turndown.addRule("strikethrough", {
  filter: ["s", "del"],
  replacement: (content) => `~~${content}~~`,
})

// Underline (preserve as HTML in markdown)
turndown.addRule("underline", {
  filter: "u",
  replacement: (content) => `<u>${content}</u>`,
})

// Highlight / mark
turndown.addRule("highlight", {
  filter: "mark",
  replacement: (content) => `==${content}==`,
})

function htmlToMarkdown(html: string): string {
  if (!html) return ""
  return turndown.turndown(html).trim()
}

// ---------------------------------------------------------------------------
// Markdown -> HTML
// ---------------------------------------------------------------------------
function markdownToHtml(md: string): string {
  if (!md) return ""
  return DOMPurify.sanitize(
    marked.parse(md, { async: false, breaks: true, gfm: true }) as string,
  )
}

// ---------------------------------------------------------------------------
// Language list for code blocks
// ---------------------------------------------------------------------------
const LANGUAGES = [
  { value: "", label: "Auto detect" },
  { value: "javascript", label: "JavaScript" },
  { value: "typescript", label: "TypeScript" },
  { value: "go", label: "Go" },
  { value: "python", label: "Python" },
  { value: "bash", label: "Bash" },
  { value: "sql", label: "SQL" },
  { value: "json", label: "JSON" },
  { value: "yaml", label: "YAML" },
  { value: "html", label: "HTML" },
  { value: "css", label: "CSS" },
  { value: "rust", label: "Rust" },
  { value: "java", label: "Java" },
  { value: "ruby", label: "Ruby" },
  { value: "php", label: "PHP" },
  { value: "c", label: "C" },
  { value: "cpp", label: "C++" },
  { value: "markdown", label: "Markdown" },
] as const

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------
interface TiptapEditorProps {
  content: string
  onChange: (markdown: string) => void
  onBlur?: () => void
  placeholder?: string
  editable?: boolean
  compact?: boolean
  className?: string
  autoFocus?: boolean
}

// ---------------------------------------------------------------------------
// Slash command items
// ---------------------------------------------------------------------------
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

// ---------------------------------------------------------------------------
// Toolbar button
// ---------------------------------------------------------------------------
function ToolbarButton({
  active,
  disabled,
  onClick,
  children,
  title,
}: {
  active?: boolean
  disabled?: boolean
  onClick: () => void
  children: ReactNode
  title: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={cn(
        "p-1.5 rounded transition-colors",
        "hover:bg-white/[0.08] disabled:opacity-30 disabled:pointer-events-none",
        active
          ? "bg-blue-500/20 text-blue-400"
          : "text-muted-foreground/60 hover:text-muted-foreground",
      )}
    >
      {children}
    </button>
  )
}

// ---------------------------------------------------------------------------
// Bubble menu button
// ---------------------------------------------------------------------------
function BubbleButton({
  active,
  onClick,
  children,
  title,
}: {
  active?: boolean
  onClick: () => void
  children: ReactNode
  title?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      className={cn(
        "p-1 rounded transition-colors",
        "hover:bg-white/[0.12]",
        active ? "text-blue-400 bg-blue-500/20" : "text-white/70",
      )}
    >
      {children}
    </button>
  )
}

// ---------------------------------------------------------------------------
// Toolbar separator
// ---------------------------------------------------------------------------
function Sep() {
  return <div className="w-px h-4 bg-white/[0.06] mx-0.5 shrink-0" />
}

// ---------------------------------------------------------------------------
// Language selector for code blocks
// ---------------------------------------------------------------------------
function LanguageSelector({
  language,
  onSelect,
}: {
  language: string
  onSelect: (lang: string) => void
}) {
  const [open, setOpen] = useState(false)

  const current = LANGUAGES.find((l) => l.value === language)

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className={cn(
          "flex items-center gap-1 px-2 py-1 rounded text-[11px] font-mono",
          "bg-white/[0.06] border border-white/[0.1] hover:bg-white/[0.1] transition-colors",
          "text-muted-foreground hover:text-foreground",
        )}
      >
        <FileCode className="h-3 w-3" />
        {current?.label || "Auto detect"}
        <ChevronDown className="h-3 w-3 opacity-50" />
      </button>

      {open && (
        <>
          <div
            className="fixed inset-0 z-40"
            onClick={() => setOpen(false)}
          />
          <div
            className={cn(
              "absolute top-full left-0 mt-1 z-50",
              "bg-[#1a1a2e] border border-white/[0.1] rounded-lg shadow-xl",
              "max-h-[240px] overflow-y-auto py-1 w-[160px]",
              "scrollbar-thin scrollbar-thumb-white/10",
            )}
          >
            {LANGUAGES.map((lang) => (
              <button
                key={lang.value}
                type="button"
                onClick={() => {
                  onSelect(lang.value)
                  setOpen(false)
                }}
                className={cn(
                  "w-full text-left px-3 py-1.5 text-[11px] font-mono",
                  "hover:bg-white/[0.08] transition-colors",
                  lang.value === language
                    ? "text-blue-400 bg-blue-500/10"
                    : "text-muted-foreground",
                )}
              >
                {lang.label}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Code block NodeView
// ---------------------------------------------------------------------------
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function CodeBlockView({ node, updateAttributes }: any) {
  const lang = node.attrs.language || ""
  return (
    <NodeViewWrapper className="relative group my-3">
      <div className="absolute right-2 top-2 z-10 opacity-0 group-hover:opacity-100 transition-opacity">
        <LanguageSelector
          language={lang}
          onSelect={(l: string) => updateAttributes({ language: l })}
        />
      </div>
      {lang && (
        <div className="absolute right-2 top-2 text-[10px] font-mono text-muted-foreground/30 uppercase group-hover:opacity-0 transition-opacity pointer-events-none">
          {LANGUAGES.find((l) => l.value === lang)?.label || lang}
        </div>
      )}
      <pre
        className="bg-[#0d1117] border border-white/[0.08] rounded-lg p-4 pt-3 overflow-x-auto !my-0"
        spellCheck={false}
      >
        <NodeViewContent
          as="div"
          className={cn(
            "font-mono text-xs",
            lang ? `language-${lang} hljs` : "hljs",
          )}
        />
      </pre>
    </NodeViewWrapper>
  )
}

// ---------------------------------------------------------------------------
// Highlight color picker for bubble menu
// ---------------------------------------------------------------------------
const HIGHLIGHT_COLORS = [
  { label: "Yellow", value: "#fef08a" },
  { label: "Green", value: "#bbf7d0" },
  { label: "Blue", value: "#bfdbfe" },
  { label: "Pink", value: "#fbcfe8" },
  { label: "Orange", value: "#fed7aa" },
  { label: "Purple", value: "#ddd6fe" },
]

const TEXT_COLORS = [
  { label: "Default", value: "" },
  { label: "Red", value: "#f87171" },
  { label: "Orange", value: "#fb923c" },
  { label: "Yellow", value: "#facc15" },
  { label: "Green", value: "#4ade80" },
  { label: "Blue", value: "#60a5fa" },
  { label: "Purple", value: "#a78bfa" },
  { label: "Pink", value: "#f472b6" },
]

function ColorDropdown({
  colors,
  onSelect,
  onClose,
  label,
}: {
  colors: { label: string; value: string }[]
  onSelect: (color: string) => void
  onClose: () => void
  label: string
}) {
  return (
    <>
      <div className="fixed inset-0 z-40" onClick={onClose} />
      <div className="absolute bottom-full left-0 mb-1 z-50 bg-[#1a1a2e] border border-white/[0.12] rounded-lg shadow-2xl p-2">
        <div className="text-[10px] text-muted-foreground/50 mb-1.5 px-0.5">
          {label}
        </div>
        <div className="flex gap-1">
          {colors.map((color) => (
            <button
              key={color.label}
              type="button"
              title={color.label}
              onClick={() => {
                onSelect(color.value)
                onClose()
              }}
              className="w-5 h-5 rounded border border-white/[0.1] hover:scale-110 transition-transform"
              style={{
                backgroundColor: color.value || "transparent",
                ...(color.value === "" && {
                  background:
                    "linear-gradient(135deg, transparent 45%, #f87171 45%, #f87171 55%, transparent 55%)",
                }),
              }}
            />
          ))}
        </div>
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// Prose editor classes
// ---------------------------------------------------------------------------
function getEditorClasses(compact?: boolean) {
  return cn(
    "outline-none min-h-[60px] prose prose-invert max-w-none",
    // Focus
    "[&_.ProseMirror-focused]:outline-none",
    // Placeholder
    "[&_p.is-editor-empty:first-child::before]:text-muted-foreground/30",
    "[&_p.is-editor-empty:first-child::before]:content-[attr(data-placeholder)]",
    "[&_p.is-editor-empty:first-child::before]:float-left",
    "[&_p.is-editor-empty:first-child::before]:h-0",
    "[&_p.is-editor-empty:first-child::before]:pointer-events-none",
    // Headings
    "[&_h1]:text-lg [&_h1]:font-semibold [&_h1]:mb-2 [&_h1]:mt-3",
    "[&_h2]:text-base [&_h2]:font-semibold [&_h2]:mb-2 [&_h2]:mt-3",
    "[&_h3]:text-sm [&_h3]:font-semibold [&_h3]:mb-1 [&_h3]:mt-2",
    // Paragraphs
    "[&_p]:text-sm [&_p]:leading-relaxed [&_p]:mb-1.5 [&_p]:text-foreground/80",
    "[&_strong]:text-foreground [&_strong]:font-semibold",
    // Inline code
    "[&_code]:bg-emerald-500/10 [&_code]:text-emerald-300 [&_code]:px-1 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_code]:font-mono",
    // Code blocks
    "[&_pre]:relative [&_pre]:bg-[#0d1117] [&_pre]:border [&_pre]:border-white/[0.08] [&_pre]:rounded-lg [&_pre]:p-4 [&_pre]:pt-8 [&_pre]:my-3 [&_pre]:overflow-x-auto",
    "[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-xs [&_pre_code]:font-mono [&_pre_code]:text-[#c9d1d9]",
    // Blockquote
    "[&_blockquote]:border-l-2 [&_blockquote]:border-amber-500/40 [&_blockquote]:pl-3 [&_blockquote]:italic [&_blockquote]:text-foreground/60",
    // Lists
    "[&_ul]:pl-4 [&_ul]:mb-2 [&_ol]:pl-4 [&_ol]:mb-2",
    "[&_li]:mb-0.5 [&_li]:text-sm [&_li]:text-foreground/80",
    "[&_a]:text-blue-400 [&_a]:underline",
    // Tables
    "[&_table]:w-full [&_table]:text-xs [&_table]:my-2",
    "[&_th]:text-left [&_th]:font-semibold [&_th]:py-1.5 [&_th]:px-2 [&_th]:border [&_th]:border-white/[0.08] [&_th]:bg-white/[0.02]",
    "[&_td]:py-1.5 [&_td]:px-2 [&_td]:border [&_td]:border-white/[0.04]",
    // Horizontal rule
    "[&_hr]:border-white/[0.06] [&_hr]:my-3",
    // Task lists
    "[&_ul[data-type=taskList]]:list-none [&_ul[data-type=taskList]]:pl-0",
    "[&_li[data-type=taskItem]]:flex [&_li[data-type=taskItem]]:items-start [&_li[data-type=taskItem]]:gap-2",
    // Underline
    "[&_u]:underline [&_u]:underline-offset-2",
    // Images
    "[&_img]:max-w-full [&_img]:h-auto [&_img]:rounded-lg [&_img]:my-2",
    // Mark / highlight
    "[&_mark]:rounded [&_mark]:px-0.5 [&_mark]:py-0",
    // Text alignment
    "[&_.text-center]:text-center [&_.text-right]:text-right",
    // Compact mode
    compact && "[&_p]:text-xs [&_h1]:text-sm [&_h2]:text-sm [&_h3]:text-xs",
  )
}

// ---------------------------------------------------------------------------
// Main editor
// ---------------------------------------------------------------------------
export function TiptapEditor({
  content,
  onChange,
  onBlur,
  placeholder: placeholderText,
  editable = true,
  compact,
  className,
  autoFocus,
}: TiptapEditorProps) {
  const [showHighlightPicker, setShowHighlightPicker] = useState(false)
  const [showTextColorPicker, setShowTextColorPicker] = useState(false)
  const saveTimeoutRef = useRef<NodeJS.Timeout | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)

  // Bubble menu state
  const [bubbleMenuVisible, setBubbleMenuVisible] = useState(false)
  const [bubbleMenuPos, setBubbleMenuPos] = useState({ top: 0, left: 0 })
  const bubbleMenuRef = useRef<HTMLDivElement>(null)

  // Floating menu state (+ button on empty lines)
  const [floatingMenuVisible, setFloatingMenuVisible] = useState(false)
  const [floatingMenuPos, setFloatingMenuPos] = useState({ top: 0, left: 0 })

  // Cleanup save timeout
  useEffect(() => {
    return () => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current)
      }
    }
  }, [])

  const editor = useEditor({
    immediatelyRender: false,
    extensions: [
      StarterKit.configure({
        codeBlock: false, // replaced by CodeBlockLowlight
      }),
      Placeholder.configure({
        placeholder: ({ node }) => {
          if (node.type.name === "heading") return "Heading"
          if (node.type.name === "codeBlock") return "Code..."
          return placeholderText || "Type '/' for commands..."
        },
      }),
      Link.configure({
        openOnClick: false,
        autolink: true,
        HTMLAttributes: {
          class: "text-blue-400 underline underline-offset-2 cursor-pointer",
        },
      }),
      Underline,
      TaskList,
      TaskItem.configure({ nested: true }),
      Highlight.configure({ multicolor: true }),
      Table.configure({ resizable: false }),
      TableRow,
      TableCell,
      TableHeader,
      CodeBlockLowlight.configure({
        lowlight,
        defaultLanguage: null,
      }).extend({
        addNodeView() {
          return ReactNodeViewRenderer(CodeBlockView)
        },
      }),
      Image.configure({
        inline: false,
        allowBase64: false,
      }),
      // New extensions
      Typography,
      TextStyle,
      Color,
      TextAlign.configure({
        types: ["heading", "paragraph"],
      }),
      createSlashCommandsExtension(),
    ],
    content: markdownToHtml(content),
    editable,
    autofocus: autoFocus ? "end" : false,
    editorProps: {
      attributes: {
        class: getEditorClasses(compact),
      },
    },
    onUpdate: ({ editor: e }) => {
      const md = htmlToMarkdown(e.getHTML())
      onChange(md)

      // Debounced auto-save
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current)
      }
      saveTimeoutRef.current = setTimeout(() => {
        onBlur?.()
      }, 1500)
    },
    onBlur: () => {
      // Clear any pending debounce and save immediately
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current)
      }
      onBlur?.()
    },
    onSelectionUpdate: ({ editor: e }) => {
      // Update bubble menu position
      const { from, to } = e.state.selection
      const hasSelection = from !== to
      const inCodeBlock = e.isActive("codeBlock")

      if (hasSelection && !inCodeBlock && editable) {
        // Get the selection coordinates
        try {
          const coords = e.view.coordsAtPos(from)
          const endCoords = e.view.coordsAtPos(to)
          const containerEl = containerRef.current
          if (containerEl) {
            const containerRect = containerEl.getBoundingClientRect()
            const midX =
              (coords.left + endCoords.right) / 2 - containerRect.left
            const topY = coords.top - containerRect.top - 44
            setBubbleMenuPos({ top: topY, left: midX })
            setBubbleMenuVisible(true)
          }
        } catch {
          setBubbleMenuVisible(false)
        }
      } else {
        setBubbleMenuVisible(false)
      }

      // Update floating menu (show on empty paragraphs)
      const { $from } = e.state.selection
      const isEmptyParagraph =
        $from.parent.type.name === "paragraph" &&
        $from.parent.content.size === 0
      const showFloat =
        isEmptyParagraph &&
        !inCodeBlock &&
        !e.isActive("table") &&
        !hasSelection &&
        editable

      if (showFloat) {
        try {
          const coords = e.view.coordsAtPos(from)
          const containerEl = containerRef.current
          if (containerEl) {
            const containerRect = containerEl.getBoundingClientRect()
            setFloatingMenuPos({
              top: coords.top - containerRect.top,
              left: coords.left - containerRect.left - 36,
            })
            setFloatingMenuVisible(true)
          }
        } catch {
          setFloatingMenuVisible(false)
        }
      } else {
        setFloatingMenuVisible(false)
      }
    },
  })

  // Update content when prop changes externally
  useEffect(() => {
    if (editor && content !== htmlToMarkdown(editor.getHTML())) {
      editor.commands.setContent(markdownToHtml(content))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentionally omit editor to avoid infinite loops on editor state changes
  }, [content])

  const handleInsertLink = useCallback(() => {
    if (!editor) return
    const previousUrl = editor.getAttributes("link").href || ""
    const url = window.prompt("URL:", previousUrl)
    if (url === null) return
    if (url === "") {
      editor.chain().focus().extendMarkRange("link").unsetLink().run()
      return
    }
    editor
      .chain()
      .focus()
      .extendMarkRange("link")
      .setLink({ href: url })
      .run()
  }, [editor])

  const handleInsertImage = useCallback(() => {
    if (!editor) return
    const url = window.prompt("Image URL:")
    if (!url) return
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(editor.chain().focus() as any).setImage({ src: url }).run()
  }, [editor])

  if (!editor) return null

  const iconSize = compact ? "h-3 w-3" : "h-3.5 w-3.5"

  return (
    <div ref={containerRef} className={cn("relative", className)}>
      {/* Bubble menu (appears on text selection) */}
      {bubbleMenuVisible && editable && (
        <div
          ref={bubbleMenuRef}
          className="absolute z-50 animate-in fade-in-0 zoom-in-95 duration-150"
          style={{
            top: bubbleMenuPos.top,
            left: bubbleMenuPos.left,
            transform: "translateX(-50%)",
          }}
          onMouseDown={(e) => e.preventDefault()}
        >
          <div className="flex items-center gap-0.5 bg-[#1a1a2e] border border-white/[0.12] rounded-lg shadow-2xl p-1">
            <BubbleButton
              active={editor.isActive("bold")}
              onClick={() => editor.chain().focus().toggleBold().run()}
              title="Bold"
            >
              <Bold className="h-3.5 w-3.5" />
            </BubbleButton>
            <BubbleButton
              active={editor.isActive("italic")}
              onClick={() => editor.chain().focus().toggleItalic().run()}
              title="Italic"
            >
              <Italic className="h-3.5 w-3.5" />
            </BubbleButton>
            <BubbleButton
              active={editor.isActive("underline")}
              onClick={() => editor.chain().focus().toggleUnderline().run()}
              title="Underline"
            >
              <UnderlineIcon className="h-3.5 w-3.5" />
            </BubbleButton>
            <BubbleButton
              active={editor.isActive("strike")}
              onClick={() => editor.chain().focus().toggleStrike().run()}
              title="Strikethrough"
            >
              <Strikethrough className="h-3.5 w-3.5" />
            </BubbleButton>
            <BubbleButton
              active={editor.isActive("code")}
              onClick={() => editor.chain().focus().toggleCode().run()}
              title="Code"
            >
              <Code className="h-3.5 w-3.5" />
            </BubbleButton>

            <div className="w-px h-4 bg-white/[0.1] mx-0.5" />

            <BubbleButton
              active={editor.isActive("link")}
              onClick={handleInsertLink}
              title="Link"
            >
              <Link2 className="h-3.5 w-3.5" />
            </BubbleButton>

            {/* Highlight color */}
            <div className="relative">
              <BubbleButton
                active={editor.isActive("highlight")}
                onClick={() => {
                  setShowHighlightPicker(!showHighlightPicker)
                  setShowTextColorPicker(false)
                }}
                title="Highlight"
              >
                <Highlighter className="h-3.5 w-3.5" />
              </BubbleButton>
              {showHighlightPicker && (
                <ColorDropdown
                  label="Highlight"
                  colors={HIGHLIGHT_COLORS}
                  onSelect={(color) => {
                    if (color) {
                      editor
                        .chain()
                        .focus()
                        .toggleHighlight({ color })
                        .run()
                    } else {
                      editor.chain().focus().unsetHighlight().run()
                    }
                  }}
                  onClose={() => setShowHighlightPicker(false)}
                />
              )}
            </div>

            {/* Text color */}
            <div className="relative">
              <BubbleButton
                active={!!editor.getAttributes("textStyle").color}
                onClick={() => {
                  setShowTextColorPicker(!showTextColorPicker)
                  setShowHighlightPicker(false)
                }}
                title="Text color"
              >
                <Type className="h-3.5 w-3.5" />
              </BubbleButton>
              {showTextColorPicker && (
                <ColorDropdown
                  label="Text color"
                  colors={TEXT_COLORS}
                  onSelect={(color) => {
                    if (color) {
                      editor.chain().focus().setColor(color).run()
                    } else {
                      editor.chain().focus().unsetColor().run()
                    }
                  }}
                  onClose={() => setShowTextColorPicker(false)}
                />
              )}
            </div>
          </div>
        </div>
      )}

      {/* Floating menu (appears on empty lines) */}
      {floatingMenuVisible && editable && (
        <div
          className="absolute z-40 animate-in fade-in-0 duration-150"
          style={{
            top: floatingMenuPos.top,
            left: floatingMenuPos.left,
          }}
          onMouseDown={(e) => e.preventDefault()}
        >
          <div className="flex items-center gap-1 bg-[#1a1a2e]/90 border border-white/[0.1] rounded-lg shadow-xl p-1 backdrop-blur-sm">
            <button
              type="button"
              onClick={() =>
                editor.chain().focus().toggleHeading({ level: 2 }).run()
              }
              className="p-1 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-white/[0.08] transition-colors"
              title="Heading 2"
            >
              <Heading2 className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              onClick={() =>
                editor.chain().focus().toggleBulletList().run()
              }
              className="p-1 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-white/[0.08] transition-colors"
              title="Bullet list"
            >
              <List className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              onClick={() =>
                editor.chain().focus().toggleTaskList().run()
              }
              className="p-1 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-white/[0.08] transition-colors"
              title="Task list"
            >
              <CheckSquare className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              onClick={() =>
                editor.chain().focus().toggleCodeBlock().run()
              }
              className="p-1 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-white/[0.08] transition-colors"
              title="Code block"
            >
              <FileCode className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              onClick={() =>
                editor.chain().focus().toggleBlockquote().run()
              }
              className="p-1 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-white/[0.08] transition-colors"
              title="Blockquote"
            >
              <Quote className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              onClick={() =>
                editor
                  .chain()
                  .focus()
                  .insertTable({ rows: 3, cols: 3, withHeaderRow: true })
                  .run()
              }
              className="p-1 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-white/[0.08] transition-colors"
              title="Table"
            >
              <TableIcon className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      )}

      {/* Toolbar */}
      {editable && (
        <div className="flex items-center gap-0.5 pb-2 mb-2 border-b border-white/[0.06] flex-wrap">
          {/* Text formatting */}
          <ToolbarButton
            active={editor.isActive("bold")}
            onClick={() => editor.chain().focus().toggleBold().run()}
            title="Bold (Ctrl+B)"
          >
            <Bold className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("italic")}
            onClick={() => editor.chain().focus().toggleItalic().run()}
            title="Italic (Ctrl+I)"
          >
            <Italic className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("underline")}
            onClick={() => editor.chain().focus().toggleUnderline().run()}
            title="Underline (Ctrl+U)"
          >
            <UnderlineIcon className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("strike")}
            onClick={() => editor.chain().focus().toggleStrike().run()}
            title="Strikethrough"
          >
            <Strikethrough className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("code")}
            onClick={() => editor.chain().focus().toggleCode().run()}
            title="Inline code"
          >
            <Code className={iconSize} />
          </ToolbarButton>

          <Sep />

          {/* Headings */}
          <ToolbarButton
            active={editor.isActive("heading", { level: 1 })}
            onClick={() =>
              editor.chain().focus().toggleHeading({ level: 1 }).run()
            }
            title="Heading 1"
          >
            <Heading1 className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("heading", { level: 2 })}
            onClick={() =>
              editor.chain().focus().toggleHeading({ level: 2 }).run()
            }
            title="Heading 2"
          >
            <Heading2 className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("heading", { level: 3 })}
            onClick={() =>
              editor.chain().focus().toggleHeading({ level: 3 }).run()
            }
            title="Heading 3"
          >
            <Heading3 className={iconSize} />
          </ToolbarButton>

          <Sep />

          {/* Lists */}
          <ToolbarButton
            active={editor.isActive("bulletList")}
            onClick={() => editor.chain().focus().toggleBulletList().run()}
            title="Bullet list"
          >
            <List className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("orderedList")}
            onClick={() => editor.chain().focus().toggleOrderedList().run()}
            title="Ordered list"
          >
            <ListOrdered className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("taskList")}
            onClick={() => editor.chain().focus().toggleTaskList().run()}
            title="Task list"
          >
            <CheckSquare className={iconSize} />
          </ToolbarButton>

          <Sep />

          {/* Blocks & inserts */}
          <ToolbarButton
            active={editor.isActive("blockquote")}
            onClick={() => editor.chain().focus().toggleBlockquote().run()}
            title="Blockquote"
          >
            <Quote className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("codeBlock")}
            onClick={() => editor.chain().focus().toggleCodeBlock().run()}
            title="Code block"
          >
            <FileCode className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={false}
            onClick={() =>
              editor.chain().focus().setHorizontalRule().run()
            }
            title="Horizontal rule"
          >
            <Minus className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive("link")}
            onClick={handleInsertLink}
            title="Insert link"
          >
            <Link2 className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={false}
            onClick={() =>
              editor
                .chain()
                .focus()
                .insertTable({ rows: 3, cols: 3, withHeaderRow: true })
                .run()
            }
            title="Insert table"
          >
            <TableIcon className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={false}
            onClick={handleInsertImage}
            title="Insert image"
          >
            <ImagePlus className={iconSize} />
          </ToolbarButton>

          <Sep />

          {/* Text alignment */}
          <ToolbarButton
            active={editor.isActive({ textAlign: "left" })}
            onClick={() =>
              editor.chain().focus().setTextAlign("left").run()
            }
            title="Align left"
          >
            <AlignLeft className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive({ textAlign: "center" })}
            onClick={() =>
              editor.chain().focus().setTextAlign("center").run()
            }
            title="Align center"
          >
            <AlignCenter className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={editor.isActive({ textAlign: "right" })}
            onClick={() =>
              editor.chain().focus().setTextAlign("right").run()
            }
            title="Align right"
          >
            <AlignRight className={iconSize} />
          </ToolbarButton>

          <Sep />

          {/* History */}
          <ToolbarButton
            active={false}
            disabled={!editor.can().undo()}
            onClick={() => editor.chain().focus().undo().run()}
            title="Undo (Ctrl+Z)"
          >
            <Undo className={iconSize} />
          </ToolbarButton>
          <ToolbarButton
            active={false}
            disabled={!editor.can().redo()}
            onClick={() => editor.chain().focus().redo().run()}
            title="Redo (Ctrl+Shift+Z)"
          >
            <Redo className={iconSize} />
          </ToolbarButton>
        </div>
      )}

      {/* Editor content */}
      <EditorContent editor={editor} />
    </div>
  )
}
