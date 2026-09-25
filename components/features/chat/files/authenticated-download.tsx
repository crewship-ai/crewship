"use client"

import { useEffect, useRef, useState, type ComponentProps } from "react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { getAuthMode } from "@/lib/server-base"

/** Native downloads for cookies; bearer sessions must go through apiFetch. */
export function AuthenticatedDownload({ href, download, onClick, ...props }: ComponentProps<"a"> & { href: string; download: string }) {
  const [busy, setBusy] = useState(false)
  const pending = useRef<AbortController | null>(null)
  useEffect(() => { setBusy(false); pending.current = null; return () => pending.current?.abort() }, [href])
  return <a {...props} href={href} download={download} aria-busy={busy} onClick={(event) => {
    onClick?.(event)
    if (event.defaultPrevented || getAuthMode() !== "bearer") return
    event.preventDefault()
    if (pending.current) return
    const controller = new AbortController()
    pending.current = controller
    setBusy(true)
    void apiFetch(href, { signal: controller.signal }).then(async (response) => {
      if (!response.ok) throw new Error(`Download failed (${response.status})`)
      // Download is independent of the preview's size cap.
      const body = await response.blob()
      if (controller.signal.aborted) return
      const url = URL.createObjectURL(new Blob([body], { type: "application/octet-stream" }))
      const link = document.createElement("a")
      link.href = url; link.download = download; link.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    }).catch(() => { if (!controller.signal.aborted) toast.error("Could not download file. Please try again.") })
      .finally(() => { if (pending.current === controller) { pending.current = null; if (!controller.signal.aborted) setBusy(false) } })
  }} />
}
