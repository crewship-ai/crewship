"use client"

import { useEffect, useState, useCallback } from "react"
import Link from "next/link"
import { Plug, Terminal, Globe, ArrowRight } from "lucide-react"
import {
  Card, CardContent, CardHeader,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"

interface CrewMCPServer {
  id: string
  name: string
  display_name: string
  transport: string
  command: string | null
  enabled: boolean
}

interface CrewMCPConfigProps {
  crewId: string
  workspaceId: string
}

export function CrewMCPConfig({ crewId, workspaceId }: CrewMCPConfigProps) {
  const [loading, setLoading] = useState(true)
  const [servers, setServers] = useState<CrewMCPServer[]>([])

  const fetchIntegrations = useCallback(async () => {
    setLoading(true)
    try {
      const res = await apiFetch(
        `/api/v1/crews/${crewId}/integrations?workspace_id=${workspaceId}`,
      )
      if (!res.ok) {
        toast.error("Failed to load crew MCP servers")
        return
      }
      const data: CrewMCPServer[] = await res.json()
      setServers(data)
    } catch {
      toast.error("Network error loading MCP servers")
    } finally {
      setLoading(false)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    fetchIntegrations()
  }, [fetchIntegrations])

  if (loading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-5 w-32" />
        </CardHeader>
        <CardContent className="space-y-2">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </CardContent>
      </Card>
    )
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Plug className="h-4 w-4 text-muted-foreground" />
            <span className="text-sm font-semibold">MCP Servers</span>
            {servers.length > 0 && (
              <span className="text-xs text-muted-foreground">
                ({servers.length})
              </span>
            )}
          </div>
          <Link
            href="/integrations"
            className="text-xs text-muted-foreground hover:text-foreground transition-colors flex items-center gap-1"
          >
            Manage in Integrations
            <ArrowRight className="h-3 w-3" />
          </Link>
        </div>
      </CardHeader>

      <CardContent className="pt-0">
        {servers.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No MCP servers configured.{" "}
            <Link
              href="/integrations"
              className="text-primary hover:underline inline-flex items-center gap-1"
            >
              Add integrations on the Integrations page
              <ArrowRight className="h-3 w-3" />
            </Link>
          </p>
        ) : (
          <div className="divide-y divide-border rounded-md border">
            {servers.map((server) => (
              <div
                key={server.id}
                className="flex items-center justify-between px-3 py-2.5"
              >
                <div className="flex items-center gap-2">
                  {server.transport === "stdio" ? (
                    <Terminal className="h-4 w-4 text-muted-foreground" />
                  ) : (
                    <Globe className="h-4 w-4 text-muted-foreground" />
                  )}
                  <span className="text-sm font-medium">
                    {server.display_name}
                  </span>
                </div>
                <div className="flex items-center gap-3">
                  <span className="text-xs text-muted-foreground capitalize">
                    {server.transport === "stdio" ? "Stdio" : "HTTP"}
                  </span>
                  <Link
                    href="/integrations"
                    className="text-muted-foreground hover:text-foreground transition-colors"
                    aria-label={`Manage ${server.display_name} in Integrations`}
                  >
                    <ArrowRight className="h-4 w-4" />
                  </Link>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
