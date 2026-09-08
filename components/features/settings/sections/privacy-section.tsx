"use client"
import { PersonalMemory } from "@/components/features/crews/memory-workspace"

export function PrivacySection({ workspaceId }: { workspaceId: string }) {
  return <PersonalMemory key={workspaceId} workspaceId={workspaceId} />
}
