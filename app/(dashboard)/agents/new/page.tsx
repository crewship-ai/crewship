"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import { ArrowLeft, Bot, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { PageHeader } from "@/components/layout/page-header"
import { useWorkspace } from "@/hooks/use-workspace"
import { slugify } from "@/lib/utils/slugify"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS } from "@/lib/cli-adapters"

interface TeamOption {
  id: string
  name: string
  slug: string
}

export default function NewAgentPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [crews, setTeams] = useState<TeamOption[]>([])
  const [teamsLoading, setTeamsLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Form fields
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [slugManual, setSlugManual] = useState(false)
  const [crewId, setTeamId] = useState("")
  const [description, setDescription] = useState("")
  const [roleTitle, setRoleTitle] = useState("")
  const [agentRole, setAgentRole] = useState("AGENT")
  const [cliAdapter, setCliAdapter] = useState("CLAUDE_CODE")
  const [llmProvider, setLlmProvider] = useState("")
  const [llmModel, setLlmModel] = useState("")
  const [systemPrompt, setSystemPrompt] = useState("")
  const [toolProfile, setToolProfile] = useState("CODING")
  const [showCustomModel, setShowCustomModel] = useState(false)

  // Auto-generate slug from name
  useEffect(() => {
    if (!slugManual) {
      setSlug(slugify(name))
    }
  }, [name, slugManual])

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

  // Fetch crews when workspaceId is available
  useEffect(() => {
    if (!workspaceId) {
      setTeamsLoading(false)
      return
    }

    async function fetchTeams() {
      try {
        const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        if (res.ok) {
          const data: TeamOption[] = await res.json()
          setTeams(data)
        }
      } catch {
        // Silently fail — crews dropdown will be empty
      } finally {
        setTeamsLoading(false)
      }
    }

    fetchTeams()
  }, [workspaceId])

  const handleSubmit = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault()
      if (!workspaceId) return

      setSubmitting(true)
      setError(null)

      const body: Record<string, unknown> = {
        name,
        slug,
        crew_id: crewId,
        agent_role: agentRole,
        cli_adapter: cliAdapter,
        tool_profile: toolProfile,
      }

      if (description) body.description = description
      if (roleTitle) body.role_title = roleTitle
      if (llmProvider) body.llm_provider = llmProvider
      if (llmModel) body.llm_model = llmModel
      if (systemPrompt) body.system_prompt = systemPrompt

      try {
        const res = await fetch(`/api/v1/agents?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        })

        if (!res.ok) {
          const data = await res.json()
          const message =
            typeof data.error === "string"
              ? data.error
              : "Failed to create agent. Please check your input."
          setError(message)
          setSubmitting(false)
          return
        }

        router.push("/agents")
      } catch {
        setError("Network error. Please try again.")
        setSubmitting(false)
      }
    },
    [
      workspaceId,
      name,
      slug,
      crewId,
      description,
      roleTitle,
      agentRole,
      cliAdapter,
      llmProvider,
      llmModel,
      systemPrompt,
      toolProfile,
      router,
    ]
  )

  if (wsLoading) {
    return (
      <div className="flex items-center justify-center p-12">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
      <PageHeader title="New Agent" description="Create a new AI virtual employee">
        <Button variant="outline" size="sm" asChild>
          <Link href="/agents">
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back
          </Link>
        </Button>
      </PageHeader>

      <form onSubmit={handleSubmit} className="space-y-4 sm:space-y-6">
        {/* General */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">General</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="name">Name *</Label>
                <Input
                  id="name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="e.g. Claude — SEO Writer"
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="slug">Slug *</Label>
                <Input
                  id="slug"
                  value={slug}
                  onChange={(e) => {
                    setSlugManual(true)
                    setSlug(e.target.value)
                  }}
                  placeholder="claude-seo-writer"
                  className="font-mono text-sm"
                  required
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="crew_id">Crew *</Label>
              <Select value={crewId} onValueChange={setTeamId} required>
                <SelectTrigger id="crew_id" className="w-full">
                  <SelectValue placeholder={teamsLoading ? "Loading crews…" : "Select a crew"} />
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
                placeholder="What does this agent do?"
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
                        <div className="text-sm font-medium">{cfg.label}</div>
                        <div className="text-[10px] text-muted-foreground">{cfg.description}</div>
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
          </CardContent>
        </Card>

        {/* System Prompt */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">System Prompt</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-2">
              <Label htmlFor="system_prompt">Instructions for the agent</Label>
              <Textarea
                id="system_prompt"
                value={systemPrompt}
                onChange={(e) => setSystemPrompt(e.target.value)}
                placeholder="You are a helpful AI assistant that..."
                rows={6}
              />
            </div>
          </CardContent>
        </Card>

        {/* Error message */}
        {error && (
          <p className="text-sm text-destructive">{error}</p>
        )}

        {/* Actions */}
        <div className="flex items-center gap-3 pt-2">
          <Button type="submit" disabled={submitting || !workspaceId} className="gap-2">
            {submitting ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Bot className="h-4 w-4" />
            )}
            Create Agent
          </Button>
          <Button type="button" variant="outline" asChild>
            <Link href="/agents">Cancel</Link>
          </Button>
        </div>
      </form>
    </div>
  )
}
