"use client"

import { cn } from "@/lib/utils"

import { ROLE_LABEL, type CellState } from "./members-model"

/** A role's colour, the same in the matrix, the panel and the phone list, so a
 *  role reads the same wherever it appears. */
export const ROLE_TONE: Record<string, string> = {
  OWNER: "border-purple/30 bg-purple/10 text-purple-hover",
  ADMIN: "border-info/30 bg-info/10 text-info",
  MANAGER: "border-notice/30 bg-notice/10 text-notice",
  MEMBER: "border-gold/30 bg-gold/10 text-gold",
  VIEWER: "border-border bg-surface-subtle text-muted-foreground",
}
export const ROLE_DOT: Record<string, string> = {
  OWNER: "bg-purple", ADMIN: "bg-info", MANAGER: "bg-notice", MEMBER: "bg-gold", VIEWER: "bg-muted-foreground",
}

export function RolePill({ role, className }: { role: string; className?: string }) {
  return (
    <span className={cn("inline-flex h-[22px] items-center gap-1.5 rounded-md border px-2 font-mono text-[10.5px] font-medium lowercase", ROLE_TONE[role] ?? ROLE_TONE.VIEWER, className)}>
      <span className={cn("h-1.5 w-1.5 rounded-full", ROLE_DOT[role] ?? ROLE_DOT.VIEWER)} aria-hidden />
      {ROLE_LABEL[role] ?? role}
    </span>
  )
}

/** One capability, at a glance: filled when held, an orange ring when it is
 *  an exception to the profile. */
export function Dot({ state, danger, className }: { state: CellState; danger?: boolean; className?: string }) {
  const fill = danger ? "bg-destructive" : "bg-primary"
  return (
    <span aria-hidden className={cn(
      "block rounded-full transition-transform group-hover:scale-105",
      state === "profile" && cn("h-3 w-3", fill),
      state === "added" && cn("h-3 w-3 ring-2 ring-warn ring-offset-2 ring-offset-card", fill),
      state === "removed" && "h-3 w-3 border-2 border-warn",
      state === "off" && "h-2.5 w-2.5 border-[1.5px] border-border",
      className,
    )} />
  )
}
