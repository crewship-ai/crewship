"use client"

import { useEffect, useState } from "react"
import { staticArtifactDocument } from "@/lib/pages/artifact-static"

/** Do not retain the previous source's preview while validating its replacement. */
export function useStaticArtifactDocument(source: string | undefined): string {
  const [result, setResult] = useState<{ source: string; document: string }>()
  useEffect(() => {
    if (source === undefined) return
    const controller = new AbortController()
    void staticArtifactDocument(source, controller.signal).then(document => {
      if (!controller.signal.aborted) setResult({ source, document })
    }).catch(() => {
      if (!controller.signal.aborted) setResult({ source, document: "<p>Static preview unavailable.</p>" })
    })
    return () => controller.abort()
  }, [source])
  return source !== undefined && result?.source === source ? result.document : ""
}
