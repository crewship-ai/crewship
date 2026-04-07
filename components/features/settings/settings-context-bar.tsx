"use client"

import { cn } from "@/lib/utils"

export type Scope = "user" | "org"

interface SettingsContextBarProps {
  scope: Scope
  onScopeChange: (scope: Scope) => void
  workspaceName?: string
}

export function SettingsContextBar({ scope, onScopeChange, workspaceName }: SettingsContextBarProps) {
  return (
    <div className="shrink-0 z-20 flex items-center justify-between h-9 bg-card border-b border-white/[0.1] px-3">
      <div className="flex items-center gap-3 min-w-0">
        {/* Scope toggle */}
        <div className="flex items-stretch h-8">
          {([
            { id: "user" as const, label: "User" },
            { id: "org" as const, label: "Workspace" },
          ]).map(({ id, label }) => (
            <button
              key={id}
              onClick={() => onScopeChange(id)}
              className={cn(
                "flex items-center px-3 text-[12px] font-medium border-b-2 transition-all duration-100 relative top-px",
                scope === id
                  ? "border-blue-400 text-blue-400"
                  : "border-transparent text-muted-foreground hover:text-foreground/80",
              )}
            >
              {label}
            </button>
          ))}
        </div>

        {/* Workspace info */}
        {scope === "org" && workspaceName && (
          <div className="flex items-center gap-2 min-w-0">
            <div className="w-5 h-5 rounded-[4px] bg-primary flex items-center justify-center text-primary-foreground text-[9px] font-bold shrink-0">
              {workspaceName[0]?.toUpperCase()}
            </div>
            <span className="text-[11px] text-muted-foreground truncate">{workspaceName}</span>
          </div>
        )}
      </div>
    </div>
  )
}
