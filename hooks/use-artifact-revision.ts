"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"

export function parseArtifactRevision(value: string | null): number | undefined {
  if (!value || !/^[1-9][0-9]*$/.test(value)) return undefined
  const revision = Number(value)
  return Number.isSafeInteger(revision) ? revision : undefined
}

/** Match Pages tab navigation: local selection plus a reloadable URL. */
export function useArtifactRevision(): [number | undefined, (revision: number | undefined) => void] {
  const search = useSearchParams().toString()
  const [revision, setRevision] = useState(() => parseArtifactRevision(new URLSearchParams(search).get("revision")))
  const applied = useRef(search)
  useEffect(() => {
    if (applied.current === search) return
    applied.current = search
    setRevision(parseArtifactRevision(new URLSearchParams(search).get("revision")))
  }, [search])
  const select = useCallback((value: number | undefined) => {
    setRevision(value)
    const url = new URL(window.location.href)
    if (value === undefined) url.searchParams.delete("revision")
    else url.searchParams.set("revision", String(value))
    window.history.replaceState(window.history.state, "", url.toString())
  }, [])
  return [revision, select]
}
