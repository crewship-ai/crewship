"use client"

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { slugify } from "@/lib/utils/slugify"

interface CrewNameSlugFieldsProps {
  name: string
  slug: string
  onNameChange: (name: string) => void
  onSlugChange: (slug: string) => void
  onSlugManualEdit: () => void
  namePlaceholder?: string
  nameId?: string
  slugId?: string
}

export function CrewNameSlugFields({
  name,
  slug,
  onNameChange,
  onSlugChange,
  onSlugManualEdit,
  namePlaceholder,
  nameId = "name",
  slugId = "slug",
}: CrewNameSlugFieldsProps) {
  return (
    <Card>
      <CardHeader><CardTitle className="text-base">Crew Name</CardTitle></CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-2">
            <Label htmlFor={nameId}>Name *</Label>
            <Input
              id={nameId}
              value={name}
              onChange={(e) => onNameChange(e.target.value)}
              placeholder={namePlaceholder}
              required
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor={slugId}>Slug</Label>
            <Input
              id={slugId}
              value={slug}
              onChange={(e) => { onSlugManualEdit(); onSlugChange(slugify(e.target.value)) }}
              className="font-mono text-sm"
              required
            />
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
