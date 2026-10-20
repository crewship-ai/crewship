"use client"
import type { ReactNode } from "react"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"
import type { ExactRevisionRead } from "./exact-revision"

interface Props { read: ExactRevisionRead; children: (read: Extract<ExactRevisionRead, { status: "ready" }>) => ReactNode }

/** Body slot for a pinned revision. Shows content only for a verified exact read, never the latest as a stand-in. */
export function PinnedRevisionBody({ read, children }: Props) {
  const enabled = useArtifactContextEnabled()
  if (!enabled) return null
  if (read.status === "ready") return <>{children(read)}</>
  if (read.status === "loading") return <p role="status">Loading this revision…</p>
  if (read.status === "error") return <p role="alert">This revision could not be loaded right now. Try again; access was not removed.</p>
  if (read.status === "denied") return <p role="alert">Access to this revision is unavailable.</p>
  return <p role="alert">This revision is not available.</p>
}
