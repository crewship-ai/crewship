"use client"

import * as React from "react"
import { Download } from "lucide-react"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { apiFetch } from "@/lib/api-fetch"
import { ArtifactTool } from "../../frame"

/**
 * The formats the server really produces for a Docs page (#5147): each one is
 * a `GET /document/export` format with the same access as reading the document.
 * A format joins this list only when that route returns it.
 */
export const DOCS_EXPORT_FORMATS = [
  { format: "markdown", label: "Markdown", extension: "md" },
  { format: "docx", label: "Word", extension: "docx" },
] as const

type ExportFormat = (typeof DOCS_EXPORT_FORMATS)[number]

const SERVER_FILE_NAME = /^attachment; filename="?([A-Za-z0-9_-]{1,100}\.(?:md|docx))"?$/

/** The server's own failure reason, or a plain fallback; never response content. */
async function failureReason(response: Response, label: string) {
  const data = await response.json().catch(() => null) as { error?: unknown } | null
  return typeof data?.error === "string" && data.error.length <= 300 ? data.error : `The ${label} export could not be created.`
}

/**
 * Download the confirmed document in one of the formats above. It exports what
 * the server has saved: changes this browser has not sent yet are not included,
 * which the menu says.
 */
export function DocsExportMenu({ workspaceId, pageId }: { workspaceId: string; pageId: string }) {
  const [pending, setPending] = React.useState<ExportFormat["format"] | null>(null)
  const [error, setError] = React.useState("")
  const download = async (entry: ExportFormat) => {
    setPending(entry.format)
    setError("")
    let url: string | undefined
    try {
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/document/export?format=${entry.format}&workspace_id=${encodeURIComponent(workspaceId)}`, { cache: "no-store" })
      if (!response.ok) throw new Error(await failureReason(response, entry.label))
      const named = SERVER_FILE_NAME.exec(response.headers.get("Content-Disposition") ?? "")?.[1]
      const name = named?.endsWith("." + entry.extension) ? named : `document.${entry.extension}`
      url = URL.createObjectURL(await response.blob())
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = name
      document.body.appendChild(anchor)
      try { anchor.click() } finally { anchor.remove() }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `The ${entry.label} export could not be created.`)
    } finally {
      setPending(null)
      if (url) {
        const revoke = URL.revokeObjectURL.bind(URL)
        const downloaded = url
        setTimeout(() => revoke(downloaded), 0)
      }
    }
  }
  return <>
    <DropdownMenu>
      <DropdownMenuTrigger asChild><ArtifactTool icon={Download} label="Export" disabled={pending !== null} /></DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Saved content only; unsent changes are not included.</DropdownMenuLabel>
        {DOCS_EXPORT_FORMATS.map(entry => <DropdownMenuItem key={entry.format} disabled={pending !== null} onSelect={() => void download(entry)}>
          <Download aria-hidden />{pending === entry.format ? `Preparing ${entry.label}…` : entry.label}
          <span className="ml-auto font-mono text-xs text-muted-foreground">.{entry.extension}</span>
        </DropdownMenuItem>)}
      </DropdownMenuContent>
    </DropdownMenu>
    {error && <span role="alert" className="max-w-56 px-1 text-xs text-destructive">{error}</span>}
  </>
}
