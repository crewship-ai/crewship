"use client"

import { useEffect } from "react"
import { useQuery } from "@tanstack/react-query"
import { FileText } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"

/** A choice for the next human Send, never a grant inferred from model text. */
export function DocumentCreationChoice({ workspaceId, selected, onChange, disabled }: {
  workspaceId: string
  selected: boolean
  onChange: (selected: boolean) => void
  disabled: boolean
}) {
  const availability = useQuery({
    queryKey: ["document-status", workspaceId],
    enabled: Boolean(workspaceId),
    retry: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/documents/status?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) return false
      const body = await response.json()
      return body.enabled === true
    },
  })
  useEffect(() => {
    if (availability.data === false && selected) onChange(false)
  }, [availability.data, selected, onChange])
  if (availability.data !== true) return null
  return <div className="px-4 pb-2 md:px-6">
    <Button type="button" size="sm" variant={selected ? "secondary" : "ghost"} className="coarse:min-h-12" aria-pressed={selected} disabled={disabled} onClick={() => onChange(!selected)}>
      <FileText className="mr-2 h-4 w-4" aria-hidden />Create document
    </Button>
    {selected && <p className="mt-1 text-xs text-muted-foreground">Send your request to get a private document you can open and edit.</p>}
  </div>
}
