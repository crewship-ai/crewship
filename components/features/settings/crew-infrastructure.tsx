"use client"

import { useEffect, useState, useCallback } from "react"
import { Container, Network, RefreshCw, Square, Play, Activity } from "lucide-react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { CrewContainerConfig } from "@/components/features/crews/crew-container-config"
import { CrewNetworkPolicy } from "@/components/features/crews/crew-network-policy"
import { CrewConnections } from "@/components/features/orchestration/crew-connections"
import { toast } from "sonner"

interface Crew {
  id: string
  name: string
  slug: string
  icon: string
  color: string
  container_memory_mb: number
  container_cpus: number
  container_ttl_hours: number | null
  network_mode: string
  allowed_domains: string[]
}

interface ContainerStatus {
  crew_id: string
  status: string
  uptime: string
}

interface CrewInfrastructureProps {
  workspaceId: string
  canEdit: boolean
}

export function CrewInfrastructure({ workspaceId, canEdit }: CrewInfrastructureProps) {
  const [crews, setCrews] = useState<Crew[]>([])
  const [statuses, setStatuses] = useState<Record<string, ContainerStatus>>({})
  const [loading, setLoading] = useState(true)
  const [activeSection, setActiveSection] = useState<"overview" | "connections">("overview")

  const fetchCrews = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setCrews(data.data || data || [])
      }
    } catch {
      toast.error("Failed to load crews")
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  const fetchStatuses = useCallback(async () => {
    for (const crew of crews) {
      try {
        const res = await fetch(`/api/v1/crews/${crew.id}/container/status?workspace_id=${workspaceId}`)
        if (res.ok) {
          const data = await res.json()
          setStatuses(prev => ({ ...prev, [crew.id]: data }))
        }
      } catch {
        // Container status may not be available if container isn't running
      }
    }
  }, [crews, workspaceId])

  useEffect(() => { fetchCrews() }, [fetchCrews])
  useEffect(() => { if (crews.length > 0) fetchStatuses() }, [crews.length, fetchStatuses])

  async function handleContainerAction(crewId: string, action: "start" | "stop" | "restart") {
    try {
      const endpoint = action === "start"
        ? `/api/v1/crews/${crewId}/container/start?workspace_id=${workspaceId}`
        : action === "stop"
        ? `/api/v1/crews/${crewId}/container/stop?workspace_id=${workspaceId}`
        : `/api/v1/crews/${crewId}/container/restart?workspace_id=${workspaceId}`

      const res = await fetch(endpoint, { method: "POST" })
      if (res.ok) {
        toast.success(`Container ${action}ed`)
        fetchStatuses()
      } else {
        const body = await res.json().catch(() => null)
        toast.error(body?.error || `Failed to ${action} container`)
      }
    } catch {
      toast.error(`Failed to ${action} container`)
    }
  }

  async function handleSaveCrewConfig(crewId: string, config: Record<string, unknown>) {
    const res = await fetch(`/api/v1/crews/${crewId}?workspace_id=${workspaceId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(config),
    })
    if (!res.ok) {
      const body = await res.json().catch(() => null)
      throw new Error(body?.error || "Failed to save")
    }
    fetchCrews()
  }

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {/* Section toggle */}
      <div className="flex gap-1 p-0.5 bg-muted rounded-lg w-fit">
        <button
          className={`px-3 py-1.5 text-xs font-medium rounded-md transition-colors ${
            activeSection === "overview" ? "bg-background shadow-sm" : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => setActiveSection("overview")}
        >
          <Container className="h-3 w-3 inline mr-1.5" />
          Crew Overview
        </button>
        <button
          className={`px-3 py-1.5 text-xs font-medium rounded-md transition-colors ${
            activeSection === "connections" ? "bg-background shadow-sm" : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => setActiveSection("connections")}
        >
          <Network className="h-3 w-3 inline mr-1.5" />
          Connections
        </button>
      </div>

      {activeSection === "connections" && (
        <CrewConnections workspaceId={workspaceId} />
      )}

      {activeSection === "overview" && (
        <>
          {crews.length === 0 ? (
            <Card>
              <CardContent className="p-6 text-center">
                <p className="text-sm text-muted-foreground">No crews in this workspace yet.</p>
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-4">
              {crews.map((crew) => {
                const status = statuses[crew.id]
                const isRunning = status?.status === "running"

                return (
                  <Card key={crew.id}>
                    <CardHeader className="pb-3">
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2">
                          <CardTitle className="text-sm">{crew.name}</CardTitle>
                          <Badge variant="outline" className="text-[10px]">{crew.slug}</Badge>
                          {status && (
                            <Badge
                              variant={isRunning ? "default" : "secondary"}
                              className="text-[10px]"
                            >
                              <Activity className="h-2.5 w-2.5 mr-1" />
                              {status.status}
                            </Badge>
                          )}
                        </div>
                        {canEdit && (
                          <div className="flex gap-1">
                            {isRunning ? (
                              <>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs"
                                  onClick={() => handleContainerAction(crew.id, "restart")}
                                >
                                  <RefreshCw className="h-3 w-3 mr-1" />
                                  Restart
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs text-destructive hover:text-destructive"
                                  onClick={() => handleContainerAction(crew.id, "stop")}
                                >
                                  <Square className="h-3 w-3 mr-1" />
                                  Stop
                                </Button>
                              </>
                            ) : (
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 text-xs"
                                onClick={() => handleContainerAction(crew.id, "start")}
                              >
                                <Play className="h-3 w-3 mr-1" />
                                Start
                              </Button>
                            )}
                          </div>
                        )}
                      </div>
                      {status?.uptime && (
                        <CardDescription className="text-[10px]">Uptime: {status.uptime}</CardDescription>
                      )}
                    </CardHeader>
                    <CardContent className="space-y-4">
                      <div>
                        <h4 className="text-xs font-medium mb-2">Resources</h4>
                        <CrewContainerConfig
                          memoryMb={crew.container_memory_mb}
                          cpus={crew.container_cpus}
                          ttlHours={crew.container_ttl_hours}
                          canEdit={canEdit}
                          onSave={(config) => handleSaveCrewConfig(crew.id, config)}
                        />
                      </div>
                      <div className="border-t pt-4">
                        <h4 className="text-xs font-medium mb-2">Network Policy</h4>
                        <CrewNetworkPolicy
                          networkMode={crew.network_mode || "free"}
                          allowedDomains={crew.allowed_domains || []}
                          canEdit={canEdit}
                          onSave={async (mode, domains) => {
                            await handleSaveCrewConfig(crew.id, {
                              network_mode: mode,
                              allowed_domains: domains,
                            })
                          }}
                        />
                      </div>
                    </CardContent>
                  </Card>
                )
              })}
            </div>
          )}
        </>
      )}
    </div>
  )
}
