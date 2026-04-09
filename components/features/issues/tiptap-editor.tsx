"use client"

import { useEffect, useState, useCallback } from "react"
import { marked } from "marked"
import { useEditor, EditorContent, NodeViewContent, NodeViewWrapper, ReactNodeViewRenderer } from "@tiptap/react"
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
} from "lucide-react"

// ---------------------------------------------------------------------------
// Lowlight setup
// ---------------------------------------------------------------------------
const lowlight = createLowlight(all)

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
// Markdown <-> HTML helpers
// ---------------------------------------------------------------------------
function markdownToHtml(md: string): string {
  if (!md) return ""
  return marked.parse(md, { async: false, breaks: true, gfm: true }) as string
}

function htmlToMarkdown(html: string): string {
  if (!html) return ""
  let md = html
  // Remove wrapper tags
  md = md.replace(/<\/?(div|span)[^>]*>/g, "")
  // Headings
  md = md.replace(/<h1[^>]*>(.*?)<\/h1>/g, "# $1\n")
  md = md.replace(/<h2[^>]*>(.*?)<\/h2>/g, "## $1\n")
  md = md.replace(/<h3[^>]*>(.*?)<\/h3>/g, "### $1\n")
  // Bold / Italic / Underline / Strikethrough
  md = md.replace(/<strong>(.*?)<\/strong>/g, "**$1**")
  md = md.replace(/<em>(.*?)<\/em>/g, "*$1*")
  md = md.replace(/<u>(.*?)<\/u>/g, "<u>$1</u>") // preserve underline as HTML in markdown
  md = md.replace(/<s>(.*?)<\/s>/g, "~~$1~~")
  // Code blocks (with optional language class)
  md = md.replace(/<pre><code class="language-([^"]+)">([\s\S]*?)<\/code><\/pre>/g, "```$1\n$2```\n")
  md = md.replace(/<pre><code[^>]*>([\s\S]*?)<\/code><\/pre>/g, "```\n$1```\n")
  // Inline code
  md = md.replace(/<code>(.*?)<\/code>/g, "`$1`")
  // Links
  md = md.replace(/<a[^>]*href="([^"]*)"[^>]*>(.*?)<\/a>/g, "[$2]($1)")
  // Images
  md = md.replace(/<img[^>]*src="([^"]*)"[^>]*alt="([^"]*)"[^>]*\/?>/g, "![$2]($1)")
  md = md.replace(/<img[^>]*src="([^"]*)"[^>]*\/?>/g, "![]($1)")
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
    const headers = (table.match(/<th>(.*?)<\/th>/g) || []).map((h) =>
      h.replace(/<\/?th>/g, ""),
    )
    const rows = (
      table.match(/<tr>(?:(?!<thead)[\s\S])*?<\/tr>/g) || []
    ).slice(1)
    if (headers.length === 0) return ""
    let result = "| " + headers.join(" | ") + " |\n"
    result += "| " + headers.map(() => "---").join(" | ") + " |\n"
    for (const row of rows) {
      const cells = (row.match(/<td>(.*?)<\/td>/g) || []).map((d) =>
        d.replace(/<\/?td>/g, ""),
      )
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
  children: React.ReactNode
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
// Toolbar separator
// ---------------------------------------------------------------------------
function Sep() {
  return <div className="w-px h-4 bg-white/[0.06] mx-0.5 shrink-0" />
}

// ---------------------------------------------------------------------------
// Code block language selector (appears in toolbar when cursor is in a code block)
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
          {/* Backdrop to close on click outside */}
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
// Code block NodeView — renders language selector directly on the code block
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
      <pre className="bg-[#0d1117] border border-white/[0.08] rounded-lg p-4 pt-3 overflow-x-auto !my-0" spellCheck={false}>
        <NodeViewContent as="div" className={cn("font-mono text-xs", lang ? `language-${lang} hljs` : "hljs")} />
      </pre>
    </NodeViewWrapper>
  )
}

// ---------------------------------------------------------------------------
// Main editor
// ---------------------------------------------------------------------------
export function TiptapEditor({
  content,
  onChange,
  onBlur,
  placeholder,
  editable = true,
  compact,
  className,
  autoFocus,
}: TiptapEditorProps) {
  const [isInCodeBlock, setIsInCodeBlock] = useState(false)
  const [codeBlockLanguage, setCodeBlockLanguage] = useState("")

  const editor = useEditor({
    immediatelyRender: false,
    extensions: [
      StarterKit.configure({
        codeBlock: false, // replaced by CodeBlockLowlight
      }),
      Placeholder.configure({
        placeholder: placeholder || "Write something...",
      }),
      Link.configure({
        openOnClick: false,
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
    ],
    content: markdownToHtml(content),
    editable,
    autofocus: autoFocus ? "end" : false,
    editorProps: {
      attributes: {
        class: cn(
          "outline-none min-h-[60px] prose prose-invert max-w-none",
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
          // Language badge on code blocks via CSS attribute selector
          "[&_pre]:before:absolute [&_pre]:before:right-3 [&_pre]:before:top-2 [&_pre]:before:text-[10px] [&_pre]:before:text-muted-foreground/30 [&_pre]:before:uppercase [&_pre]:before:font-mono [&_pre]:before:tracking-wider",
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
          // Compact mode overrides
          compact &&
            "[&_p]:text-xs [&_h1]:text-sm [&_h2]:text-sm [&_h3]:text-xs",
        ),
      },
    },
    onUpdate: ({ editor: e }) => {
      onChange(htmlToMarkdown(e.getHTML()))
    },
    onBlur: () => {
      onBlur?.()
    },
    onSelectionUpdate: ({ editor: e }) => {
      const inCodeBlock = e.isActive("codeBlock")
      setIsInCodeBlock(inCodeBlock)
      if (inCodeBlock) {
        const { language } = e.getAttributes("codeBlock")
        setCodeBlockLanguage(language || "")
      }
    },
  })

  // Keep language badge text in sync by setting a data-language attribute on <pre> elements
  useEffect(() => {
    if (!editor) return

    const updateLanguageLabels = () => {
      const el = editor.view.dom
      const pres = el.querySelectorAll("pre")
      pres.forEach((pre) => {
        const code = pre.querySelector("code")
        if (!code) return
        const classes = Array.from(code.classList)
        const langClass = classes.find((c) => c.startsWith("language-"))
        const lang = langClass ? langClass.replace("language-", "") : ""
        if (lang) {
          pre.setAttribute("data-language", lang)
          pre.style.setProperty("--lang-label", `"${lang}"`)
        } else {
          pre.removeAttribute("data-language")
          pre.style.removeProperty("--lang-label")
        }
      })
    }

    // Run once initially and on every update
    updateLanguageLabels()
    editor.on("update", updateLanguageLabels)
    return () => {
      editor.off("update", updateLanguageLabels)
    }
  }, [editor])

  // Update content when prop changes externally
  useEffect(() => {
    if (editor && content !== htmlToMarkdown(editor.getHTML())) {
      editor.commands.setContent(markdownToHtml(content))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [content])

  const handleLanguageChange = useCallback(
    (lang: string) => {
      if (!editor) return
      // Update the code block's language attribute
      editor.chain().focus().updateAttributes("codeBlock", { language: lang || null }).run()
    },
    [editor],
  )

  const handleInsertLink = useCallback(() => {
    if (!editor) return
    const previousUrl = editor.getAttributes("link").href || ""
    const url = window.prompt("URL:", previousUrl)
    if (url === null) return // cancelled
    if (url === "") {
      editor.chain().focus().extendMarkRange("link").unsetLink().run()
      return
    }
    editor.chain().focus().extendMarkRange("link").setLink({ href: url }).run()
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
    <div className={cn("relative", className)}>
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

          {/* Blocks */}
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
            onClick={() => editor.chain().focus().setHorizontalRule().run()}
            title="Horizontal rule"
          >
            <Minus className={iconSize} />
          </ToolbarButton>

          <Sep />

          {/* Insert */}
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

      {/* Editor content with language badge styling */}
      <style jsx global>{`
        .ProseMirror pre[data-language]::before {
          content: attr(data-language);
          position: absolute;
          right: 0.75rem;
          top: 0.5rem;
          font-size: 10px;
          font-family: var(--font-mono, ui-monospace, monospace);
          text-transform: uppercase;
          letter-spacing: 0.05em;
          color: hsl(var(--muted-foreground) / 0.3);
          pointer-events: none;
        }
        .ProseMirror pre:not([data-language])  {
          padding-top: 1rem !important;
        }
      `}</style>

      <EditorContent editor={editor} />
    </div>
  )
}
