"use client"

import * as React from "react"
import type * as Y from "yjs"
import { ArrowDown, ArrowUp, IndentDecrease, IndentIncrease, Pencil, Plus, Trash2 } from "lucide-react"
import { ArtifactTool } from "../../frame"
import { cn } from "@/lib/utils"
import { documentTabs, newTabId, readTabRows, type DocumentTabRow } from "@/lib/documents/tabs"

const MESSAGES: Record<string, string> = {
  "tab limit": "This document has as many tabs as it can hold.",
  "tab history limit": "This document cannot take more tab changes.",
  "tab nesting limit": "Tabs nest at most 8 levels deep.",
  "invalid tab name": "Give the tab a name of at most 120 characters.",
  "a tab cannot move into itself": "A tab cannot move into its own sub-tabs.",
  "the first tab cannot be deleted": "The first tab cannot be deleted.",
}
const message = (cause: unknown) => MESSAGES[cause instanceof Error ? cause.message : ""] ?? "That tab change could not be made."

type Rows = { rows: DocumentTabRow[]; unreadable: boolean }
type Untyped = { _length: number }

/**
 * The tab tree of a document, re-read only when it can change: a tab map
 * changed, a root was added, or a tab's page went from empty to written or
 * back (an empty tab root is not shown). Typing inside a tab does not re-read.
 */
export function useTabRows(doc: Y.Doc, enabled: boolean): Rows {
  const read = React.useCallback((): Rows & { doc: Y.Doc } => ({ ...(enabled ? readTabRows(doc) : { rows: [], unreadable: false }), doc }), [doc, enabled])
  const [state, setState] = React.useState(read)
  React.useEffect(() => {
    if (!enabled) { setState(read()); return }
    const shape = () => [...doc.share].filter(([name]) => name.startsWith(documentTabs.PREFIX)).map(([name, type]) => `${name}:${(type as unknown as Untyped)._length > 0}`).join(",")
    let key = "", roots = shape()
    const update = () => { const next = read(), nextKey = JSON.stringify(next); if (nextKey !== key) { key = nextKey; setState(next) } }
    const listener = (transaction: Y.Transaction) => {
      const maps = [doc.share.get(documentTabs.TABS), doc.share.get(documentTabs.NAMES)]
      const metadata = [...transaction.changed.keys()].some(type => maps.includes(type))
      const now = shape()
      if (metadata || now !== roots) { roots = now; update() }
    }
    update()
    doc.on("afterTransaction", listener)
    return () => { doc.off("afterTransaction", listener) }
  }, [doc, enabled, read])
  // Rows read from a previous document are never used for this one.
  return state.doc === doc ? { rows: state.rows, unreadable: state.unreadable } : { rows: [], unreadable: false }
}

/**
 * Docs tabs: the tab list above the Outline. Choosing a tab shows its page;
 * people who can edit add, rename, reorder, nest and delete tabs. Every change
 * is tab metadata only (the model never moves content), written with this
 * session's origin so it is saved like any other edit. `selected` is the tab
 * the editor shows (see `shownTab`), never one it does not.
 */
export function DocsTabs({ doc, tabs, selected, editable, origin, onSelect }: {
  doc: Y.Doc
  tabs: Rows
  selected: string
  editable: boolean
  origin: string
  onSelect: (id: string) => void
}) {
  const { rows, unreadable } = tabs
  const [renaming, setRenaming] = React.useState<string | null>(null)
  const [confirming, setConfirming] = React.useState<string | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const current = rows.find(row => row.id === selected) ?? rows[0]
  const canEdit = editable && !unreadable
  React.useEffect(() => { if (!canEdit) { setRenaming(null); setConfirming(null) } }, [canEdit])

  const run = (change: () => void) => {
    if (!canEdit) return false
    try { change(); setError(null); return true } catch (cause) { setError(message(cause)); return false }
  }
  const add = (parent: string | null) => {
    const id = newTabId("t")
    if (run(() => documentTabs.createTab(doc, { id, name: "Untitled tab", parent, blockId: newTabId("b") }, origin))) { onSelect(id); setRenaming(id) }
  }
  const rename = (id: string, name: string) => { if (run(() => documentTabs.renameTab(doc, id, name.trim(), origin))) setRenaming(null) }
  const move = (row: DocumentTabRow, step: -1 | 1) => {
    const index = row.siblings.indexOf(row.id), target = index + step
    if (target < 0 || target >= row.siblings.length) return
    // The model leaves the moving tab out of its siblings: up goes before the sibling above, down before the one after the next.
    const before = step < 0 ? row.siblings[target] : row.siblings[target + 1] ?? null
    run(() => documentTabs.moveTab(doc, row.id, { parent: row.parent, before }, origin))
  }
  const indent = (row: DocumentTabRow) => {
    const index = row.siblings.indexOf(row.id)
    if (index > 0) run(() => documentTabs.moveTab(doc, row.id, { parent: row.siblings[index - 1], before: null }, origin))
  }
  const outdent = (row: DocumentTabRow) => {
    const parent = rows.find(candidate => candidate.id === row.parent)
    if (!parent) return
    const after = parent.siblings[parent.siblings.indexOf(parent.id) + 1] ?? null
    run(() => documentTabs.moveTab(doc, row.id, { parent: parent.parent, before: after }, origin))
  }
  const remove = (id: string) => {
    const others = rows.filter(row => row.id !== id)
    const index = rows.findIndex(row => row.id === id)
    const next = others[Math.max(0, index - 1)]?.id ?? documentTabs.DEFAULT
    if (run(() => documentTabs.deleteTab(doc, id, origin))) { setConfirming(null); onSelect(next) }
  }

  const index = current ? current.siblings.indexOf(current.id) : -1
  return <div data-docs-tabs className="grid gap-1 border-b p-1.5">
    <div className="flex items-center gap-1 pl-1.5">
      <b className="flex-1 text-sm font-semibold">Tabs</b>
      {canEdit && <ArtifactTool icon={Plus} label="Add tab" onClick={() => add(null)} />}
    </div>
    <nav aria-label="Tabs">
      <ul className="grid gap-px">
        {rows.map(row => <li key={row.id} data-depth={row.depth}>
          {renaming === row.id && canEdit
            ? <RenameField name={row.name} depth={row.depth} onCommit={name => rename(row.id, name)} onCancel={() => setRenaming(null)} />
            : <button type="button" data-tab={row.id} onClick={() => onSelect(row.id)} onDoubleClick={() => { if (canEdit) setRenaming(row.id) }}
              aria-current={row.id === current?.id ? "page" : undefined} style={{ paddingLeft: `${0.5 + row.depth}rem` }}
              className={cn("w-full truncate rounded-[var(--radius-row)] py-1.5 pr-2 text-left text-sm hover:bg-accent coarse:min-h-[44px]",
                row.id === current?.id ? "bg-accent font-medium text-foreground" : "text-muted-foreground")}>
              {row.name}
            </button>}
        </li>)}
      </ul>
    </nav>
    {canEdit && current && <div role="toolbar" aria-label="Tab actions" className="flex flex-wrap items-center gap-0.5">
      <ArtifactTool icon={Pencil} label="Rename tab" onClick={() => setRenaming(current.id)} />
      <ArtifactTool icon={Plus} label="Add sub-tab" onClick={() => add(current.id)} />
      <ArtifactTool icon={ArrowUp} label="Move tab up" disabled={index <= 0} onClick={() => move(current, -1)} />
      <ArtifactTool icon={ArrowDown} label="Move tab down" disabled={index < 0 || index >= current.siblings.length - 1} onClick={() => move(current, 1)} />
      <ArtifactTool icon={IndentIncrease} label="Nest tab" disabled={index <= 0} onClick={() => indent(current)} />
      <ArtifactTool icon={IndentDecrease} label="Un-nest tab" disabled={current.parent === null} onClick={() => outdent(current)} />
      {current.id !== documentTabs.DEFAULT && <ArtifactTool icon={Trash2} label="Delete tab" onClick={() => setConfirming(current.id)} />}
    </div>}
    {canEdit && confirming && confirming === current?.id && <div role="alertdialog" aria-label="Delete tab" className="grid gap-2 rounded-card border p-2 text-sm">
      <p>Delete “{current.name}” and its page? Its sub-tabs move up a level. You can get it back from History.</p>
      <div className="flex gap-2">
        <button type="button" className="rounded-[var(--radius-row)] bg-destructive px-2 py-1 text-destructive-foreground coarse:min-h-[44px]" onClick={() => remove(current.id)}>Delete</button>
        <button type="button" className="rounded-[var(--radius-row)] border px-2 py-1 coarse:min-h-[44px]" onClick={() => setConfirming(null)}>Cancel</button>
      </div>
    </div>}
    {unreadable && <p role="alert" className="px-1.5 text-xs text-destructive">This document's tabs could not be read; only its first tab is shown.</p>}
    {error && <p role="alert" className="px-1.5 text-xs text-destructive">{error}</p>}
  </div>
}

function RenameField({ name, depth, onCommit, onCancel }: { name: string; depth: number; onCommit: (name: string) => void; onCancel: () => void }) {
  const [value, setValue] = React.useState(name)
  // Escape cancels for good: the blur its unmount can cause must not commit the edited name.
  // Enter commits and keeps the field open if the name is refused; a repeated commit of the same name writes nothing.
  const cancelled = React.useRef(false)
  return <input aria-label="Tab name" autoFocus value={value} maxLength={120} style={{ marginLeft: `${depth}rem` }}
    className="w-full rounded-[var(--radius-row)] border bg-background px-2 py-1 text-sm coarse:min-h-[44px]"
    onFocus={event => event.currentTarget.select()} onChange={event => setValue(event.target.value)}
    onKeyDown={event => {
      if (event.key === "Enter") { event.preventDefault(); onCommit(value) }
      else if (event.key === "Escape") { event.preventDefault(); cancelled.current = true; onCancel() }
    }}
    onBlur={() => { if (cancelled.current) return; if (value.trim() && value.trim() !== name) onCommit(value); else onCancel() }} />
}
