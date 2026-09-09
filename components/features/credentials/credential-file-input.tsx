"use client"

import * as React from "react"
import { FileText, Upload, X } from "lucide-react"
import { Button } from "@/components/ui/button"

// Match maxCredentialValueLen and maxCredentialFieldValueLen in the API. Read only UTF-8 text; never persist a file locally.
export const CREDENTIAL_FILE_LIMIT = 64 * 1024
export async function readCredentialFile(file: File, jsonOnly = false): Promise<string> {
  if (!file.size) throw new Error("This file is empty.")
  if (file.size > CREDENTIAL_FILE_LIMIT) throw new Error("Choose a text file no larger than 64 KiB.")
  let value: string
  try {
    value = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(await file.arrayBuffer())
  } catch { throw new Error("Choose a UTF-8 text file. Binary files are not supported.") }
  if (value.includes("\0")) throw new Error("Binary files are not supported. Choose a text file.")
  if (jsonOnly || file.name.toLowerCase().endsWith(".json")) {
    try { JSON.parse(value) } catch { throw new Error("This file is not valid JSON. Choose the complete login file or correct its contents.") }
  }
  return value
}

export function CredentialFileInput({ id, value, onChange, jsonOnly, onFilename }: {
  id: string; value: string; onChange: (value: string) => void; jsonOnly?: boolean; onFilename?: (name: string) => void
}) {
  const input = React.useRef<HTMLInputElement>(null)
  const generation = React.useRef(0)
  const [selected, setSelected] = React.useState<{name: string; size: number; value: string} | null>(null)
  const [error, setError] = React.useState("")
  React.useEffect(() => () => { generation.current++ }, [])
  const current = selected?.value === value ? selected : null
  return <div className="rounded-xl border border-dashed border-border bg-card p-3">
    <input ref={input} id={`${id}-file`} type="file" className="sr-only" aria-label="Choose credential file" accept={jsonOnly ? ".json,application/json" : undefined} onChange={async (event) => {
      const file = event.target.files?.[0]
      event.target.value = ""
      if (!file) return
      const request = ++generation.current
      setError("")
      try {
        const contents = await readCredentialFile(file, jsonOnly)
        if (request !== generation.current) return
        setSelected({name: file.name, size: file.size, value: contents})
        onChange(contents); onFilename?.(file.name)
      } catch (e) { if (request === generation.current) setError(e instanceof Error ? e.message : "Could not read this file.") }
    }} />
    <div className="flex items-center gap-2">
      {current ? <><FileText className="size-4 shrink-0 text-primary" /><span className="min-w-0 flex-1 truncate text-sm">{current.name}<span className="ml-2 text-xs text-muted-foreground">{Math.ceil(current.size / 1024)} KiB</span></span></> : <p className="flex-1 text-xs text-muted-foreground">Choose a text file, or paste its complete contents below.</p>}
      <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()}><Upload className="size-3.5" />{current ? "Replace file" : "Choose file"}</Button>
      {current && <Button type="button" variant="ghost" size="icon" aria-label="Remove file" onClick={() => { generation.current++; setSelected(null); onChange(""); onFilename?.("") }}><X className="size-4" /></Button>}
    </div>
    {error && <p role="alert" className="mt-2 text-xs text-destructive">{error}</p>}
  </div>
}
