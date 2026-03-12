"use client"

import { Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { CrewIcon } from "@/components/ui/crew-icon"

interface CrewHeaderProps {
  name: string
  slug: string
  icon: string | null
  color?: string | null
  description: string | null
  editing: boolean
  canEdit: boolean
  onToggleEdit: () => void
}

export function CrewHeader({
  name,
  slug,
  icon,
  color,
  description,
  editing,
  canEdit,
  onToggleEdit,
}: CrewHeaderProps) {
  return (
    <div>
      <div className="flex items-center gap-4">
        <CrewIcon icon={icon || "briefcase"} color={color} size="lg" />
        <div className="flex-1 min-w-0">
          <h1 className="text-title font-semibold truncate">{name}</h1>
          <p className="text-body text-muted-foreground font-mono">{slug}</p>
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
        <p className="text-body text-muted-foreground mt-2">{description}</p>
      )}
    </div>
  )
}
