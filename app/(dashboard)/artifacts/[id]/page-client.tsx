"use client"

import { useParams } from "next/navigation"
import { ArtifactsRoute } from "@/components/features/artifacts-library/artifacts-route"

/** /artifacts/<id>: the library with that artifact open. The exported `_` shell alone is the library. */
export function ArtifactDetailRoute() {
  const raw = useParams<{ id: string }>()?.id
  let id = typeof raw === "string" ? raw : null
  try { if (id) id = decodeURIComponent(id) } catch { id = null }
  return <ArtifactsRoute item={id && id !== "_" ? id : null} />
}
