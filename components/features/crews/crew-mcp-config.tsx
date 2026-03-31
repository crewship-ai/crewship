"use client"

import { useEffect, useState, useCallback } from "react"
import { Plug, Loader2, ChevronDown } from "lucide-react"
import {
  Card, CardContent, CardHeader,
} from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import { MCPConfigEditor } from "@/components/features/mcp/mcp-config-editor"

interface CrewMCPConfigProps {
  crewId: string
  workspaceId: string
}

interface CrewData {
  id: string
  mcp_config_json: string | null
}

function countServers(json: string): number {
  if (!json || json.trim() === "") return 0
  try {
    const parsed = JSON.parse(json)
    return Object.keys(parsed.mcpServers ?? {}).length
  } catch {
    return 0
  }
}

export function CrewMCPConfig({ crewId, workspaceId }: CrewMCPConfigProps) {
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [configJson, setConfigJson] = useState("")
  const [savedJson, setSavedJson] = useState("")
  const [open, setOpen] = useState(false)

  useEffect(() => {
    let cancelled = false

    async function fetchCrew() {
      setLoading(true)
      setConfigJson("")
      setSavedJson("")

      try {
        const res = await fetch(
          `/api/v1/crews/${crewId}?workspace_id=${workspaceId}`,
        )
        if (!res.ok) {
          if (!cancelled) {
            toast.error("Failed to load crew MCP configuration")
            setOpen(false)
          }
          return
        }
        const data: CrewData = await res.json()
        if (!cancelled) {
          const json = data.mcp_config_json ?? ""
          setConfigJson(json)
          setSavedJson(json)
          setLoading(false)
          setOpen(countServers(json) > 0)
        }
      } catch {
        if (!cancelled) {
          toast.error("Network error loading MCP configuration")
          setOpen(false)
        }
      }
    }

    fetchCrew()
    return () => {
      cancelled = true
    }
  }, [crewId, workspaceId])

  const handleSave = useCallback(async () => {
    setSaving(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}?workspace_id=${workspaceId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mcp_config_json: configJson || null }),
        },
      )
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to save" }))
        toast.error(
          typeof data.error === "string" ? data.error : "Failed to save MCP configuration",
        )
        return
      }
      setSavedJson(configJson)
      toast.success("MCP configuration saved")
    } catch {
      toast.error("Network error saving MCP configuration")
    } finally {
      setSaving(false)
    }
  }, [crewId, workspaceId, configJson])

  const hasChanges = configJson !== savedJson
  const serverCount = countServers(configJson)

  if (loading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-5 w-32" />
          <Skeleton className="h-4 w-64" />
        </CardHeader>
        <CardContent className="space-y-4">
          <Skeleton className="h-32 w-full" />
        </CardContent>
      </Card>
    )
  }

  return (
    <Card>
      <Collapsible open={open} onOpenChange={setOpen}>
        <CardHeader className="pb-3">
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="flex items-center justify-between w-full text-left group"
            >
              <div className="flex items-center gap-2">
                <Plug className="h-4 w-4 text-muted-foreground" />
                <span className="text-sm font-semibold">MCP Servers</span>
                {serverCount > 0 && (
                  <span className="text-xs text-muted-foreground">
                    ({serverCount} active)
                  </span>
                )}
              </div>
              <ChevronDown
                className={cn(
                  "h-4 w-4 text-muted-foreground transition-transform",
                  open && "rotate-180",
                )}
              />
            </button>
          </CollapsibleTrigger>
        </CardHeader>

        <CollapsibleContent>
          <CardContent className="space-y-4 pt-0">
            <p className="text-xs text-muted-foreground">
              Configure Model Context Protocol servers shared by all agents in this crew.
              Agent-level configurations are merged on top of crew settings.
            </p>

            <MCPConfigEditor value={configJson} onChange={setConfigJson} workspaceId={workspaceId} />

            {hasChanges && (
              <Button size="sm" onClick={handleSave} disabled={saving} className="gap-1.5">
                {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {saving ? "Saving..." : "Save MCP Configuration"}
              </Button>
            )}
          </CardContent>
        </CollapsibleContent>
      </Collapsible>
    </Card>
  )
}
