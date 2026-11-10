"use client"

import * as React from "react"
import { Download } from "lucide-react"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { apiFetch } from "@/lib/api-fetch"
import { useSessionSafe } from "@/hooks/use-auth"
import { getBearerToken } from "@/lib/server-base"
import { assertWorkspaceRequest, captureWorkspaceRequest } from "@/lib/workspace-availability"
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
  const session = useSessionSafe()
  const human = session?.data?.user?.id
  const credential = getBearerToken()
  const scope = React.useMemo(() => ({ binding: [workspaceId, pageId, human], active: true, controllers: new Set<AbortController>(), credential }), [workspaceId, pageId, human, credential])
  const current = React.useRef(scope)
  if (current.current !== scope) {
    current.current.active = false
    for (const controller of current.current.controllers) controller.abort()
    current.current = scope
  }
  React.useEffect(() => {
    scope.active = true
    return () => { scope.active = false; for (const controller of scope.controllers) controller.abort() }
  }, [scope])
  const [progress, setProgress] = React.useState<{ scope: typeof scope; pending: ExportFormat["format"] | null; error: string }>()
  const pending = progress?.scope === scope ? progress.pending : null
  const error = progress?.scope === scope ? progress.error : ""
  const valid = () => current.current === scope && scope.active && scope.credential === getBearerToken() && Boolean(human && workspaceId && pageId)
  const download = async (entry: ExportFormat) => {
    if (!valid() || scope.controllers.size) return
    const controller = new AbortController()
    scope.controllers.add(controller)
    setProgress({ scope, pending: entry.format, error: "" })
    let url: string | undefined
    const endpoint = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/document/export?format=${entry.format}&workspace_id=${encodeURIComponent(workspaceId)}`
    try {
      const captured = captureWorkspaceRequest(endpoint)
      const assertActive = () => {
        if (!valid() || controller.signal.aborted) throw new Error("Export request retired.")
        assertWorkspaceRequest(captured)
      }
      assertActive()
      const response = await apiFetch(endpoint, { cache: "no-store", signal: controller.signal, redirect: "error" })
      assertActive()
      if (!response.ok) {
        const reason = await failureReason(response, entry.label)
        assertActive()
        throw new Error(reason)
      }
      const named = SERVER_FILE_NAME.exec(response.headers.get("Content-Disposition") ?? "")?.[1]
      const name = named?.endsWith("." + entry.extension) ? named : `document.${entry.extension}`
      const blob = await response.blob()
      assertActive()
      url = URL.createObjectURL(blob)
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = name
      document.body.appendChild(anchor)
      try { assertActive(); anchor.click() } finally { anchor.remove() }
    } catch (cause) {
      if (valid() && !controller.signal.aborted) setProgress({ scope, pending: null, error: cause instanceof Error ? cause.message : `The ${entry.label} export could not be created.` })
    } finally {
      scope.controllers.delete(controller)
      if (valid()) setProgress(value => value?.scope === scope ? { ...value, pending: null } : value)
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
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Current saved Page content only. Canvas layout, unsent changes and selected History revisions are not included.</DropdownMenuLabel>
        {DOCS_EXPORT_FORMATS.map(entry => <DropdownMenuItem key={entry.format} disabled={pending !== null} onSelect={() => void download(entry)}>
          <Download aria-hidden />{pending === entry.format ? `Preparing ${entry.label}…` : entry.label}
          <span className="ml-auto font-mono text-xs text-muted-foreground">.{entry.extension}</span>
        </DropdownMenuItem>)}
      </DropdownMenuContent>
    </DropdownMenu>
    {error && <span role="alert" className="max-w-56 px-1 text-xs text-destructive">{error}</span>}
  </>
}
