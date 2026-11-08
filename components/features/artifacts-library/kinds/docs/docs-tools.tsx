"use client"

import * as React from "react"
import type { Editor } from "@tiptap/core"
import { useEditorState } from "@tiptap/react"
import { Bold, Braces, Code, Heading1, Heading2, Italic, Link2, List, ListChecks, ListOrdered, MessageSquarePlus, Pilcrow, Quote, Redo2, SquareCode, Strikethrough, Table, Undo2, Unlink } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { documentSchema, normalizeDocumentLink } from "@/lib/documents/schema"
import { isFormattedSchema, isRichSchema } from "@/components/features/pages/document-rich-extensions"
import { ArtifactSlot, ArtifactTool, ArtifactToolSeparator } from "../../frame"

/**
 * The Docs formatting bar in the `tools` slot, mounted only while the page is
 * editable. The same commands the document editor has always offered: undo
 * and redo of this editor's own changes, paragraph and headings, lists and a
 * table; schema 3 adds code and links; schema 4 adds bold, italic, strike,
 * quotes, checklists and code block languages. Nothing here inserts an embed.
 * `onComment` (only while this person may comment) starts a thread on the
 * selected text in the Comments drawer.
 */
export function DocsTools({ editor, schema, onError, onComment }: { editor: Editor; schema: number; onError: (message: string | undefined) => void; onComment?: () => void }) {
  const rich = isRichSchema(schema), formatted = isFormattedSchema(schema)
  const active = useEditorState({ editor, selector: ({ editor }) => ({
    undo: editor.can().undo(), redo: editor.can().redo(),
    paragraph: editor.isActive("paragraph"), h1: editor.isActive("heading", { level: 1 }), h2: editor.isActive("heading", { level: 2 }),
    bullet: editor.isActive("bulletList"), ordered: editor.isActive("orderedList"),
    // Code and links exist only in schema 3.
    code: rich && editor.isActive("code"), codeBlock: rich && editor.isActive("codeBlock"), link: rich && editor.isActive("link"),
    // Emphasis, quotes, checklists and code languages exist only in schema 4.
    bold: formatted && editor.isActive("bold"), italic: formatted && editor.isActive("italic"), strike: formatted && editor.isActive("strike"),
    quote: formatted && editor.isActive("blockquote"), tasks: formatted && editor.isActive("taskList"),
    language: formatted && editor.isActive("codeBlock") ? (editor.getAttributes("codeBlock").language as string | null | undefined) ?? "" : null,
    selection: !editor.state.selection.empty,
  }) })
  const run = () => editor.chain().focus()
  return <ArtifactSlot name="tools" label="Document formatting">
    <ArtifactTool icon={Undo2} label="Undo" disabled={!active.undo} onClick={() => run().undo().run()} />
    <ArtifactTool icon={Redo2} label="Redo" disabled={!active.redo} onClick={() => run().redo().run()} />
    <ArtifactToolSeparator />
    <ArtifactTool icon={Pilcrow} label="Paragraph" on={active.paragraph} onClick={() => run().setParagraph().run()} />
    <ArtifactTool icon={Heading1} label="Heading 1" on={active.h1} onClick={() => run().toggleHeading({ level: 1 }).run()} />
    <ArtifactTool icon={Heading2} label="Heading 2" on={active.h2} onClick={() => run().toggleHeading({ level: 2 }).run()} />
    {formatted && <>
      <ArtifactToolSeparator />
      <ArtifactTool icon={Bold} label="Bold" on={active.bold} onClick={() => run().toggleMark("bold").run()} />
      <ArtifactTool icon={Italic} label="Italic" on={active.italic} onClick={() => run().toggleMark("italic").run()} />
      <ArtifactTool icon={Strikethrough} label="Strikethrough" on={active.strike} onClick={() => run().toggleMark("strike").run()} />
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

/** The code block's language label: lowercase letters, digits, +, # and -, or none. */
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
        <Input aria-label="Code language" value={value} maxLength={32} onChange={event => setValue(event.target.value)} placeholder="python" autoFocus />
        <Button type="submit" size="sm" className="justify-self-end coarse:min-h-[44px]">Apply language</Button>
      </form>
    </PopoverContent>
  </Popover>
}
