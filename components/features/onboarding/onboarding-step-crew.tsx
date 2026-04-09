"use client"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

const CREW_TEMPLATES = [
  { name: "Development", description: "Software development & DevOps", icon: "\u{1F4BB}" },
  { name: "Research", description: "Web research & data analysis", icon: "\u{1F50D}" },
  { name: "Support", description: "Customer support & helpdesk", icon: "\u{1F3A7}" },
  { name: "Marketing", description: "Content creation & SEO", icon: "\u{1F4C8}" },
] as const

export interface StepCrewProps {
  crewName: string
  onCrewNameChange: (v: string) => void
}

export function StepCrew({
  crewName,
  onCrewNameChange,
}: StepCrewProps) {
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold">Create your first crew</h2>
        <p className="text-sm text-muted-foreground">
          A crew is a team of AI agents. Pick a template or create your own.
        </p>
      </div>
      <div className="grid grid-cols-2 gap-2">
        {CREW_TEMPLATES.map((t) => (
          <button
            key={t.name}
            type="button"
            onClick={() => onCrewNameChange(t.name)}
            className={`flex items-start gap-2 rounded-lg border p-3 text-left transition-colors hover:bg-accent ${
              crewName === t.name ? "border-primary bg-primary/5" : "border-border"
            }`}
          >
            <span className="text-lg">{t.icon}</span>
            <div>
              <div className="text-sm font-medium">{t.name}</div>
              <div className="text-xs text-muted-foreground">{t.description}</div>
            </div>
          </button>
        ))}
      </div>
      <div className="space-y-2">
        <Label htmlFor="crew_name">Or enter a custom name</Label>
        <Input
          id="crew_name"
          value={crewName}
          onChange={(e) => onCrewNameChange(e.target.value)}
          placeholder="e.g. My Dev Team"
        />
      </div>
    </div>
  )
}
