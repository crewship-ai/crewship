"use client"

import { use, useState, useEffect, useCallback } from "react"
import { useRouter , useParams} from "next/navigation"
import { Save, Trash2, Loader2, AlertCircle, CheckCircle2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useWorkspace } from "@/hooks/use-workspace"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS } from "@/lib/cli-adapters"
import { AvatarPicker } from "@/components/avatar-picker"

interface AgentDetail {
  id: string
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  lead_mode: string | null
  status: string
  cli_adapter: string
  llm_provider: string | null
  llm_model: string | null
  system_prompt: string | null
  avatar_seed: string | null
  avatar_style: string | null
  timeout_seconds: number
  tool_profile: string
  memory_enabled: boolean
  crew_id: string | null
  crew: { name: string; slug: string; color: string | null; avatar_style: string | null } | null
}

interface TeamOption {
  id: string
  name: string
  slug: string
}

export function SettingsPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [agent, setAgent] = useState<AgentDetail | null>(null)
  const [crews, setTeams] = useState<TeamOption[]>([])
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)

  // Form fields
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [roleTitle, setRoleTitle] = useState("")
  const [agentRole, setAgentRole] = useState("AGENT")
  const [cliAdapter, setCliAdapter] = useState("CLAUDE_CODE")
  const [llmProvider, setLlmProvider] = useState("")
  const [llmModel, setLlmModel] = useState("")
  const [systemPrompt, setSystemPrompt] = useState("")
  const [timeoutSeconds, setTimeoutSeconds] = useState("1800")
  const [toolProfile, setToolProfile] = useState("CODING")
  const [leadMode, setLeadMode] = useState("active")
  const [crewId, setTeamId] = useState("")
  const [showCustomModel, setShowCustomModel] = useState(false)
  const [avatarSeed, setAvatarSeed] = useState("")
  const [avatarStyle, setAvatarStyle] = useState("")

  function handleAdapterChange(key: string) {
    setCliAdapter(key)
    const cfg = CLI_ADAPTERS[key]
    if (cfg) {
      setLlmProvider(cfg.provider)
      setLlmModel(cfg.defaultModel)
      setShowCustomModel(false)
    }
  }

  function handleModelSelect(value: string) {
    if (value === "__custom__") {
      setShowCustomModel(true)
      setLlmModel("")
    } else {
      setShowCustomModel(false)
      setLlmModel(value)
    }
  }

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false

    async function fetchData() {
      try {
        const [agentRes, teamsRes] = await Promise.all([
          fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`),
          fetch(`/api/v1/crews?workspace_id=${workspaceId}`),
        ])

        if (!agentRes.ok) {
          if (!cancelled) setError("Failed to load agent")
          return
        }

        const agentData: AgentDetail = await agentRes.json()
        if (!cancelled) {
          setAgent(agentData)
          setName(agentData.name)
          setDescription(agentData.description ?? "")
          setRoleTitle(agentData.role_title ?? "")
          setAgentRole(agentData.agent_role)
          setLeadMode(agentData.lead_mode ?? "active")
          setCliAdapter(agentData.cli_adapter)
          setLlmProvider(agentData.llm_provider ?? "")
          setLlmModel(agentData.llm_model ?? "")
          const adapterModels = CLI_ADAPTERS[agentData.cli_adapter]?.models ?? []
          if (agentData.llm_model && !adapterModels.some((m) => m.value === agentData.llm_model)) {
            setShowCustomModel(true)
          }
          setSystemPrompt(agentData.system_prompt ?? "")
          setAvatarSeed(agentData.avatar_seed ?? "")
          setAvatarStyle(agentData.avatar_style ?? "")
          setTimeoutSeconds(agentData.timeout_seconds.toString())
          setToolProfile(agentData.tool_profile)
          setTeamId(agentData.crew_id ?? "")
        }

        if (teamsRes.ok) {
          const teamsData: TeamOption[] = await teamsRes.json()
          if (!cancelled) setTeams(teamsData)
        }
      } catch {
        if (!cancelled) setError("Network error. Please try again.")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  const handleSave = useCallback(async (e: React.FormEvent) => {
    e.preventDefault()
    if (!workspaceId) return

    setSubmitting(true)
    setError(null)
    setSuccess(null)

    const body: Record<string, unknown> = {
      name,
      agent_role: agentRole,
      cli_adapter: cliAdapter,
      tool_profile: toolProfile,
      timeout_seconds: parseInt(timeoutSeconds, 10),
    }

    if (description) body.description = description
    if (roleTitle) body.role_title = roleTitle
    body.avatar_seed = avatarSeed || null
    body.avatar_style = avatarStyle || null
    if (agentRole === "LEAD") body.lead_mode = leadMode
    if (llmProvider) body.llm_provider = llmProvider
    if (llmModel) body.llm_model = llmModel
    if (systemPrompt) body.system_prompt = systemPrompt
    if (crewId) body.crew_id = crewId

    try {
      const res = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })

      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to save changes" }))
        setError(typeof data.error === "string" ? data.error : "Failed to save changes. Please check your input.")
        return
      }

      setSuccess("Changes saved successfully.")
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setSubmitting(false)
    }
  }, [workspaceId, agentId, name, description, roleTitle, agentRole, leadMode, cliAdapter, llmProvider, llmModel, systemPrompt, timeoutSeconds, toolProfile, crewId, avatarSeed, avatarStyle])

  const handleDelete = useCallback(async () => {
    if (!workspaceId) return
    if (!confirm("Are you sure you want to delete this agent? This action cannot be undone.")) return

    setDeleting(true)
    setError(null)
    setSuccess(null)

    try {
      const res = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })

      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to delete agent" }))
        setError(typeof data.error === "string" ? data.error : "Failed to delete agent")
        return
      }

      router.push("/agents")
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setDeleting(false)
    }
  }, [workspaceId, agentId, router])

  if (wsLoading || loading) {
    return <SettingsSkeleton />
  }

  if (!agent && error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
      <form onSubmit={handleSave} className="space-y-4 sm:space-y-6">
        {/* General */}
        <Card>
          <CardHeader>
            <CardTitle className="text-default">General</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="name">Agent Name</Label>
                <Input
                  id="name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="slug">Slug</Label>
                <Input
                  id="slug"
                  value={agent?.slug ?? ""}
                  disabled
                  className="font-mono text-sm opacity-60"
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="crew_id">Crew</Label>
              <Select value={crewId} onValueChange={setTeamId}>
                <SelectTrigger id="crew_id" className="w-full">
                  <SelectValue placeholder="Select a crew" />
                </SelectTrigger>
                <SelectContent>
                  {crews.map((crew) => (
                    <SelectItem key={crew.id} value={crew.id}>
                      {crew.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="description">Description</Label>
              <Textarea
                id="description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={3}
              />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="role_title">Role Title</Label>
                <Input
                  id="role_title"
                  value={roleTitle}
                  onChange={(e) => setRoleTitle(e.target.value)}
                  placeholder="e.g. Senior Developer"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="agent_role">Agent Role</Label>
                <Select value={agentRole} onValueChange={setAgentRole}>
                  <SelectTrigger id="agent_role" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="AGENT">Agent</SelectItem>
                    <SelectItem value="LEAD">Lead</SelectItem>
                    <SelectItem value="COORDINATOR">Coordinator</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            {agentRole === "LEAD" && (
              <div className="space-y-2">
                <Label htmlFor="lead_mode">Lead Mode</Label>
                <Select value={leadMode} onValueChange={setLeadMode}>
                  <SelectTrigger id="lead_mode" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="active">Active</SelectItem>
                    <SelectItem value="passive" disabled>Passive (Phase 2)</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-label text-muted-foreground">
                  Active: Lead receives crew context and can orchestrate tasks.
                </p>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Avatar */}
        <Card>
          <CardHeader>
            <CardTitle className="text-default">Avatar</CardTitle>
          </CardHeader>
          <CardContent>
            <AvatarPicker
              seed={avatarSeed || agent?.name || ""}
              style={avatarStyle}
              onSeedChange={setAvatarSeed}
              onStyleChange={setAvatarStyle}
              lockedStyle={!avatarStyle ? agent?.crew?.avatar_style : undefined}
            />
          </CardContent>
        </Card>

        {/* Runtime */}
        <Card>
          <CardHeader>
            <CardTitle className="text-default">Runtime</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label>CLI Adapter</Label>
              <div className="grid grid-cols-2 gap-2">
                {CLI_ADAPTER_KEYS.map((key) => {
                  const cfg = CLI_ADAPTERS[key]
                  const Icon = cfg.icon
                  const isActive = cliAdapter === key
                  return (
                    <button
                      key={key}
                      type="button"
                      onClick={() => handleAdapterChange(key)}
                      className={`flex items-center gap-3 rounded-lg border p-3 text-left transition-colors ${
                        isActive ? "border-primary bg-primary/5" : "border-border hover:bg-muted"
                      }`}
                    >
                      <Icon className={`h-5 w-5 shrink-0 ${isActive ? "text-primary" : "text-muted-foreground"}`} />
                      <div className="min-w-0">
                        <div className="text-body font-medium">{cfg.label}</div>
                        <div className="text-micro text-muted-foreground">{cfg.description}</div>
                      </div>
                    </button>
                  )
                })}
              </div>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Model</Label>
                {showCustomModel ? (
                  <div className="flex gap-2">
                    <Input
                      value={llmModel}
                      onChange={(e) => setLlmModel(e.target.value)}
                      placeholder="Enter model name"
                      className="font-mono text-xs"
                    />
                    <Button type="button" variant="outline" size="sm" onClick={() => {
                      setShowCustomModel(false)
                      const cfg = CLI_ADAPTERS[cliAdapter]
                      if (cfg) setLlmModel(cfg.defaultModel)
                    }}>
                      Back
                    </Button>
                  </div>
                ) : (
                  <Select value={llmModel} onValueChange={handleModelSelect}>
                    <SelectTrigger className="w-full font-mono text-xs">
                      <SelectValue placeholder="Select model" />
                    </SelectTrigger>
                    <SelectContent>
                      {(CLI_ADAPTERS[cliAdapter]?.models ?? []).map((m) => (
                        <SelectItem key={m.value} value={m.value} className="font-mono text-xs">
                          {m.label}
                        </SelectItem>
                      ))}
                      <SelectItem value="__custom__" className="text-muted-foreground">
                        Custom...
                      </SelectItem>
                    </SelectContent>
                  </Select>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="tool_profile">Tool Profile</Label>
                <Select value={toolProfile} onValueChange={setToolProfile}>
                  <SelectTrigger id="tool_profile" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="MINIMAL">Minimal</SelectItem>
                    <SelectItem value="CODING">Coding</SelectItem>
                    <SelectItem value="MESSAGING">Messaging</SelectItem>
                    <SelectItem value="FULL">Full</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="timeout">Timeout (seconds)</Label>
              <Input
                id="timeout"
                type="number"
                min={30}
                max={7200}
                value={timeoutSeconds}
                onChange={(e) => setTimeoutSeconds(e.target.value)}
              />
            </div>
          </CardContent>
        </Card>

        {/* System Prompt */}
        <Card>
          <CardHeader>
            <CardTitle className="text-default">System Prompt</CardTitle>
          </CardHeader>
          <CardContent>
            <Textarea
              id="system_prompt"
              value={systemPrompt}
              onChange={(e) => setSystemPrompt(e.target.value)}
              placeholder="You are a helpful AI assistant that..."
              rows={6}
            />
          </CardContent>
        </Card>

        {/* Messages */}
        {error && (
          <div className="flex items-center gap-2 text-destructive">
            <AlertCircle className="h-4 w-4" />
            <p className="text-body">{error}</p>
          </div>
        )}
        {success && (
          <div className="flex items-center gap-2 text-emerald-600">
            <CheckCircle2 className="h-4 w-4" />
            <p className="text-body">{success}</p>
          </div>
        )}

        {/* Actions */}
        <div className="flex flex-wrap items-center gap-3 pt-2">
          <Button type="submit" disabled={submitting} className="gap-2">
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
            Save Changes
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={deleting}
            onClick={handleDelete}
            className="gap-2 text-destructive border-destructive/30 hover:bg-destructive/10"
          >
            {deleting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
            Delete Agent
          </Button>
        </div>
      </form>
    </div>
  )
}

function SettingsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
      {Array.from({ length: 3 }).map((_, i) => (
        <Card key={i}>
          <CardHeader>
            <Skeleton className="h-5 w-24" />
          </CardHeader>
          <CardContent className="space-y-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </CardContent>
        </Card>
      ))}
      <div className="flex gap-3">
        <Skeleton className="h-10 w-32" />
        <Skeleton className="h-10 w-32" />
      </div>
    </div>
  )
}
