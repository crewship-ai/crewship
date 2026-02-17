"use client"

import { use, useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
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

interface AgentDetail {
  id: string
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  status: string
  cli_adapter: string
  llm_provider: string | null
  llm_model: string | null
  system_prompt: string | null
  temperature: number | null
  max_tokens: number | null
  timeout_seconds: number
  tool_profile: string
  memory_enabled: boolean
  crew_id: string | null
  crew: { name: string; slug: string; color: string | null } | null
}

interface TeamOption {
  id: string
  name: string
  slug: string
}

export default function SettingsPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = use(params)
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
  const [temperature, setTemperature] = useState("0.7")
  const [maxTokens, setMaxTokens] = useState("")
  const [timeoutSeconds, setTimeoutSeconds] = useState("1800")
  const [toolProfile, setToolProfile] = useState("CODING")
  const [crewId, setTeamId] = useState("")

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
          setCliAdapter(agentData.cli_adapter)
          setLlmProvider(agentData.llm_provider ?? "")
          setLlmModel(agentData.llm_model ?? "")
          setSystemPrompt(agentData.system_prompt ?? "")
          setTemperature(agentData.temperature?.toString() ?? "0.7")
          setMaxTokens(agentData.max_tokens?.toString() ?? "")
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
      temperature: parseFloat(temperature),
      timeout_seconds: parseInt(timeoutSeconds, 10),
    }

    if (description) body.description = description
    if (roleTitle) body.role_title = roleTitle
    if (llmProvider) body.llm_provider = llmProvider
    if (llmModel) body.llm_model = llmModel
    if (systemPrompt) body.system_prompt = systemPrompt
    if (maxTokens) body.max_tokens = parseInt(maxTokens, 10)
    if (crewId) body.crew_id = crewId

    try {
      const res = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, {
        method: "PUT",
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
  }, [workspaceId, agentId, name, description, roleTitle, agentRole, cliAdapter, llmProvider, llmModel, systemPrompt, temperature, maxTokens, timeoutSeconds, toolProfile, crewId])

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
          <p className="text-sm">{error}</p>
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
            <CardTitle className="text-base">General</CardTitle>
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
          </CardContent>
        </Card>

        {/* Runtime */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Runtime</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="cli_adapter">CLI Adapter</Label>
                <Select value={cliAdapter} onValueChange={setCliAdapter}>
                  <SelectTrigger id="cli_adapter" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="CLAUDE_CODE">Claude Code</SelectItem>
                    <SelectItem value="OPENCODE">OpenCode</SelectItem>
                    <SelectItem value="CODEX_CLI">Codex CLI</SelectItem>
                    <SelectItem value="GEMINI_CLI">Gemini CLI</SelectItem>
                  </SelectContent>
                </Select>
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
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="llm_provider">LLM Provider</Label>
                <Select value={llmProvider} onValueChange={setLlmProvider}>
                  <SelectTrigger id="llm_provider" className="w-full">
                    <SelectValue placeholder="Select provider (optional)" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ANTHROPIC">Anthropic</SelectItem>
                    <SelectItem value="OPENAI">OpenAI</SelectItem>
                    <SelectItem value="GOOGLE">Google</SelectItem>
                    <SelectItem value="OLLAMA">Ollama</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label htmlFor="llm_model">LLM Model</Label>
                <Input
                  id="llm_model"
                  value={llmModel}
                  onChange={(e) => setLlmModel(e.target.value)}
                  placeholder="e.g. claude-sonnet-4-20250514"
                  className="font-mono text-sm"
                />
              </div>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
              <div className="space-y-2">
                <Label htmlFor="temperature">Temperature</Label>
                <Input
                  id="temperature"
                  type="number"
                  min={0}
                  max={2}
                  step={0.1}
                  value={temperature}
                  onChange={(e) => setTemperature(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="max_tokens">Max Tokens</Label>
                <Input
                  id="max_tokens"
                  type="number"
                  min={1}
                  value={maxTokens}
                  onChange={(e) => setMaxTokens(e.target.value)}
                  placeholder="Default"
                />
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
            </div>
          </CardContent>
        </Card>

        {/* System Prompt */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">System Prompt</CardTitle>
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
            <p className="text-sm">{error}</p>
          </div>
        )}
        {success && (
          <div className="flex items-center gap-2 text-emerald-600">
            <CheckCircle2 className="h-4 w-4" />
            <p className="text-sm">{success}</p>
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
