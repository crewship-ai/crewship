"use client"

export interface FilesTabProps {
  onOpenFiles: () => void
}

export function FilesTab({ onOpenFiles }: FilesTabProps) {
  return (
    <div className="space-y-4">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold">Crew files</h2>
        <span className="text-xs text-muted-foreground">shared at <code className="text-foreground/80">/crew/shared</code></span>
      </div>
      <div className="rounded-xl border border-white/8 bg-card p-6 flex items-center gap-4">
        <div className="flex-1">
          <div className="text-sm font-medium">Crew-wide shared files</div>
          <div className="text-xs text-muted-foreground mt-1">
            Browse and edit files in <code className="text-foreground/80">/crew/shared</code>. All agents in this crew read from the same tree —
            use it for runbooks, policies, and templates that should be visible to every agent.
          </div>
        </div>
        <button
          type="button"
          onClick={onOpenFiles}
          className="text-sm px-3 py-2 rounded-lg bg-blue-500 hover:bg-blue-400 text-white"
        >
          Open Files panel
        </button>
      </div>
    </div>
  )
}
