"use client"

import { useEffect, useState } from "react"
import { TableGrid } from "./table-preview"
import { XLSX_LIMITS } from "./xlsx-limits"
import type { WorkbookData } from "./xlsx-data"

export function XlsxPreview({ bytes, path, onAskSelection }: { bytes: Uint8Array<ArrayBuffer>; path: string; onAskSelection?: (context: string) => void }) {
  const [result, setResult] = useState<{ source: Uint8Array; path: string; workbook?: WorkbookData; version?: string; error?: string } | null>(null)
  const [selectedSheet, setSelectedSheet] = useState<{ source: Uint8Array; path: string; index: number } | null>(null)
  useEffect(() => {
    if (bytes.byteLength > XLSX_LIMITS.compressedBytes) { setResult({ source: bytes, path, error: "Workbook exceeds the 5 MiB input limit. Download the original workbook." }); return }
    let worker: Worker
    try { worker = new Worker("/chat-workers/xlsx-v1.js", { type: "module" }) }
    catch { setResult({ source: bytes, path, error: "Workbook worker is unavailable. Download the original workbook." }); return }
    let active = true
    const finish = () => { clearTimeout(timer); worker.terminate() }
    const timer = setTimeout(() => { finish(); if (active) setResult({ source: bytes, path, error: "Workbook preview timed out. Download the original workbook." }); active = false }, 5000)
    worker.onerror = () => { finish(); if (active) setResult({ source: bytes, path, error: "Could not parse workbook. Download the original workbook." }); active = false }
    worker.onmessage = async ({ data }: MessageEvent<{ workbook?: WorkbookData; error?: string }>) => {
      finish()
      if (!active) return
      if (data.error || !data.workbook?.sheets.length) { setResult({ source: bytes, path, error: data.error ?? "Workbook has no supported worksheets. Download the original workbook." }); active = false; return }
      try {
        const digest = await crypto.subtle.digest("SHA-256", bytes)
        const version = Array.from(new Uint8Array(digest), value => value.toString(16).padStart(2, "0")).join("")
        if (active) setResult({ source: bytes, path, workbook: data.workbook, version })
      } catch { if (active) setResult({ source: bytes, path, workbook: data.workbook }) }
    }
    const copy = bytes.slice().buffer
    worker.postMessage({ bytes: copy }, [copy])
    return () => { active = false; finish() }
  }, [bytes, path])
  const current = result?.source === bytes && result.path === path ? result : null
  if (current?.error) return <p role="alert" className="p-4 text-sm text-muted-foreground">{current.error}</p>
  if (!current?.workbook) return <p role="status" className="p-4 text-sm text-muted-foreground">Preparing workbook…</p>
  const index = selectedSheet?.source === bytes && selectedSheet.path === path ? selectedSheet.index : 0
  const sheet = current.workbook.sheets[index]
  return <section aria-label="Workbook preview" className="flex h-full min-h-0 flex-col">
    <div className="flex flex-wrap items-center gap-2 border-b p-2 text-xs">
      <label>Worksheet <select aria-label="Worksheet" className="rounded border bg-background p-2 coarse:min-h-[44px]" value={index} onChange={event => setSelectedSheet({ source: bytes, path, index: Number(event.target.value) })}>{current.workbook.sheets.map((item, number) => <option key={item.name} value={number}>{item.name}</option>)}</select></label>
      <span>Saved values only; formulas are never calculated. {current.workbook.missingFormulaResults > 0 && `${current.workbook.missingFormulaResults} formula results were not saved.`}</span>
    </div>
    <TableGrid key={sheet.name} bytes={bytes} path={path} table={sheet.table} version={current.version} sheet={sheet.name} onAskSelection={onAskSelection} />
  </section>
}
