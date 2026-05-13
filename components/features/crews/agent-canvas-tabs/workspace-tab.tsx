"use client"

export interface WorkspaceTabProps {
  agentId: string
  agentSlug: string
  onOpenFiles?: () => void
}

export function WorkspaceTab({ agentSlug, onOpenFiles }: WorkspaceTabProps) {
  return (
    <div className="space-y-4">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold">Workspace</h2>
        <span className="text-xs text-muted-foreground">files in container <code className="text-foreground/80">/crew/agents/{agentSlug}</code></span>
      </div>
      <div className="rounded-xl border border-white/8 bg-card p-6 flex items-center gap-4">
        <div className="flex-1">
          <div className="text-sm font-medium">Agent files browser</div>
          <div className="text-xs text-muted-foreground mt-1">
            Browse, edit, and upload files in this agent&apos;s home directory.
            The browser opens in the bottom panel for fast peek-and-edit without losing your place.
          </div>
        </div>
        {onOpenFiles ? (
          <button
            type="button"
            onClick={onOpenFiles}
            className="text-sm px-3 py-2 rounded-lg bg-blue-500 hover:bg-blue-400 text-white"
          >
            Open Files panel
          </button>
        ) : (
          <span className="text-xs text-muted-foreground italic">Files panel not available in this view.</span>
        )}
      </div>
    </div>
  )
}
