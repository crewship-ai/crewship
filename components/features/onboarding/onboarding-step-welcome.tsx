"use client"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export interface StepWelcomeProps {
  workspaceName: string
  onWorkspaceNameChange: (v: string) => void
}

export function StepWelcome({
  workspaceName,
  onWorkspaceNameChange,
}: StepWelcomeProps) {
  return (
    <div className="space-y-4">
      <div className="text-center space-y-2">
        <h2 className="text-lg font-semibold">Welcome to Crewship!</h2>
        <p className="text-sm text-muted-foreground">
          Let&apos;s set up your workspace and get your first AI agent running in under a minute.
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="workspace_name">Workspace Name (optional)</Label>
        <Input
          id="workspace_name"
          value={workspaceName}
          onChange={(e) => onWorkspaceNameChange(e.target.value)}
          placeholder="e.g. My Company"
        />
        <p className="text-xs text-muted-foreground">
          A workspace was auto-created for you. Rename it if you like.
        </p>
      </div>
    </div>
  )
}
