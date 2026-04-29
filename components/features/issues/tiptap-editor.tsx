"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  EditorContent,
  ReactNodeViewRenderer,
  useEditor,
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
import { cn } from "@/lib/utils"
import {
  AlignCenter,
  AlignLeft,
  AlignRight,
  Bold,
  CheckSquare,
  Code,
  FileCode,
  Heading1,
  Heading2,
  Heading3,
  Highlighter,
  ImagePlus,
  Italic,
  Link2,
  List,
  ListOrdered,
  Minus,
  Quote,
  Redo,
  Strikethrough,
  Table as TableIcon,
  Type,
  Underline as UnderlineIcon,
  Undo,
} from "lucide-react"

import { htmlToMarkdown, lowlight, markdownToHtml } from "./tiptap-editor-markdown"
import {
  BubbleButton,
  CodeBlockView,
  ColorDropdown,
  HIGHLIGHT_COLORS,
  Sep,
  TEXT_COLORS,
  ToolbarButton,
} from "./tiptap-editor-toolbar"
import { createSlashCommandsExtension } from "./tiptap-editor-slash"

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
