"use client"

import { useState, useEffect, useCallback } from "react"
import { useAgentId } from "@/hooks/use-agent-id"
import {
  Save, Loader2, AlertCircle, CheckCircle2,
  User, Hash, Users, FileText, Briefcase, Shield, Cpu,
  Wrench, Timer, MessageSquare, Image as ImageIcon,
  ChevronDown, Maximize2, ChevronRight,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { SectionCard } from "@/components/ui/section-card"
import { PropertyRow } from "@/components/layout/property-row"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useWorkspace } from "@/hooks/use-workspace"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS } from "@/lib/cli-adapters"
import { AvatarPicker } from "@/components/avatar-picker"
import { AvatarOverrideBadge } from "@/components/features/agents/settings/avatar-override-badge"
import { cn } from "@/lib/utils"

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

const MODEL_DESCRIPTIONS: Record<string, { description: string; badge?: string }> = {
  "claude-opus-4-20250514": { description: "Most capable · best for complex analysis", badge: "Reasoning" },
  "claude-sonnet-4-20250514": { description: "Balanced speed and capability · default", badge: "Balanced" },
  "claude-haiku-4-5-20251001": { description: "Fast and cheap · quick replies", badge: "Fast" },
  "o3": { description: "Frontier reasoning model", badge: "Reasoning" },
  "o4-mini": { description: "Smaller, faster reasoning", badge: "Fast" },
  "gpt-4o": { description: "Multimodal flagship", badge: "Multimodal" },
  "gemini-2.5-pro": { description: "Google's flagship · 1M-token context", badge: "Long ctx" },
  "gemini-2.5-flash": { description: "Faster, cheaper Gemini", badge: "Fast" },
}

export function SettingsPageClient() {
  const agentId = useAgentId()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [agent, setAgent] = useState<AgentDetail | null>(null)
  const [crews, setTeams] = useState<TeamOption[]>([])
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [systemPromptFullscreen, setSystemPromptFullscreen] = useState(false)

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
    if (!workspaceId || !agentId) return
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
    if (!workspaceId || !agentId) return

    setSubmitting(true)
    setError(null)
    setSuccess(null)

    const body: Record<string, unknown> = {
      name,
      agent_role: agentRole,
      cli_adapter: cliAdapter,
      tool_profile: toolProfile,
      timeout_seconds: parseInt(timeoutSeconds, 10),
      description: description || null,
      role_title: roleTitle || null,
      avatar_seed: avatarSeed || null,
      avatar_style: avatarStyle || null,
      llm_provider: llmProvider || null,
      llm_model: llmModel || null,
      system_prompt: systemPrompt || null,
      crew_id: crewId || null,
    }
    if (agentRole === "LEAD") body.lead_mode = leadMode

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

  if (wsLoading || loading) return <SettingsSkeleton />

  if (!agent && error) {
    return (
      <div className="p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  const adapterCfg = CLI_ADAPTERS[cliAdapter]
  const availableModels = adapterCfg?.models ?? []
  const modelMeta = llmModel ? MODEL_DESCRIPTIONS[llmModel] : undefined

  return (
    <div className="p-6 mx-auto w-full max-w-3xl space-y-6">
      <div>
        <h2 className="text-title font-semibold">Settings</h2>
        <p className="text-body text-muted-foreground mt-1">
          Identity, runtime, and system prompt for this agent.
        </p>
      </div>

      <form onSubmit={handleSave} className="space-y-6">
        {/* Identity + Avatar side-by-side on lg+, stacked on small */}
        <div className="grid gap-4 lg:grid-cols-[1fr_280px]">
          <SectionCard title={<span className="flex items-center gap-2"><User className="h-4 w-4 text-muted-foreground" />Identity</span>}>
            <div className="space-y-0">
              <PropertyRow label="Name" icon={User}>
                <Input value={name} onChange={(e) => setName(e.target.value)} required />
              </PropertyRow>
              <PropertyRow label="Slug" icon={Hash}>
                <Input value={agent?.slug ?? ""} disabled className="font-mono text-label opacity-60" />
              </PropertyRow>
              <PropertyRow label="Role title" icon={Briefcase}>
                <Input value={roleTitle} onChange={(e) => setRoleTitle(e.target.value)} placeholder="e.g. Senior Developer" />
              </PropertyRow>
              <PropertyRow label="Description" icon={FileText}>
                <Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={3} />
              </PropertyRow>
            </div>
          </SectionCard>

          <SectionCard title={<span className="flex items-center gap-2"><ImageIcon className="h-4 w-4 text-muted-foreground" />Avatar</span>}>
            <div className="space-y-3">
              <AvatarOverrideBadge
                agentId={agentId}
                workspaceId={workspaceId ?? ""}
                hasOverride={!!avatarStyle}
                onReset={() => setAvatarStyle("")}
              />
              <AvatarPicker
                seed={avatarSeed || agent?.name || ""}
                style={avatarStyle}
                onSeedChange={setAvatarSeed}
                onStyleChange={() => { /* style is crew-controlled */ }}
                lockedStyle={avatarStyle || agent?.crew?.avatar_style || "bottts-neutral"}
              />
            </div>
          </SectionCard>
        </div>

        {/* Crew & Role */}
        <SectionCard title={<span className="flex items-center gap-2"><Users className="h-4 w-4 text-muted-foreground" />Crew &amp; Role</span>}>
          <div className="space-y-0">
            <PropertyRow label="Crew" icon={Users}>
              {/* Radix Select rejects "" as a SelectItem value, so we use a
                  sentinel for "no crew" and translate at the boundary. */}
              <Select
                value={crewId === "" ? "__none__" : crewId}
                onValueChange={(v) => setTeamId(v === "__none__" ? "" : v)}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Select a crew" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__none__">Unassigned</SelectItem>
                  {crews.map((crew) => (
                    <SelectItem key={crew.id} value={crew.id}>{crew.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </PropertyRow>
            <PropertyRow label="Agent role" icon={Shield}>
              <div className="space-y-2">
                {agentRole === "COORDINATOR" && (
                  <div className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-200">
                    This agent is a <strong>COORDINATOR</strong> — a deprecated role
                    kept for backward compatibility. Selecting Agent or Lead below
                    will migrate it; the choice is one-way.
                  </div>
                )}
                <div className="flex flex-wrap gap-2">
                  {[
                    { id: "AGENT", label: "Agent", description: "Standard contributor" },
                    { id: "LEAD", label: "Lead", description: "Plans + assigns work in crew" },
                  ].map((r) => {
                    const active = agentRole === r.id
                    return (
                      <button
                        key={r.id}
                        type="button"
                        onClick={() => setAgentRole(r.id)}
                        className={cn(
                          "rounded-lg border px-3 py-2 text-left transition-colors min-w-[160px]",
                          active ? "border-primary bg-primary/5 text-foreground" : "border-border hover:bg-muted text-muted-foreground",
                        )}
                      >
                        <div className="text-body font-medium">{r.label}</div>
                        <div className="text-micro text-muted-foreground">{r.description}</div>
                      </button>
                    )
                  })}
                </div>
              </div>
            </PropertyRow>
            {agentRole === "LEAD" && (
              <PropertyRow label="Lead mode" icon={Shield}>
                <div className="space-y-2">
                  <Select value={leadMode} onValueChange={setLeadMode}>
                    <SelectTrigger className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="active">Active — autonomously plans and assigns</SelectItem>
                      <SelectItem value="passive">Passive — receives updates, manual task delegation</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </PropertyRow>
            )}
          </div>
        </SectionCard>

        {/* Runtime */}
        <SectionCard title={<span className="flex items-center gap-2"><Cpu className="h-4 w-4 text-muted-foreground" />Runtime</span>}>
          <div className="space-y-5">
            <div className="space-y-2">
              <Label className="text-label">Provider &amp; CLI adapter</Label>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                {CLI_ADAPTER_KEYS.map((key) => {
                  const cfg = CLI_ADAPTERS[key]
                  const Icon = cfg.icon
                  const isActive = cliAdapter === key
                  return (
                    <button
                      key={key}
                      type="button"
                      onClick={() => handleAdapterChange(key)}
                      className={cn(
                        "flex items-center gap-3 rounded-lg border p-3 text-left transition-colors",
                        isActive ? "border-primary bg-primary/5 ring-1 ring-primary/20" : "border-border hover:bg-muted",
                      )}
                    >
                      <Icon className={cn("h-6 w-6 shrink-0", isActive ? "text-primary" : "text-muted-foreground")} />
                      <div className="min-w-0 flex-1">
                        <div className="text-body font-medium flex items-center gap-1.5">
                          {cfg.label}
                          {isActive && <CheckCircle2 className="h-3.5 w-3.5 text-primary" />}
                        </div>
                        <div className="text-micro text-muted-foreground truncate">
                          <span className="font-mono">{cfg.provider}</span> · {cfg.description}
                        </div>
                      </div>
                    </button>
                  )
                })}
              </div>
            </div>

            <div className="space-y-2">
              <Label className="text-label">Model</Label>
              {showCustomModel ? (
                <div className="flex gap-2">
                  <Input
                    value={llmModel}
                    onChange={(e) => setLlmModel(e.target.value)}
                    placeholder="Enter custom model name"
                    className="font-mono text-label"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      setShowCustomModel(false)
                      if (adapterCfg) setLlmModel(adapterCfg.defaultModel)
                    }}
                  >
                    Use list
                  </Button>
                </div>
              ) : (
                <>
                  <Select value={llmModel} onValueChange={handleModelSelect}>
                    <SelectTrigger className="w-full font-mono text-label">
                      <SelectValue placeholder="Select a model" />
                    </SelectTrigger>
                    <SelectContent>
                      {availableModels.map((m) => {
                        const meta = MODEL_DESCRIPTIONS[m.value]
                        return (
                          <SelectItem key={m.value} value={m.value}>
                            <div className="flex flex-col gap-0.5 py-0.5 min-w-0">
                              <div className="flex items-center gap-2">
                                <span className="font-mono text-label">{m.label}</span>
                                {meta?.badge && (
                                  <span className="rounded bg-primary/10 text-primary px-1.5 py-px text-[10px] font-medium">
                                    {meta.badge}
                                  </span>
                                )}
                              </div>
                              {meta?.description && (
                                <span className="text-micro text-muted-foreground">{meta.description}</span>
                              )}
                            </div>
                          </SelectItem>
                        )
                      })}
                      <SelectItem value="__custom__" className="text-muted-foreground italic">
                        Custom model name…
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  {modelMeta && (
                    <p className="text-micro text-muted-foreground pl-1">
                      {modelMeta.description}
                    </p>
                  )}
                </>
              )}
            </div>

            <div className="space-y-0">
              <PropertyRow label="Tool profile" icon={Wrench}>
                <Select value={toolProfile} onValueChange={setToolProfile}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="MINIMAL">Minimal — read-only ops</SelectItem>
                    <SelectItem value="CODING">Coding — files + shell</SelectItem>
                    <SelectItem value="MESSAGING">Messaging — peers + status</SelectItem>
                    <SelectItem value="FULL">Full — everything available</SelectItem>
                  </SelectContent>
                </Select>
              </PropertyRow>
              <PropertyRow label="Timeout" icon={Timer}>
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    min={30}
                    max={7200}
                    value={timeoutSeconds}
                    onChange={(e) => setTimeoutSeconds(e.target.value)}
                  />
                  <span className="text-label text-muted-foreground shrink-0">seconds</span>
                </div>
              </PropertyRow>
            </div>
          </div>
        </SectionCard>

        {/* System Prompt */}
        <SectionCard
          title={
            <div className="flex items-center justify-between gap-2 w-full">
              <span className="flex items-center gap-2">
                <MessageSquare className="h-4 w-4 text-muted-foreground" />
                System Prompt
              </span>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 gap-1.5 text-muted-foreground hover:text-foreground"
                onClick={() => setSystemPromptFullscreen(true)}
              >
                <Maximize2 className="h-3.5 w-3.5" />
                Expand
              </Button>
            </div>
          }
          description="Sent as the system message on every run. Focus on behavior and constraints."
        >
          <div className="space-y-1.5">
            <Textarea
              value={systemPrompt}
              onChange={(e) => setSystemPrompt(e.target.value)}
              placeholder="You are a helpful AI assistant that…"
              rows={10}
              className="font-mono text-label leading-relaxed resize-y min-h-[180px]"
            />
            <div className="flex items-center justify-between text-micro text-muted-foreground px-1">
              <span>{systemPrompt.length.toLocaleString()} characters</span>
              <span>~{Math.ceil(systemPrompt.length / 4).toLocaleString()} tokens</span>
            </div>
          </div>
        </SectionCard>

        {/* Advanced (collapsible) */}
        <SectionCard
          title={
            <button
              type="button"
              onClick={() => setAdvancedOpen((v) => !v)}
              className="flex items-center gap-2 text-left w-full"
            >
              <ChevronRight className={cn("h-4 w-4 transition-transform", advancedOpen && "rotate-90")} />
              <span>Advanced</span>
              <span className="text-micro text-muted-foreground font-normal">
                Power-user settings
              </span>
            </button>
          }
        >
          {advancedOpen && (
            <div className="space-y-3">
              <PropertyRow label="LLM provider" icon={Cpu}>
                <Input
                  value={llmProvider}
                  onChange={(e) => setLlmProvider(e.target.value)}
                  placeholder="Auto-detected from adapter"
                  className="font-mono text-label"
                />
              </PropertyRow>
              <p className="text-micro text-muted-foreground pl-1">
                Memory budget, paymaster cap, and per-agent feature flags will land here. For now the
                provider override is the main escape hatch — change it only if you really know what
                you&apos;re doing.
              </p>
            </div>
          )}
        </SectionCard>

        {/* Status messages */}
        {error && (
          <div className="flex items-center gap-2 text-destructive">
            <AlertCircle className="h-4 w-4" />
            <p className="text-body">{error}</p>
          </div>
        )}
        {success && (
          <div className="flex items-center gap-2 rounded-md border border-border bg-surface-subtle px-3 py-2">
            <CheckCircle2 className="h-4 w-4 text-emerald-500" />
            <p className="text-body">{success}</p>
          </div>
        )}

        {/* Save (Delete moved to chat header 3-dots menu) */}
        <div className="flex items-center gap-3 pt-2">
          <Button type="submit" disabled={submitting} className="gap-2">
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
            Save Changes
          </Button>
          <span className="text-micro text-muted-foreground">
            Delete this agent from the chat header menu (⋮) — owners only.
          </span>
        </div>
      </form>

      {/* Fullscreen system-prompt editor */}
      <Dialog open={systemPromptFullscreen} onOpenChange={setSystemPromptFullscreen}>
        <DialogContent className="max-w-4xl h-[80vh] flex flex-col">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <MessageSquare className="h-4 w-4 text-muted-foreground" />
              System Prompt
            </DialogTitle>
          </DialogHeader>
          <Textarea
            value={systemPrompt}
            onChange={(e) => setSystemPrompt(e.target.value)}
            className="flex-1 font-mono text-label leading-relaxed resize-none"
          />
          <div className="flex items-center justify-between text-micro text-muted-foreground">
            <span>{systemPrompt.length.toLocaleString()} characters · ~{Math.ceil(systemPrompt.length / 4).toLocaleString()} tokens</span>
            <Button type="button" size="sm" onClick={() => setSystemPromptFullscreen(false)} className="gap-1.5">
              <ChevronDown className="h-3.5 w-3.5" />
              Done
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function SettingsSkeleton() {
  return (
    <div className="p-6 mx-auto w-full max-w-3xl space-y-6">
      <div className="space-y-2">
        <Skeleton className="h-7 w-32" />
        <Skeleton className="h-4 w-72" />
      </div>
      <div className="grid gap-4 lg:grid-cols-[1fr_280px]">
        <SectionCard title={<Skeleton className="h-5 w-24" />}>
          <div className="space-y-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-20 w-full" />
          </div>
        </SectionCard>
        <SectionCard title={<Skeleton className="h-5 w-24" />}>
          <Skeleton className="h-32 w-full" />
        </SectionCard>
      </div>
      {Array.from({ length: 3 }).map((_, i) => (
        <SectionCard key={i} title={<Skeleton className="h-5 w-32" />}>
          <div className="space-y-3">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        </SectionCard>
      ))}
      <Skeleton className="h-10 w-32" />
    </div>
  )
}
