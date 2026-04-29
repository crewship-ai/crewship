"use client"

import { useState } from "react"
import type { ReactNode } from "react"
import { NodeViewContent, NodeViewWrapper } from "@tiptap/react"
import { FileCode, ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"

// Toolbar primitives + code-block extras extracted from the main editor
// file for readability. All exports are stable; the main editor imports
// these by name.

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

export {
  ToolbarButton,
  BubbleButton,
  Sep,
  LanguageSelector,
  CodeBlockView,
  HIGHLIGHT_COLORS,
  TEXT_COLORS,
  ColorDropdown,
  LANGUAGES,
}
