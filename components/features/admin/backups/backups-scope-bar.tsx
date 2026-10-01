"use client"

import { cn } from "@/lib/utils"
import { SettingsSegmented } from "@/components/features/settings/shared"
import { WsAvatar } from "./backups-kit"
import type { BackupScope, ScopeWorkspace } from "./backups-model"

/**
 * The strip under the sub-bar that says what the page covers: the whole
 * instance, or the workspaces ticked here. Instance-only pages (Storage,
 * Keys & alerts) say that instead; Data retention is always per workspace.
 */
export function BackupsScopeBar({ mode, scope, onScope, workspaces, selected, onToggle, instanceSummary }: {
  mode: "scoped" | "instance-only" | "workspaces-only"
  scope: BackupScope
  onScope: (s: BackupScope) => void
  workspaces: ScopeWorkspace[]
  selected: Set<string>
  onToggle: (id: string) => void
  instanceSummary: string
}) {
  const chips = (
    <>
      {workspaces.map((w) => {
        const on = selected.has(w.id)
        return (
          <button key={w.id} type="button" aria-pressed={on} onClick={() => onToggle(w.id)} data-ws={w.slug}
            className={cn(
              "inline-flex h-7 items-center gap-1.5 rounded-full border border-control-border py-0.5 pl-1 pr-2.5 text-[13px] transition-opacity coarse:h-[2.75rem]",
              !on && "border-dashed opacity-50",
            )}>
            <WsAvatar name={w.name} size={18} />
            {w.name}
          </button>
        )
      })}
      <span className="text-[13px] text-muted-foreground" data-slot="scope-count">{selected.size} of {workspaces.length}</span>
    </>
  )
  return (
    <div data-slot="backups-scope" className="flex flex-wrap items-center gap-2 border-b border-border bg-card px-4 py-2.5 md:px-6">
      {mode === "instance-only" ? (
        <span className="text-[13px] text-muted-foreground">Instance setting · applies to every backup plan</span>
      ) : mode === "workspaces-only" ? (
        <>
          <span className="text-[13px] text-muted-foreground">Workspaces</span>
          {chips}
        </>
      ) : (
        <>
          <SettingsSegmented<BackupScope> label="Scope" value={scope} onChange={onScope}
            options={[{ value: "instance", label: "Whole instance" }, { value: "workspaces", label: "Selected workspaces" }]} />
          {scope === "instance" ? <span className="text-[13px] text-muted-foreground">{instanceSummary}</span> : chips}
        </>
      )}
    </div>
  )
}
