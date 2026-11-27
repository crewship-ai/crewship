"use client"

import { FileSpreadsheet, FileText } from "lucide-react"
import { isSheetFileName } from "../artifact-context/sheet/sheet-icon"

/** The files sent with a message, one chip each; the path the agent got is on hover. */
export function MessageFiles({ files }: { files: { path: string; name: string }[] }) {
  if (files.length === 0) return null
  return <ul aria-label="Attached files" className="flex flex-wrap justify-end gap-1.5">
    {files.map(file => <li key={file.path} title={file.path}
      className="inline-flex min-w-0 max-w-full items-center gap-1 rounded-md border bg-background px-2 py-0.5 text-xs text-muted-foreground">
      {isSheetFileName(file.name) ? <FileSpreadsheet className="size-3.5 shrink-0 text-sheet" aria-hidden /> : <FileText className="size-3.5 shrink-0" aria-hidden />}<span className="truncate">{file.name}</span>
    </li>)}
  </ul>
}
