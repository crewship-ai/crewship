"use client"

import * as React from "react"
import type { Editor } from "@tiptap/core"
import { useEditorState } from "@tiptap/react"
import { Baseline, Bold, Braces, Check, Code, Heading1, Heading2, Heading3, Highlighter, Italic, Link2, List, ListChecks, ListOrdered, MessageSquarePlus, Pilcrow, Quote, Redo2, RemoveFormatting, SquareCode, Strikethrough, Table, Underline, Undo2, Unlink } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { documentSchema, normalizeDocumentLink } from "@/lib/documents/schema"
import { clearFormatting, colorState, isFormattedSchema, isRichSchema, isStyledSchema, markState } from "@/components/features/pages/document-rich-extensions"
import { SUGGESTED_LANGUAGES } from "@/components/features/pages/document-code-blocks"
import { cn } from "@/lib/utils"
import { ArtifactSlot, ArtifactTool, ArtifactToolSeparator } from "../../frame"

/**
 * The Docs formatting bar in the `tools` slot, mounted only while the page is
 * editable. The same commands the document editor has always offered: undo
 * and redo of this editor's own changes, paragraph and headings, lists and a
 * table; schema 3 adds code and links; schema 4 adds bold, italic, strike,
 * quotes, checklists and code block languages; schema 5 adds underline, text
 * colour and highlight from the palette, and Clear formatting. Mark toggles
 * show "mixed" when only part of the selection has the mark. Menus keep the
 * selection (KeptSelection shows it while the editor is not focused) and
 * every command returns focus to it. Nothing here inserts an embed.
 * `onComment` (only while this person may comment) starts a thread on the
 * selected text in the Comments drawer.
 */
export function DocsTools({ editor, schema, onError, onComment }: { editor: Editor; schema: number; onError: (message: string | undefined) => void; onComment?: () => void }) {
  const rich = isRichSchema(schema), formatted = isFormattedSchema(schema), styled = isStyledSchema(schema)
  const active = useEditorState({ editor, selector: ({ editor }) => ({
    undo: editor.can().undo(), redo: editor.can().redo(),
    paragraph: editor.isActive("paragraph"), h1: editor.isActive("heading", { level: 1 }), h2: editor.isActive("heading", { level: 2 }), h3: editor.isActive("heading", { level: 3 }),
    bullet: editor.isActive("bulletList"), ordered: editor.isActive("orderedList"),
    // Code and links exist only in schema 3.
    code: rich && editor.isActive("code"), codeBlock: rich && editor.isActive("codeBlock"), link: rich && editor.isActive("link"),
    // Emphasis, quotes, checklists and code languages exist only in schema 4.
    bold: formatted && markState(editor, "bold"), italic: formatted && markState(editor, "italic"), strike: formatted && markState(editor, "strike"),
    // Underline, colour and highlight exist only in schema 5.
    underline: styled && markState(editor, "underline"),
    textColor: styled ? colorState(editor, "textColor") : null, highlight: styled ? colorState(editor, "highlight") : null,
    quote: formatted && editor.isActive("blockquote"), tasks: formatted && editor.isActive("taskList"),
    language: formatted && editor.isActive("codeBlock") ? (editor.getAttributes("codeBlock").language as string | null | undefined) ?? "" : null,
    selection: !editor.state.selection.empty,
  }) })
  const run = () => editor.chain().focus()
  const toggle = (name: string, state: boolean | "mixed") => state === true ? run().unsetMark(name).run() : run().setMark(name).run()
  return <ArtifactSlot name="tools" label="Document formatting">
    <ArtifactTool icon={Undo2} label="Undo" shortcut="Mod+Z" disabled={!active.undo} onClick={() => run().undo().run()} />
    <ArtifactTool icon={Redo2} label="Redo" shortcut="Mod+Shift+Z" disabled={!active.redo} onClick={() => run().redo().run()} />
    <ArtifactToolSeparator />
    <ArtifactTool icon={Pilcrow} label="Paragraph" on={active.paragraph} onClick={() => run().setParagraph().run()} />
    <ArtifactTool icon={Heading1} label="Heading 1" on={active.h1} onClick={() => run().toggleHeading({ level: 1 }).run()} />
    <ArtifactTool icon={Heading2} label="Heading 2" on={active.h2} onClick={() => run().toggleHeading({ level: 2 }).run()} />
    <ArtifactTool icon={Heading3} label="Heading 3" on={active.h3} onClick={() => run().toggleHeading({ level: 3 }).run()} />
    {formatted && <>
      <ArtifactToolSeparator />
      {/* A mixed selection becomes fully marked; a fully marked one is cleared, as in other editors. */}
      <ArtifactTool icon={Bold} label="Bold" shortcut="Mod+B" on={active.bold} onClick={() => toggle("bold", active.bold)} />
      <ArtifactTool icon={Italic} label="Italic" shortcut="Mod+I" on={active.italic} onClick={() => toggle("italic", active.italic)} />
      {styled && <ArtifactTool icon={Underline} label="Underline" shortcut="Mod+U" on={active.underline} onClick={() => toggle("underline", active.underline)} />}
      <ArtifactTool icon={Strikethrough} label="Strikethrough" shortcut="Mod+Shift+S" on={active.strike} onClick={() => toggle("strike", active.strike)} />
      {styled && <>
        <ColorTool editor={editor} mark="textColor" value={active.textColor} />
        <ColorTool editor={editor} mark="highlight" value={active.highlight} />
      </>}
      <ArtifactTool icon={RemoveFormatting} label="Clear formatting" shortcut={"Mod+\\"} disabled={!active.selection} onClick={() => clearFormatting(editor, run())} />
    </>}
    <ArtifactToolSeparator />
    <ArtifactTool icon={List} label="Bullet list" on={active.bullet} onClick={() => run().toggleBulletList().run()} />
    <ArtifactTool icon={ListOrdered} label="Numbered list" on={active.ordered} onClick={() => run().toggleOrderedList().run()} />
    {formatted && <>
      <ArtifactTool icon={ListChecks} label="Checklist" on={active.tasks} onClick={() => run().toggleList("taskList", "taskItem").run()} />
      <ArtifactTool icon={Quote} label="Quote" on={active.quote} onClick={() => run().toggleWrap("blockquote").run()} />
    </>}
    <ArtifactTool icon={Table} label="Table" onClick={() => run().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()} />
    {rich && <>
      <ArtifactToolSeparator />
      <ArtifactTool icon={Code} label="Inline code" on={active.code} onClick={() => run().toggleMark("code").run()} />
      <ArtifactTool icon={SquareCode} label="Code block" on={active.codeBlock}
        onClick={() => run().toggleNode("codeBlock", "paragraph", { id: editor.state.selection.$from.parent.attrs.id }).run()} />
      <LinkTool editor={editor} on={active.link} onError={onError} />
      <ArtifactTool icon={Unlink} label="Remove link" disabled={!active.link} onClick={() => run().unsetMark("link").run()} />
      {formatted && active.language !== null && <LanguageTool editor={editor} language={active.language} onError={onError} />}
    </>}
    {onComment && <>
      <ArtifactToolSeparator />
      <ArtifactTool icon={MessageSquarePlus} label="Comment" disabled={!active.selection} onClick={onComment} />
    </>}
  </ArtifactSlot>
}

/** A link address for the selection; only HTTP or HTTPS without credentials is applied. */
function LinkTool({ editor, on, onError }: { editor: Editor; on: boolean; onError: (message: string | undefined) => void }) {
  const [open, setOpen] = React.useState(false)
  const [address, setAddress] = React.useState("")
  const apply = () => {
    const href = normalizeDocumentLink(address)
    if (!href) { onError("Enter an HTTP or HTTPS link without credentials."); return }
    editor.chain().focus().setMark("link", { href }).run()
    onError(undefined); setOpen(false); setAddress("")
  }
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild><ArtifactTool icon={Link2} label="Link" on={on || open} /></PopoverTrigger>
    <PopoverContent side="top" className="grid w-72 gap-2 p-3">
      <form className="grid gap-2" onSubmit={event => { event.preventDefault(); apply() }}>
        <Input aria-label="Link address" value={address} onChange={event => setAddress(event.target.value)} placeholder="https://example.com" autoFocus />
        <Button type="submit" size="sm" className="justify-self-end coarse:min-h-[44px]">Apply link</Button>
      </form>
    </PopoverContent>
  </Popover>
}

/** The code block's language: lowercase letters, digits, +, # and -, or none. Known languages are coloured; suggestions list common ones. */
function LanguageTool({ editor, language, onError }: { editor: Editor; language: string; onError: (message: string | undefined) => void }) {
  const [open, setOpen] = React.useState(false)
  const [value, setValue] = React.useState(language)
  React.useEffect(() => { if (open) setValue(language) }, [open, language])
  const apply = () => {
    const next = value.trim().toLowerCase()
    if (next && !documentSchema.validLanguage(next)) { onError("Use a short language name such as python, go or c++."); return }
    editor.chain().focus().updateAttributes("codeBlock", { language: next || null }).run()
    onError(undefined); setOpen(false)
  }
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild><ArtifactTool icon={Braces} label="Code language" text={language || undefined} on={open} /></PopoverTrigger>
    <PopoverContent side="top" className="grid w-64 gap-2 p-3">
      <form className="grid gap-2" onSubmit={event => { event.preventDefault(); apply() }}>
        <Input aria-label="Code language" value={value} maxLength={32} onChange={event => setValue(event.target.value)} placeholder="python" list="docs-code-languages" autoComplete="off" autoFocus />
        <datalist id="docs-code-languages">{SUGGESTED_LANGUAGES.map(name => <option key={name} value={name} />)}</datalist>
        <Button type="submit" size="sm" className="justify-self-end coarse:min-h-[44px]">Apply language</Button>
      </form>
    </PopoverContent>
  </Popover>
}

const COLOR_LABEL: Record<string, string> = { gray: "Gray", red: "Red", orange: "Orange", yellow: "Yellow", green: "Green", blue: "Blue", purple: "Purple", pink: "Pink" }

/**
 * Text colour or highlight for the selection, from the schema's closed
 * palette. The swatches show this theme's shade of each name; Default
 * removes the mark. The popover keeps the editor's selection and gives focus
 * back to it when a colour is applied.
 */
function ColorTool({ editor, mark, value }: { editor: Editor; mark: "textColor" | "highlight"; value: string | null | "mixed" }) {
  const [open, setOpen] = React.useState(false)
  const highlight = mark === "highlight", label = highlight ? "Highlight" : "Text colour"
  const apply = (color: string | null) => {
    const chain = editor.chain().focus()
    if (color) chain.setMark(mark, { color }).run(); else chain.unsetMark(mark).run()
    setOpen(false)
  }
  const current = value === "mixed" ? "Mixed" : value ? COLOR_LABEL[value] : "Default"
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild>
      <ArtifactTool icon={highlight ? Highlighter : Baseline} label={label} title={`${label}: ${current}`} on={value === "mixed" ? "mixed" : Boolean(value) || open} aria-haspopup="dialog">
        {/* The current colour, so the bar shows what the selection has without opening the menu. */}
        <span aria-hidden className={cn("pointer-events-none absolute inset-x-[7px] bottom-[4px] h-[3px] rounded-full", !value && "hidden", value === "mixed" && "bg-[linear-gradient(90deg,var(--doc-text-red),var(--doc-text-blue),var(--doc-text-green))]")}
          style={value && value !== "mixed" ? { background: `var(--doc-${highlight ? "mark" : "text"}-${value})` } : undefined} />
      </ArtifactTool>
    </PopoverTrigger>
    <PopoverContent side="top" className="w-auto p-2" onOpenAutoFocus={event => event.preventDefault()} onCloseAutoFocus={event => { event.preventDefault(); editor.commands.focus() }}>
      <div role="group" aria-label={label} className="grid grid-cols-[repeat(3,auto)] gap-1 sm:grid-cols-[repeat(9,auto)]">
        <button type="button" aria-label="Default" aria-pressed={value === null} onClick={() => apply(null)}
          className="inline-flex h-8 min-w-8 items-center justify-center rounded-[var(--radius-row)] border border-border px-2 text-xs text-foreground hover:bg-accent coarse:min-h-[44px] coarse:min-w-[44px]">
          {value === null ? <Check aria-hidden className="size-3.5" /> : "A"}<span className="sr-only">Default</span>
        </button>
        {documentSchema.PALETTE.map(name => <button key={name} type="button" aria-label={COLOR_LABEL[name]} title={COLOR_LABEL[name]} aria-pressed={value === name} onClick={() => apply(name)}
          className="inline-flex size-8 items-center justify-center rounded-[var(--radius-row)] border border-border text-sm font-semibold hover:ring-2 hover:ring-ring/40 coarse:size-11"
          style={highlight ? { background: `var(--doc-mark-${name})`, color: "var(--foreground)" } : { color: `var(--doc-text-${name})` }}>
          {value === name ? <Check aria-hidden className="size-4" /> : <span aria-hidden>A</span>}
        </button>)}
      </div>
    </PopoverContent>
  </Popover>
}
