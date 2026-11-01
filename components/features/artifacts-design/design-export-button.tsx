"use client"

import { useState } from "react"
import { FileDown } from "lucide-react"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { artifactsDesignExport } from "@/lib/feature-flags"

const SERVER_FILE_NAME = /^attachment; filename="([a-z0-9][a-z0-9_-]{0,63}-design-v[1-9][0-9]{0,15}\.html)"$/

/**
 * Downloads one immutable Design version as portable HTML (#4449). The server
 * renders it from that saved version; unsaved live edits are never exported.
 * Hidden unless NEXT_PUBLIC_ARTIFACTS_DESIGN_EXPORT is on.
 */
export function DesignExportButton({ workspaceId, pageId, version }: { workspaceId: string; pageId: string; version?: number }) {
 const [pending, setPending] = useState(false)
 const [error, setError] = useState("")
 if (!artifactsDesignExport() || !version) return null
 const exportHTML = async () => {
  setPending(true)
  setError("")
  let url: string | undefined
  try {
   const response = await apiFetch(`/api/v1/pages/@${encodeURIComponent(pageId)}/design/export?format=html&version=${version}&workspace_id=${encodeURIComponent(workspaceId)}`, { cache: "no-store" })
   if (!response.ok) {
    const data = await response.json().catch(() => null)
    throw new Error(typeof data?.error === "string" ? data.error : "The HTML export could not be created.")
   }
   const name = SERVER_FILE_NAME.exec(response.headers.get("Content-Disposition") ?? "")?.[1] ?? `design-v${version}.html`
   url = URL.createObjectURL(await response.blob())
   const anchor = document.createElement("a")
   anchor.href = url
   anchor.download = name
   document.body.appendChild(anchor)
   try { anchor.click() } finally { anchor.remove() }
  } catch (cause) {
   setError(cause instanceof Error ? cause.message : "The HTML export could not be created.")
  } finally {
   setPending(false)
   if (url) {
    const revoke = URL.revokeObjectURL.bind(URL)
    const downloaded = url
    setTimeout(() => revoke(downloaded), 0)
   }
  }
 }
 return <>
  <Button variant="outline" disabled={pending} onClick={() => { void exportHTML() }} title={`Download saved version ${version} as one HTML file. Unsaved live changes are not included.`}><FileDown className="size-4" />Export HTML<span className="sr-only"> (version {version})</span></Button>
  {error && <p role="alert" className="basis-full text-xs text-destructive">{error}</p>}
 </>
}
