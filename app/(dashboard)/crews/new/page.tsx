"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import {
  ArrowLeft, Loader2, Users, Sparkles, Bot, ChevronRight, ChevronDown, RefreshCw, AlertTriangle,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { Badge } from "@/components/ui/badge"
import { PageHeader } from "@/components/layout/page-header"
import { useWorkspace } from "@/hooks/use-workspace"
import { slugify } from "@/lib/utils/slugify"
import { toast } from "sonner"

// ─── Types ────────────────────────────────────────────────────────────────────

interface CrewTemplateAgent {
  name: string
  slug: string
  role_title: string
  agent_role: string
  system_prompt: string
}

interface CrewTemplate {
  id: string
  name: string
  slug: string
  description: string | null
  icon: string | null
  color: string | null
  category: string
  agents: CrewTemplateAgent[]
  is_builtin: boolean
}

interface AISuggestedAgent {
  name: string
  slug: string
  role_title: string
  agent_role: string
  system_prompt: string
}

interface AISuggestion {
  crew_name: string
  crew_slug: string
  description: string
  agents: AISuggestedAgent[]
}

type Mode = "choose" | "ai" | "ai-preview" | "template" | "manual" | "no-coordinator"

// ─── AgentRow (expandable system prompt) ─────────────────────────────────────

function AgentRow({ agent }: { agent: { name: string; slug: string; role_title: string; agent_role: string; system_prompt: string } }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="rounded-lg border border-border overflow-hidden">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full flex items-center gap-3 p-3 text-left hover:bg-accent transition-colors"
      >
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-sm">{agent.name}</span>
            <Badge variant={agent.agent_role === "LEAD" ? "default" : "secondary"} className="text-xs">
              {agent.agent_role === "LEAD" ? "Lead" : agent.agent_role}
            </Badge>
            <span className="text-xs text-muted-foreground">{agent.role_title}</span>
          </div>
        </div>
        <ChevronDown className={`h-3.5 w-3.5 text-muted-foreground transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open && (
        <div className="px-3 pb-3 border-t border-border bg-muted/30">
          <p className="text-xs text-muted-foreground mt-2 whitespace-pre-wrap leading-relaxed">{agent.system_prompt}</p>
        </div>
      )}
    </div>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function NewCrewPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [mode, setMode] = useState<Mode>("choose")
  const [submitting, setSubmitting] = useState(false)

  // Template state
  const [templates, setTemplates] = useState<CrewTemplate[]>([])
  const [loadingTemplates, setLoadingTemplates] = useState(true)
  const [selectedTemplate, setSelectedTemplate] = useState<CrewTemplate | null>(null)

  // AI wizard state
  const [aiDescription, setAiDescription] = useState("")
  const [aiSuggesting, setAiSuggesting] = useState(false)
  const [aiSuggestion, setAiSuggestion] = useState<AISuggestion | null>(null)
  const [hasAnthropicKey, setHasAnthropicKey] = useState<boolean | null>(null) // null = unknown
  const [findingCoordinator, setFindingCoordinator] = useState(false)

  // Form state (shared between manual + deploy modes)
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [slugManual, setSlugManual] = useState(false)
  const [description, setDescription] = useState("")
  const [color, setColor] = useState("#3B82F6")
  const [icon, setIcon] = useState("")

  useEffect(() => {
    if (!slugManual) setSlug(slugify(name))
  }, [name, slugManual])

  // Fetch templates once workspaceId is available
  useEffect(() => {
    if (!workspaceId) return
    fetch(`/api/v1/crew-templates?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data) => setTemplates(Array.isArray(data) ? data : []))
      .catch(() => setTemplates([]))
      .finally(() => setLoadingTemplates(false))
  }, [workspaceId])

  // Check if workspace has an Anthropic key (by attempting a probe — we get 422 if not)
  useEffect(() => {
    if (!workspaceId) return
    // We infer key presence from whether /crew-ai-suggest returns 422
    // Don't call it yet — just mark as unknown; we'll know on first attempt
    setHasAnthropicKey(null)
  }, [workspaceId])

  const handleCreateWithAI = async () => {
    if (!workspaceId) return
    setFindingCoordinator(true)
    try {
      const res = await fetch(`/api/v1/agents?workspace_id=${workspaceId}&role=COORDINATOR`)
      if (res.ok) {
        const data = await res.json()
        const agents: Array<{ id: string; agent_role: string }> = Array.isArray(data) ? data : []
        const coordinator = agents.find((a) => a.agent_role === "COORDINATOR")
        if (coordinator) {
          const prefill = encodeURIComponent(
            "I need you to create a new crew for me. Please describe what kind of crew you want and I will design the agents, roles, and system prompts, then create everything for you.\n\nWhat should the crew do?"
          )
          router.push(`/agents/${coordinator.id}/chat?prefill=${prefill}&workspace_id=${workspaceId}`)
          return
        }
      }
      setMode("no-coordinator")
    } catch {
      setMode("no-coordinator")
    } finally {
      setFindingCoordinator(false)
    }
  }

  const handleSelectTemplate = (t: CrewTemplate) => {
    setSelectedTemplate(t)
    setName(t.name)
    setSlugManual(false)
    setDescription(t.description || "")
    setColor(t.color || "#3B82F6")
    setIcon(t.icon || "")
    setMode("template")
  }

  // ── AI suggest ──────────────────────────────────────────────────────────────

  const handleAISuggest = async () => {
    if (!workspaceId || !aiDescription.trim()) return
    setAiSuggesting(true)
    setAiSuggestion(null)

    try {
      const res = await fetch(`/api/v1/crew-ai-suggest?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ description: aiDescription }),
      })
      const data = await res.json()
      if (res.status === 422) {
        setHasAnthropicKey(false)
        toast.error("No Anthropic API key found. Add one in Settings → Credentials.")
        setAiSuggesting(false)
        return
      }
      if (!res.ok) {
        toast.error(data.detail || data.error || "AI suggestion failed")
        setAiSuggesting(false)
        return
      }
      setHasAnthropicKey(true)
      setAiSuggestion(data)
      // Pre-fill name/slug from suggestion
      setName(data.crew_name)
      setSlugManual(false)
      setMode("ai-preview")
    } catch {
      toast.error("Network error. Please try again.")
    } finally {
      setAiSuggesting(false)
    }
  }

  const handleDeployAI = async () => {
    if (!workspaceId || !aiSuggestion) return
    setSubmitting(true)

    try {
      // 1. Create the crew
      const crewRes = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name,
          slug,
          description: aiSuggestion.description,
          color,
        }),
      })
      if (!crewRes.ok) {
        const d = await crewRes.json()
        toast.error(typeof d.error === "string" ? d.error : "Failed to create crew")
        setSubmitting(false)
        return
      }
      const crew = await crewRes.json()

      // 2. Create each agent
      for (const a of aiSuggestion.agents) {
        const agentSlug = a.slug + "-" + slug
        const agentRes = await fetch(`/api/v1/agents?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            crew_id: crew.id,
            name: a.name,
            slug: agentSlug,
            role_title: a.role_title,
            agent_role: a.agent_role,
            system_prompt: a.system_prompt,
            cli_adapter: "CLAUDE_CODE",
            llm_provider: "ANTHROPIC",
            llm_model: "claude-sonnet-4-20250514",
            tool_profile: a.agent_role === "LEAD" ? "FULL" : "CODING",
            timeout_seconds: 1800,
            memory_enabled: true,
          }),
        })
        if (!agentRes.ok) {
          const d = await agentRes.json().catch(() => ({ error: "Failed to create agent" }))
          toast.error(typeof d.error === "string" ? d.error : `Failed to create agent "${a.name}"`)
          setSubmitting(false)
          return
        }
      }

      toast.success(`Crew "${name}" created with ${aiSuggestion.agents.length} agents`)
      router.push(`/crews/${crew.id}`)
    } catch {
      toast.error("Network error. Please try again.")
      setSubmitting(false)
    }
  }

  // ── Template deploy ──────────────────────────────────────────────────────────

  const handleDeployTemplate = async () => {
    if (!workspaceId || !selectedTemplate) return
    setSubmitting(true)

    try {
      const res = await fetch(
        `/api/v1/crew-templates/${selectedTemplate.slug}/deploy?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ crew_name: name, crew_slug: slug }),
        }
      )
      const data = await res.json()
      if (!res.ok) {
        if (res.status === 409) {
          toast.error(`A crew with slug "${slug}" already exists. Change the crew name and try again.`)
        } else {
          toast.error(data.detail || data.error || "Failed to deploy template")
        }
        setSubmitting(false)
        return
      }
      toast.success(`Crew "${name}" created with ${data.agent_count} agents`)
      router.push(`/crews/${data.crew_id}`)
    } catch {
      toast.error("Network error. Please try again.")
      setSubmitting(false)
    }
  }

  // ── Manual create ────────────────────────────────────────────────────────────

  const handleManualSubmit = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault()
      if (!workspaceId) return
      setSubmitting(true)

      const body: Record<string, unknown> = { name, slug }
      if (description) body.description = description
      if (color) body.color = color
      if (icon) body.icon = icon

      try {
        const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        })
        if (!res.ok) {
          const data = await res.json()
          toast.error(typeof data.error === "string" ? data.error : "Failed to create crew")
          setSubmitting(false)
          return
        }
        toast.success("Crew created successfully")
        router.push("/crews")
      } catch {
        toast.error("Network error. Please try again.")
        setSubmitting(false)
      }
    },
    [workspaceId, name, slug, description, color, icon, router]
  )

  // ── Loading ──────────────────────────────────────────────────────────────────

  if (wsLoading) {
    return (
      <div className="flex items-center justify-center p-12">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  // ── Step 1: Choose mode ──────────────────────────────────────────────────────

  if (mode === "choose") {
    return (
      <div className="p-4 sm:p-6 space-y-6 max-w-3xl">
        <PageHeader title="New Crew" description="Create a new crew to organize your agents">
          <Button variant="outline" size="sm" asChild>
            <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back</Link>
          </Button>
        </PageHeader>

        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          {/* Create with AI */}
          <button
            onClick={handleCreateWithAI}
            disabled={findingCoordinator}
            className="flex flex-col items-start gap-3 rounded-lg border border-primary/40 bg-primary/5 p-5 text-left transition-all hover:bg-primary/10 hover:border-primary/70 disabled:opacity-60 disabled:cursor-not-allowed"
          >
            <div className="flex items-center gap-2">
              {findingCoordinator ? (
                <Loader2 className="h-5 w-5 text-primary animate-spin" />
              ) : (
                <Sparkles className="h-5 w-5 text-primary" />
              )}
              <span className="font-semibold">Create with AI</span>
            </div>
            <p className="text-sm text-muted-foreground">
              Chat with your Coordinator agent — describe what the crew should do and it will create it for you.
            </p>
            <Badge className="text-xs">Recommended</Badge>
          </button>

          {/* Start from Template */}
          <button
            onClick={() => setMode("template")}
            className="flex flex-col items-start gap-3 rounded-lg border border-border p-5 text-left transition-all hover:bg-accent hover:border-primary/50"
          >
            <div className="flex items-center gap-2">
              <Bot className="h-5 w-5 text-muted-foreground" />
              <span className="font-semibold">Start from Template</span>
            </div>
            <p className="text-sm text-muted-foreground">
              Choose a pre-built crew blueprint with agents and system prompts ready to deploy.
            </p>
          </button>

          {/* Create from Scratch */}
          <button
            onClick={() => setMode("manual")}
            className="flex flex-col items-start gap-3 rounded-lg border border-border p-5 text-left transition-all hover:bg-accent hover:border-primary/50"
          >
            <div className="flex items-center gap-2">
              <Users className="h-5 w-5 text-muted-foreground" />
              <span className="font-semibold">From Scratch</span>
            </div>
            <p className="text-sm text-muted-foreground">
              Set up an empty crew and configure every agent manually.
            </p>
          </button>
        </div>

        {/* Quick start template grid */}
        {loadingTemplates ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" /> Loading templates...
          </div>
        ) : templates.length > 0 && (
          <div>
            <h3 className="text-sm font-medium text-muted-foreground mb-3">Quick Start Templates</h3>
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {templates.map((t) => (
                <button
                  key={t.id}
                  onClick={() => handleSelectTemplate(t)}
                  className="flex items-start gap-3 rounded-lg border border-border p-3 text-left transition-all hover:bg-accent hover:border-primary/50 group"
                >
                  <span className="text-2xl">{t.icon || "📦"}</span>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-1">
                      <span className="font-medium text-sm truncate">{t.name}</span>
                      <ChevronRight className="h-3 w-3 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
                    </div>
                    <p className="text-xs text-muted-foreground line-clamp-2 mt-0.5">{t.description}</p>
                    <div className="flex items-center gap-2 mt-1.5">
                      <div className="flex items-center gap-1">
                        <Bot className="h-3 w-3 text-muted-foreground" />
                        <span className="text-xs text-muted-foreground">{t.agents.length} agents</span>
                      </div>
                      <Badge variant="outline" className="text-xs py-0">{t.category}</Badge>
                    </div>
                  </div>
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
    )
  }

  // ── No coordinator found ──────────────────────────────────────────────────────

  if (mode === "no-coordinator") {
    return (
      <div className="p-4 sm:p-6 space-y-6 max-w-3xl">
        <PageHeader title="Coordinator Required" description="Create with AI needs a Coordinator agent">
          <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        </PageHeader>
        <div className="rounded-lg border border-yellow-500/30 bg-yellow-500/10 p-5 space-y-3">
          <div className="flex items-start gap-3">
            <AlertTriangle className="h-5 w-5 text-yellow-500 mt-0.5 shrink-0" />
            <div className="space-y-2">
              <p className="font-medium text-sm">No Coordinator agent found in this workspace.</p>
              <p className="text-sm text-muted-foreground">
                The &ldquo;Create with AI&rdquo; feature works by chatting with a COORDINATOR agent.
                Create one first, then come back to use this feature.
              </p>
              <p className="text-sm text-muted-foreground">
                The Coordinator agent needs <code className="text-xs bg-muted px-1 rounded">agent_role = COORDINATOR</code>.
                You can create one manually or deploy a crew template that includes a Coordinator.
              </p>
            </div>
          </div>
          <div className="flex gap-2 pt-1">
            <Button size="sm" onClick={() => setMode("template")}>Browse Templates</Button>
            <Button size="sm" variant="outline" onClick={() => setMode("manual")}>Create Manually</Button>
          </div>
        </div>
      </div>
    )
  }

  // ── Step 2: AI description input ─────────────────────────────────────────────

  if (mode === "ai") {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
        <PageHeader title="Create with AI" description="Describe your crew and AI will design it for you">
          <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        </PageHeader>

        {hasAnthropicKey === false && (
          <div className="flex items-start gap-3 rounded-lg border border-yellow-500/30 bg-yellow-500/10 p-4">
            <AlertTriangle className="h-4 w-4 text-yellow-500 mt-0.5 shrink-0" />
            <div className="text-sm">
              <span className="font-medium text-yellow-600 dark:text-yellow-400">Anthropic API key required.</span>
              {" "}
              <Link href="/settings/credentials" className="underline hover:no-underline">
                Add one in Settings → Credentials
              </Link>
              {" "}(type: <code className="text-xs bg-yellow-500/20 px-1 rounded">API_KEY</code>, provider: <code className="text-xs bg-yellow-500/20 px-1 rounded">ANTHROPIC</code>). Claude Code OAuth tokens cannot be used here.
            </div>
          </div>
        )}

        <Card>
          <CardHeader>
            <CardTitle className="text-base">What should this crew do?</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <Textarea
              value={aiDescription}
              onChange={(e) => setAiDescription(e.target.value)}
              placeholder="e.g. I need a team to automate our accounting — process invoices, reconcile transactions, prepare monthly financial reports, and handle tax filing prep."
              rows={5}
              className="resize-none"
            />
            <p className="text-xs text-muted-foreground">
              Be specific about the tasks, tools, and domain. The more detail, the better the agents.
            </p>
            <Button
              onClick={handleAISuggest}
              disabled={aiSuggesting || aiDescription.trim().length < 10}
              className="gap-2"
            >
              {aiSuggesting ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Designing your crew...
                </>
              ) : (
                <>
                  <Sparkles className="h-4 w-4" />
                  Suggest Crew
                </>
              )}
            </Button>
          </CardContent>
        </Card>
      </div>
    )
  }

  // ── Step 3: AI preview + deploy ──────────────────────────────────────────────

  if (mode === "ai-preview" && aiSuggestion) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
        <PageHeader
          title="AI-Designed Crew"
          description={aiSuggestion.description || "Review and deploy your AI-designed crew"}
        >
          <Button variant="outline" size="sm" onClick={() => setMode("ai")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        </PageHeader>

        <Card>
          <CardHeader><CardTitle className="text-base">Crew Name</CardTitle></CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="ai-name">Name *</Label>
                <Input
                  id="ai-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="ai-slug">Slug</Label>
                <Input
                  id="ai-slug"
                  value={slug}
                  onChange={(e) => { setSlugManual(true); setSlug(slugify(e.target.value)) }}
                  className="font-mono text-sm"
                  required
                />
              </div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base flex items-center gap-2">
              <Bot className="h-4 w-4" />
              Agents ({aiSuggestion.agents.length})
              <span className="text-xs font-normal text-muted-foreground ml-1">— click to preview system prompt</span>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-2">
              {aiSuggestion.agents.map((a) => (
                <AgentRow key={a.slug} agent={a} />
              ))}
            </div>
          </CardContent>
        </Card>

        <div className="flex items-center gap-3 pt-2">
          <Button onClick={handleDeployAI} disabled={submitting || !name.trim()} className="gap-2">
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Sparkles className="h-4 w-4" />}
            Deploy Crew ({aiSuggestion.agents.length} agents)
          </Button>
          <Button
            variant="outline"
            onClick={() => { setAiSuggestion(null); setMode("ai") }}
            className="gap-2"
          >
            <RefreshCw className="h-4 w-4" />
            Regenerate
          </Button>
          <Button variant="ghost" onClick={() => setMode("choose")}>Cancel</Button>
        </div>
      </div>
    )
  }

  // ── Step 2a: Template preview + deploy ───────────────────────────────────────

  if (mode === "template" && selectedTemplate) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
        <PageHeader
          title={`Deploy: ${selectedTemplate.name}`}
          description={selectedTemplate.description || "Deploy this crew template"}
        >
          <Button variant="outline" size="sm" onClick={() => { setMode("choose"); setSelectedTemplate(null) }}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        </PageHeader>

        <Card>
          <CardHeader><CardTitle className="text-base">Crew Name</CardTitle></CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="tmpl-name">Name *</Label>
                <Input
                  id="tmpl-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={selectedTemplate.name}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="tmpl-slug">Slug</Label>
                <Input
                  id="tmpl-slug"
                  value={slug}
                  onChange={(e) => { setSlugManual(true); setSlug(slugify(e.target.value)) }}
                  className="font-mono text-sm"
                  required
                />
              </div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base flex items-center gap-2">
              <Bot className="h-4 w-4" />
              Agents ({selectedTemplate.agents.length})
              <span className="text-xs font-normal text-muted-foreground ml-1">— click to preview system prompt</span>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-2">
              {selectedTemplate.agents.map((a) => (
                <AgentRow key={a.slug} agent={a} />
              ))}
            </div>
          </CardContent>
        </Card>

        <div className="flex items-center gap-3 pt-2">
          <Button onClick={handleDeployTemplate} disabled={submitting || !name.trim()} className="gap-2">
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Sparkles className="h-4 w-4" />}
            Deploy Crew ({selectedTemplate.agents.length} agents)
          </Button>
          <Button variant="outline" onClick={() => { setMode("choose"); setSelectedTemplate(null) }}>
            Cancel
          </Button>
        </div>
      </div>
    )
  }

  // ── Step 2b: Template gallery ────────────────────────────────────────────────

  if (mode === "template" && !selectedTemplate) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
        <PageHeader title="Choose a Template" description="Pick a crew blueprint to get started quickly">
          <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        </PageHeader>

        {loadingTemplates ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground py-8">
            <Loader2 className="h-4 w-4 animate-spin" /> Loading templates...
          </div>
        ) : (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            {templates.map((t) => (
              <button
                key={t.id}
                onClick={() => handleSelectTemplate(t)}
                className="flex flex-col items-start gap-2 rounded-lg border border-border p-4 text-left transition-all hover:bg-accent hover:border-primary/50"
              >
                <div className="flex items-center gap-2">
                  <span className="text-2xl">{t.icon || "📦"}</span>
                  <span className="font-semibold">{t.name}</span>
                </div>
                <p className="text-sm text-muted-foreground">{t.description}</p>
                <div className="flex items-center gap-2 mt-1">
                  <Bot className="h-3.5 w-3.5 text-muted-foreground" />
                  <span className="text-xs text-muted-foreground">{t.agents.length} agents</span>
                  <Badge variant="outline" className="text-xs">{t.category}</Badge>
                </div>
                <div className="flex flex-wrap gap-1 mt-1">
                  {t.agents.map((a) => (
                    <Badge key={a.slug} variant={a.agent_role === "LEAD" ? "default" : "secondary"} className="text-xs">
                      {a.name}
                    </Badge>
                  ))}
                </div>
              </button>
            ))}
          </div>
        )}
      </div>
    )
  }

  // ── Step 2c: Manual form ─────────────────────────────────────────────────────

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
      <PageHeader title="New Crew" description="Create a new crew from scratch">
        <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
          <ArrowLeft className="mr-2 h-4 w-4" />Back
        </Button>
      </PageHeader>

      <form onSubmit={handleManualSubmit} className="space-y-4 sm:space-y-6">
        <Card>
          <CardHeader><CardTitle className="text-base">Crew Details</CardTitle></CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="name">Name *</Label>
                <Input id="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Marketing" required />
              </div>
              <div className="space-y-2">
                <Label htmlFor="slug">Slug *</Label>
                <Input id="slug" value={slug} onChange={(e) => { setSlugManual(true); setSlug(e.target.value) }} placeholder="marketing" className="font-mono text-sm" required />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="description">Description</Label>
              <Textarea id="description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What is this crew responsible for?" rows={3} />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader><CardTitle className="text-base">Appearance</CardTitle></CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="color">Color</Label>
                <div className="flex items-center gap-3">
                  <Input id="color" type="color" value={color} onChange={(e) => setColor(e.target.value)} className="h-9 w-14 cursor-pointer p-1" />
                  <Input value={color} onChange={(e) => setColor(e.target.value)} placeholder="#3B82F6" className="font-mono text-sm" />
                </div>
              </div>
              <div className="space-y-2">
                <Label htmlFor="icon">Icon (emoji)</Label>
                <Input id="icon" value={icon} onChange={(e) => setIcon(e.target.value)} placeholder="e.g. 🚀" maxLength={10} />
              </div>
            </div>
          </CardContent>
        </Card>

        <div className="flex items-center gap-3 pt-2">
          <Button type="submit" disabled={submitting || !workspaceId} className="gap-2">
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Users className="h-4 w-4" />}
            Create Crew
          </Button>
          <Button type="button" variant="outline" onClick={() => setMode("choose")}>Cancel</Button>
        </div>
      </form>
    </div>
  )
}
