"use client"

import { useEffect } from "react"
import { marked } from "marked"
import { useEditor, EditorContent } from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import Placeholder from "@tiptap/extension-placeholder"
import Link from "@tiptap/extension-link"
import TaskList from "@tiptap/extension-task-list"
import TaskItem from "@tiptap/extension-task-item"
import Highlight from "@tiptap/extension-highlight"
import { Table } from "@tiptap/extension-table"
import TableRow from "@tiptap/extension-table-row"
import TableCell from "@tiptap/extension-table-cell"
import TableHeader from "@tiptap/extension-table-header"
import CodeBlockLowlight from "@tiptap/extension-code-block-lowlight"
import { all, createLowlight } from "lowlight"
import "highlight.js/styles/github-dark.css"
import { cn } from "@/lib/utils"
import {
  Bold, Italic, Strikethrough, Code, Heading1, Heading2, Heading3,
  List, ListOrdered, CheckSquare, Quote, Minus, Link2, Table as TableIcon,
  Undo, Redo,
} from "lucide-react"

const lowlight = createLowlight(all)

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

// Convert markdown to HTML using `marked` library (handles tables, lists, code blocks correctly)
function markdownToHtml(md: string): string {
  if (!md) return ""
  return marked.parse(md, { async: false, breaks: true, gfm: true }) as string
}

// Convert tiptap HTML back to markdown
function htmlToMarkdown(html: string): string {
  if (!html) return ""
  let md = html
  // Remove wrapper tags
  md = md.replace(/<\/?(div|span)[^>]*>/g, "")
  // Headings
  md = md.replace(/<h1[^>]*>(.*?)<\/h1>/g, "# $1\n")
  md = md.replace(/<h2[^>]*>(.*?)<\/h2>/g, "## $1\n")
  md = md.replace(/<h3[^>]*>(.*?)<\/h3>/g, "### $1\n")
  // Bold/Italic
  md = md.replace(/<strong>(.*?)<\/strong>/g, "**$1**")
  md = md.replace(/<em>(.*?)<\/em>/g, "*$1*")
  // Code blocks
  md = md.replace(/<pre><code[^>]*>([\s\S]*?)<\/code><\/pre>/g, "```\n$1```\n")
  // Inline code
  md = md.replace(/<code>(.*?)<\/code>/g, "`$1`")
  // Links
  md = md.replace(/<a[^>]*href="([^"]*)"[^>]*>(.*?)<\/a>/g, "[$2]($1)")
  // Task lists
  md = md.replace(/<li data-type="taskItem" data-checked="true"[^>]*><p>(.*?)<\/p><\/li>/g, "- [x] $1")
  md = md.replace(/<li data-type="taskItem" data-checked="false"[^>]*><p>(.*?)<\/p><\/li>/g, "- [ ] $1")
  // Lists
  md = md.replace(/<li><p>(.*?)<\/p><\/li>/g, "- $1")
  md = md.replace(/<li>(.*?)<\/li>/g, "- $1")
  md = md.replace(/<\/?(ul|ol)[^>]*>/g, "")
  // Blockquote
  md = md.replace(/<blockquote><p>(.*?)<\/p><\/blockquote>/g, "> $1")
  // Paragraphs
  md = md.replace(/<p>(.*?)<\/p>/g, "$1\n")
  // Horizontal rule
  md = md.replace(/<hr[^>]*>/g, "---\n")
  // Tables
  md = md.replace(/<table>[\s\S]*?<\/table>/g, (table) => {
    const headers = (table.match(/<th>(.*?)<\/th>/g) || []).map(h => h.replace(/<\/?th>/g, ""))
    const rows = (table.match(/<tr>(?:(?!<thead)[\s\S])*?<\/tr>/g) || []).slice(1)
    if (headers.length === 0) return ""
    let result = "| " + headers.join(" | ") + " |\n"
    result += "| " + headers.map(() => "---").join(" | ") + " |\n"
    for (const row of rows) {
      const cells = (row.match(/<td>(.*?)<\/td>/g) || []).map(d => d.replace(/<\/?td>/g, ""))
      result += "| " + cells.join(" | ") + " |\n"
    }
    return result
  })
  // Cleanup
  md = md.replace(/<br\s*\/?>/g, "\n")
  md = md.replace(/<[^>]+>/g, "")
  md = md.replace(/&amp;/g, "&")
  md = md.replace(/&lt;/g, "<")
  md = md.replace(/&gt;/g, ">")
  md = md.replace(/&quot;/g, '"')
  md = md.replace(/\n{3,}/g, "\n\n")
  return md.trim()
}

function ToolbarButton({ active, onClick, children, title }: { active?: boolean; onClick: () => void; children: React.ReactNode; title: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      className={cn(
        "p-1 rounded hover:bg-white/[0.08] transition-colors",
        active ? "bg-white/[0.1] text-foreground" : "text-muted-foreground/60"
      )}
    >
      {children}
    </button>
  )
}

export function TiptapEditor({ content, onChange, onBlur, placeholder, editable = true, compact, className, autoFocus }: TiptapEditorProps) {
  const editor = useEditor({
    immediatelyRender: false,
    extensions: [
      StarterKit.configure({
        codeBlock: false, // use lowlight version instead
      }),
      Placeholder.configure({ placeholder: placeholder || "Write something..." }),
      Link.configure({ openOnClick: false, HTMLAttributes: { class: "text-blue-400 underline underline-offset-2 cursor-pointer" } }),
      TaskList,
      TaskItem.configure({ nested: true }),
      Highlight.configure({ multicolor: true }),
      Table.configure({ resizable: false }),
      TableRow,
      TableCell,
      TableHeader,
      CodeBlockLowlight.configure({ lowlight }),
    ],
    content: markdownToHtml(content),
    editable,
    autofocus: autoFocus ? "end" : false,
    editorProps: {
      attributes: {
        class: cn(
          "outline-none min-h-[60px] prose prose-invert max-w-none",
          "[&_h1]:text-lg [&_h1]:font-semibold [&_h1]:mb-2 [&_h1]:mt-3",
          "[&_h2]:text-base [&_h2]:font-semibold [&_h2]:mb-2 [&_h2]:mt-3",
          "[&_h3]:text-sm [&_h3]:font-semibold [&_h3]:mb-1 [&_h3]:mt-2",
          "[&_p]:text-sm [&_p]:leading-relaxed [&_p]:mb-1.5 [&_p]:text-foreground/80",
          "[&_strong]:text-foreground [&_strong]:font-semibold",
          "[&_code]:bg-emerald-500/10 [&_code]:text-emerald-300 [&_code]:px-1 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_code]:font-mono",
          "[&_pre]:bg-[#0d1117] [&_pre]:border [&_pre]:border-white/[0.08] [&_pre]:rounded-lg [&_pre]:p-4 [&_pre]:my-3 [&_pre]:overflow-x-auto",
          "[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-xs [&_pre_code]:font-mono",
          "[&_blockquote]:border-l-2 [&_blockquote]:border-amber-500/40 [&_blockquote]:pl-3 [&_blockquote]:italic [&_blockquote]:text-foreground/60",
          "[&_ul]:pl-4 [&_ul]:mb-2 [&_ol]:pl-4 [&_ol]:mb-2",
          "[&_li]:mb-0.5 [&_li]:text-sm [&_li]:text-foreground/80",
          "[&_a]:text-blue-400 [&_a]:underline",
          "[&_table]:w-full [&_table]:text-xs [&_table]:my-2",
          "[&_th]:text-left [&_th]:font-semibold [&_th]:py-1.5 [&_th]:px-2 [&_th]:border [&_th]:border-white/[0.08] [&_th]:bg-white/[0.02]",
          "[&_td]:py-1.5 [&_td]:px-2 [&_td]:border [&_td]:border-white/[0.04]",
          "[&_hr]:border-white/[0.06] [&_hr]:my-3",
          "[&_ul[data-type=taskList]]:list-none [&_ul[data-type=taskList]]:pl-0",
          "[&_li[data-type=taskItem]]:flex [&_li[data-type=taskItem]]:items-start [&_li[data-type=taskItem]]:gap-2",
          compact && "[&_p]:text-xs [&_h1]:text-sm [&_h2]:text-sm [&_h3]:text-xs",
        ),
      },
    },
    onUpdate: ({ editor: e }) => {
      onChange(htmlToMarkdown(e.getHTML()))
    },
    onBlur: () => {
      onBlur?.()
    },
  })

  // Update content when prop changes externally
  useEffect(() => {
    if (editor && content !== htmlToMarkdown(editor.getHTML())) {
      editor.commands.setContent(markdownToHtml(content))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [content])

  if (!editor) return null

  return (
    <div className={cn("relative", className)}>
      {/* Toolbar */}
      {editable && (
        <div className="flex items-center gap-0.5 pb-2 mb-2 border-b border-white/[0.06] flex-wrap">
          <ToolbarButton active={editor.isActive("bold")} onClick={() => editor.chain().focus().toggleBold().run()} title="Bold">
            <Bold className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("italic")} onClick={() => editor.chain().focus().toggleItalic().run()} title="Italic">
            <Italic className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("strike")} onClick={() => editor.chain().focus().toggleStrike().run()} title="Strikethrough">
            <Strikethrough className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("code")} onClick={() => editor.chain().focus().toggleCode().run()} title="Inline code">
            <Code className="h-3.5 w-3.5" />
          </ToolbarButton>
          <div className="w-px h-4 bg-white/[0.06] mx-1" />
          <ToolbarButton active={editor.isActive("heading", { level: 1 })} onClick={() => editor.chain().focus().toggleHeading({ level: 1 }).run()} title="Heading 1">
            <Heading1 className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("heading", { level: 2 })} onClick={() => editor.chain().focus().toggleHeading({ level: 2 }).run()} title="Heading 2">
            <Heading2 className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("heading", { level: 3 })} onClick={() => editor.chain().focus().toggleHeading({ level: 3 }).run()} title="Heading 3">
            <Heading3 className="h-3.5 w-3.5" />
          </ToolbarButton>
          <div className="w-px h-4 bg-white/[0.06] mx-1" />
          <ToolbarButton active={editor.isActive("bulletList")} onClick={() => editor.chain().focus().toggleBulletList().run()} title="Bullet list">
            <List className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("orderedList")} onClick={() => editor.chain().focus().toggleOrderedList().run()} title="Ordered list">
            <ListOrdered className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("taskList")} onClick={() => editor.chain().focus().toggleTaskList().run()} title="Task list">
            <CheckSquare className="h-3.5 w-3.5" />
          </ToolbarButton>
          <div className="w-px h-4 bg-white/[0.06] mx-1" />
          <ToolbarButton active={editor.isActive("blockquote")} onClick={() => editor.chain().focus().toggleBlockquote().run()} title="Blockquote">
            <Quote className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={editor.isActive("codeBlock")} onClick={() => editor.chain().focus().toggleCodeBlock().run()} title="Code block">
            <Code className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={false} onClick={() => editor.chain().focus().setHorizontalRule().run()} title="Horizontal rule">
            <Minus className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={false} onClick={() => { const url = window.prompt("URL:"); if (url) editor.chain().focus().setLink({ href: url }).run() }} title="Add link">
            <Link2 className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={false} onClick={() => editor.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()} title="Insert table">
            <TableIcon className="h-3.5 w-3.5" />
          </ToolbarButton>
          <div className="w-px h-4 bg-white/[0.06] mx-1" />
          <ToolbarButton active={false} onClick={() => editor.chain().focus().undo().run()} title="Undo">
            <Undo className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton active={false} onClick={() => editor.chain().focus().redo().run()} title="Redo">
            <Redo className="h-3.5 w-3.5" />
          </ToolbarButton>
        </div>
      )}

      <EditorContent editor={editor} />
    </div>
  )
}
