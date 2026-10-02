"use client"

import * as React from "react"
import { getBearerToken, withServerBase } from "@/lib/server-base"
import { apiErrorMessage } from "@/lib/api-error"
import { SmallButton } from "./backups-kit"
import { INSTANCE_BACKUPS } from "./use-backups-data"

export interface UploadReceipt {
  path: string
  scope: "instance" | "workspace" | "crew"
  size_bytes: number
  format_version: number
  proof_level: number
  duplicate: boolean
  conversion_required: boolean
}

export function BackupsUpload({ disabled, onUploaded }: { disabled?: boolean; onUploaded: (receipt: UploadReceipt) => void }) {
  const input = React.useRef<HTMLInputElement>(null)
  const active = React.useRef<XMLHttpRequest | null>(null)
  const [progress, setProgress] = React.useState<number | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  React.useEffect(() => () => { active.current?.abort() }, [])
  const upload = (file: File) => {
    if (active.current) return
    if (file.size > 64 * 1024 ** 3 || file.size === 0) {
      setError("Choose an encrypted backup archive between 1 byte and 64 GiB.")
      return
    }
    const request = new XMLHttpRequest()
    active.current = request
    setError(null)
    setProgress(0)
    request.open("POST", withServerBase(`${INSTANCE_BACKUPS}/bundles/upload`))
    request.withCredentials = true
    request.setRequestHeader("Content-Type", "application/octet-stream")
    const token = getBearerToken()
    if (token) request.setRequestHeader("Authorization", `Bearer ${token}`)
    request.upload.onprogress = (event) => {
      if (event.lengthComputable) setProgress(Math.round(event.loaded / event.total * 100))
    }
    const finish = () => { active.current = null; setProgress(null) }
    request.onload = () => {
      finish()
      try {
        const result = JSON.parse(request.responseText)
        if (request.status !== 201) {
          setError(apiErrorMessage(result, `Upload failed (HTTP ${request.status}).`))
          return
        }
        onUploaded(result as UploadReceipt)
      } catch { setError("The server returned an unreadable upload result.") }
    }
    request.onerror = () => { finish(); setError("Upload interrupted. Check your connection and retry.") }
    request.onabort = () => { finish(); setError("Upload cancelled. No restore was started.") }
    request.send(file)
  }
  return <div className="flex flex-wrap items-center gap-2">
    <input ref={input} type="file" accept=".zst,application/zstd,application/octet-stream" className="sr-only" aria-label="Encrypted backup archive"
      onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) upload(file) }} />
    <SmallButton disabled={disabled || progress !== null} onClick={() => input.current?.click()}>Upload a backup…</SmallButton>
    {progress !== null && <>
      <span role="status" className="text-xs">{progress < 100 ? `Uploading ${progress}%` : "Validating archive…"}</span>
      <SmallButton onClick={() => active.current?.abort()}>Cancel upload</SmallButton>
    </>}
    {error && <p role="alert" className="max-w-72 break-words text-xs text-destructive">{error}</p>}
  </div>
}
