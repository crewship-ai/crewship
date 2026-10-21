"use client"

import { useEffect, useMemo, useState } from "react"
import { TableVirtuoso } from "react-virtuoso"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { columnLabel, selectionContext, TABLE_LIMITS, visibleRows, type TableData } from "./table-data"

export function TablePreview({ bytes, path, onAskSelection }: { bytes: Uint8Array<ArrayBuffer>; path: string; onAskSelection?: (context: string) => void }) {
  const [result, setResult] = useState<{ source: Uint8Array; path: string; table?: TableData; version?: string; error?: string } | null>(null)
  useEffect(() => {
    if (bytes.byteLength > TABLE_LIMITS.bytes) { setResult({ source: bytes, path, error: "Table exceeds the 5 MiB preview limit. Download the original file." }); return }
    let worker: Worker
    try { worker = new Worker("/chat-workers/table-v1.js", { type: "module" }) }
    catch { setResult({ source: bytes, path, error: "Table worker is unavailable. Download the original file." }); return }
    let active = true
    const timer = setTimeout(() => { worker.terminate(); if (active) setResult({ source: bytes, path, error: "Table preview timed out. Download the original file." }); active = false }, 5000)
    worker.onerror = () => { clearTimeout(timer); worker.terminate(); if (active) setResult({ source: bytes, path, error: "Could not parse table. Download the original file." }); active = false }
    worker.onmessage = async ({ data }: MessageEvent<{ table?: TableData; error?: string }>) => {
      clearTimeout(timer); worker.terminate()
      if (!active) return
      if (data.error || !data.table) { setResult({ source: bytes, path, error: data.error ?? "Could not parse table." }); return }
      try {
        const digest = await crypto.subtle.digest("SHA-256", bytes)
        const version = Array.from(new Uint8Array(digest), value => value.toString(16).padStart(2, "0")).join("")
        if (active) setResult({ source: bytes, path, table: data.table, version })
      } catch { if (active) setResult({ source: bytes, path, table: data.table }) }
    }
    const copy = bytes.slice().buffer
    worker.postMessage({ bytes: copy, separator: /\.tsv$/i.test(path) ? "\t" : "," }, [copy])
    return () => { active = false; clearTimeout(timer); worker.terminate() }
  }, [bytes, path])
  const current = result?.source === bytes && result.path === path ? result : null
  const table = current?.table
  if (current?.error) return <p role="alert" className="p-4 text-sm text-muted-foreground">{current.error}</p>
  if (!table) return <p role="status" className="p-4 text-sm text-muted-foreground">Preparing table…</p>
  return <TableGrid bytes={bytes} path={path} table={table} version={current.version} onAskSelection={onAskSelection} />
}

/** Shared read-only grid; selection always refers to the original source rows. */
export function TableGrid({ bytes, path, table, version, sheet = "CSV/TSV", onAskSelection }: { bytes: Uint8Array; path: string; table: TableData; version?: string; sheet?: string; onAskSelection?: (context: string) => void }) {
  const [filter, setFilter] = useState("")
  const [sort, setSort] = useState<{ column: number; descending: boolean } | null>(null)
  const [selection, setSelection] = useState<{ source: Uint8Array; path: string; indices: number[] } | null>(null)
  const [selectionError, setSelectionError] = useState("")
  const rows = useMemo(() => visibleRows(table, filter, sort), [table, filter, sort])
  const selected = selection?.source === bytes && selection.path === path ? selection.indices : []
  const ask = () => {
    try {
      if (!version) throw new Error("A verified file version is unavailable. Download the original file.")
      onAskSelection?.(selectionContext(path, version, table, selected, sheet)); setSelectionError("")
    } catch (error) { setSelectionError(error instanceof Error ? error.message : "Could not use selection.") }
  }
  return <section aria-label="Spreadsheet preview" className="flex h-full min-h-0 flex-col text-xs">
    <div className="flex flex-wrap items-center gap-2 border-b p-2">
      <Input aria-label="Filter table" placeholder="Filter rows…" value={filter} onChange={event => setFilter(event.target.value)} className="h-8 min-w-28 flex-1 text-xs" />
      <span role="status">{rows.length} / {table.rows.length} rows · read only</span>
      {onAskSelection && <Button size="sm" variant="outline" disabled={!selected.length || !version} onClick={ask}>Ask about selection ({selected.length})</Button>}
      {!!selected.length && <Button size="sm" variant="ghost" onClick={() => { setSelection(null); setSelectionError("") }}>Clear selection</Button>}
    </div>
    {selectionError && <p role="alert" className="px-3 py-2 text-destructive">{selectionError}</p>}
    <TableVirtuoso className="min-h-0 flex-1" data={rows} computeItemKey={(_, row) => row.index}
      fixedHeaderContent={() => <tr><th scope="col" className="bg-background px-2 py-1">Row</th>{Array.from({ length: table.columns }, (_, column) => <th key={column} scope="col" aria-sort={sort?.column === column ? sort.descending ? "descending" : "ascending" : "none"} className="bg-background p-0"><button type="button" className="w-full px-3 py-2 text-left coarse:min-h-[44px]" aria-label={`Sort column ${columnLabel(column)}`} onClick={() => setSort(previous => ({ column, descending: previous?.column === column && !previous.descending }))}>{columnLabel(column)}{sort?.column === column ? sort.descending ? " ↓" : " ↑" : ""}</button></th>)}</tr>}
      itemContent={(_, row) => <><th scope="row" className="bg-background px-2 py-1 font-normal"><label className="flex items-center gap-2 coarse:min-h-[44px]"><input type="checkbox" aria-label={`Select source row ${row.index + 1}`} checked={selected.includes(row.index)} onChange={event => setSelection({ source: bytes, path, indices: event.target.checked ? [...selected, row.index] : selected.filter(index => index !== row.index) })} />{row.index + 1}</label></th>{Array.from({ length: table.columns }, (_, column) => <td key={column} className="min-w-24 max-w-72 truncate border-b border-l border-border/50 px-2 py-1" title={row.cells[column] ?? ""}>{row.cells[column] ?? ""}</td>)}</>} />
    <p className="border-t px-3 py-1 text-muted-foreground">Original row and column coordinates are preserved. Cell values are never executed.</p>
  </section>
}
