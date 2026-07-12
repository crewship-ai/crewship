"use client"

import { useSyncExternalStore } from "react"

import { avatarStylesVersion, subscribeAvatarStyles } from "@/lib/agent-avatar"

/**
 * Re-renders the component whenever a lazy DiceBear style collection
 * finishes loading, so `getAgentAvatarUrl` calls upgrade from the
 * deterministic placeholder to the real avatar. Cheap: the snapshot is
 * a monotonic integer; components subscribe once.
 */
export function useAvatarStylesVersion(): number {
  return useSyncExternalStore(subscribeAvatarStyles, avatarStylesVersion, avatarStylesVersion)
}
