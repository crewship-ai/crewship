"use client"

import { Users, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"

interface CrewHeaderProps {
  name: string
  slug: string
  color: string | null
  icon: string | null
  description: string | null
  editing: boolean
  canEdit: boolean
  onToggleEdit: () => void
}

export function CrewHeader({
  name,
  slug,
  color,
  icon,
  description,
  editing,
  canEdit,
  onToggleEdit,
}: CrewHeaderProps) {
  const crewColor = color ?? "#6b7280"

  return (
    <div style={{ "--crew-color": crewColor } as React.CSSProperties}>
      <div className="flex items-center gap-4">
        <div
          className="flex h-12 w-12 items-center justify-center rounded-lg text-xl shrink-0"
          style={{ backgroundColor: `color-mix(in srgb, var(--crew-color) 12%, transparent)` }}
        >
          {icon ?? (
            <Users className="h-6 w-6" style={{ color: "var(--crew-color)" }} />
          )}
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-semibold truncate">{name}</h1>
            <span
              className="h-3 w-3 rounded-full shrink-0"
              style={{ backgroundColor: "var(--crew-color)" }}
            />
          </div>
          <p className="text-sm text-muted-foreground font-mono">{slug}</p>
        </div>
        {canEdit && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-pressed={editing}
            onClick={onToggleEdit}
          >
            <Pencil className="mr-2 h-3.5 w-3.5" />
            {editing ? "Cancel" : "Edit"}
          </Button>
        )}
      </div>
      {description && !editing && (
        <p className="text-sm text-muted-foreground mt-2">{description}</p>
      )}
    </div>
  )
}
