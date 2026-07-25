"use client"

import { ShieldAlert } from "lucide-react"
import { parseDevcontainerFull } from "./runtime-config-data"

// #1380 — "show the effective posture wherever the crew is viewed". The
// privileged toggle lives three clicks deep (Settings → Container image &
// features → Security), so a crew running with isolation stripped looked
// identical to a hardened one on the surface an operator actually opens.
// This badge is the always-visible counterweight to that.

/**
 * True when the crew's stored devcontainer_config declares privileged mode.
 * Tolerates null / malformed JSON by reporting false — the same "unset means
 * not privileged" reading the runtime uses; a config we can't parse never
 * reaches the container in a privileged state either.
 */
export function isCrewPrivileged(devcontainerConfig: string | null | undefined): boolean {
  if (!devcontainerConfig) return false
  return parseDevcontainerFull(devcontainerConfig).privileged
}

export function CrewPrivilegedBadge({
  devcontainerConfig,
}: {
  devcontainerConfig: string | null | undefined
}) {
  if (!isCrewPrivileged(devcontainerConfig)) return null
  return (
    <span
      title="This crew runs privileged — no-new-privileges is off, the rootfs is writable, and the container has host device access."
      className="text-[11px] flex items-center gap-1 px-2 py-0.5 rounded-full bg-red-500/15 text-red-300 border border-red-500/40"
    >
      <ShieldAlert className="h-3 w-3" />
      Privileged · isolation reduced
    </span>
  )
}
