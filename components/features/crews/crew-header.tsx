"use client"

import { Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { getCrewIconUrl } from "@/lib/crew-icon"

interface CrewHeaderProps {
  name: string
  slug: string
  icon: string | null
  description: string | null
  editing: boolean
  canEdit: boolean
  onToggleEdit: () => void
}

export function CrewHeader({
  name,
  slug,
  icon,
  description,
  editing,
  canEdit,
  onToggleEdit,
}: CrewHeaderProps) {
  return (
    <div>
      <div className="flex items-center gap-4">
        <img
          src={getCrewIconUrl(icon || name)}
          alt={name}
          className="h-12 w-12 rounded-lg shrink-0"
        />
        <div className="flex-1 min-w-0">
          <h1 className="text-xl font-semibold truncate">{name}</h1>
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
