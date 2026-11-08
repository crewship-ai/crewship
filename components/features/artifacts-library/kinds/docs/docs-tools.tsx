"use client"

import * as React from "react"
import type { Editor } from "@tiptap/core"
import { useEditorState } from "@tiptap/react"
import { Code, Heading1, Heading2, Link2, List, ListOrdered, Pilcrow, Redo2, SquareCode, Table, Undo2, Unlink } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { normalizeDocumentLink } from "@/lib/documents/schema"
import { ArtifactSlot, ArtifactTool, ArtifactToolSeparator } from "../../frame"

/**
 * The Docs formatting bar in the `tools` slot, mounted only while the page is
 * editable. The same commands the document editor has always offered: undo
 * and redo of this editor's own changes, paragraph and headings, lists and a
 * table; schema 3 adds code and links. Nothing here inserts an embed.
 */
export function DocsTools({ editor, schema, onError }: { editor: Editor; schema: number; onError: (message: string | undefined) => void }) {
  const rich = schema === 3
  const active = useEditorState({ editor, selector: ({ editor }) => ({
    undo: editor.can().undo(), redo: editor.can().redo(),
    paragraph: editor.isActive("paragraph"), h1: editor.isActive("heading", { level: 1 }), h2: editor.isActive("heading", { level: 2 }),
    bullet: editor.isActive("bulletList"), ordered: editor.isActive("orderedList"),
    // Code and links exist only in schema 3.
    code: rich && editor.isActive("code"), codeBlock: rich && editor.isActive("codeBlock"), link: rich && editor.isActive("link"),
  }) })
  const run = () => editor.chain().focus()
  return <ArtifactSlot name="tools" label="Document formatting">
    <ArtifactTool icon={Undo2} label="Undo" disabled={!active.undo} onClick={() => run().undo().run()} />
    <ArtifactTool icon={Redo2} label="Redo" disabled={!active.redo} onClick={() => run().redo().run()} />
    <ArtifactToolSeparator />
    <ArtifactTool icon={Pilcrow} label="Paragraph" on={active.paragraph} onClick={() => run().setParagraph().run()} />
    <ArtifactTool icon={Heading1} label="Heading 1" on={active.h1} onClick={() => run().toggleHeading({ level: 1 }).run()} />
    <ArtifactTool icon={Heading2} label="Heading 2" on={active.h2} onClick={() => run().toggleHeading({ level: 2 }).run()} />
    <ArtifactToolSeparator />
    <ArtifactTool icon={List} label="Bullet list" on={active.bullet} onClick={() => run().toggleBulletList().run()} />
    <ArtifactTool icon={ListOrdered} label="Numbered list" on={active.ordered} onClick={() => run().toggleOrderedList().run()} />
    <ArtifactTool icon={Table} label="Table" onClick={() => run().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()} />
    {rich && <>
      <ArtifactToolSeparator />
      <ArtifactTool icon={Code} label="Inline code" on={active.code} onClick={() => run().toggleMark("code").run()} />
      <ArtifactTool icon={SquareCode} label="Code block" on={active.codeBlock}
        onClick={() => run().toggleNode("codeBlock", "paragraph", { id: editor.state.selection.$from.parent.attrs.id }).run()} />
      <LinkTool editor={editor} on={active.link} onError={onError} />
      <ArtifactTool icon={Unlink} label="Remove link" disabled={!active.link} onClick={() => run().unsetMark("link").run()} />
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
