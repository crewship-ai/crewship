"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import {
  ArrowLeft, Loader2, Users, Sparkles, Bot, RefreshCw, AlertTriangle,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { Badge } from "@/components/ui/badge"
import { PageShell } from "@/components/layout/page-shell"
import { SectionCard } from "@/components/ui/section-card"
import { useWorkspace } from "@/hooks/use-workspace"
import { slugify } from "@/lib/utils/slugify"
import { CREW_COLORS, CREW_BG_CLASSES, STATUS_BG_LIGHT } from "@/lib/colors"
import { cn } from "@/lib/utils"

type CrewPaletteId = keyof typeof CREW_COLORS
import { toast } from "sonner"
import { CrewNameSlugFields } from "@/components/features/crews/crew-name-slug-fields"
import { CrewAgentPreviewList } from "@/components/features/crews/crew-agent-preview-list"
import { QuickStartTemplateGrid, TemplateGallery } from "@/components/features/crews/crew-template-picker"
import { RuntimeConfig, type RuntimeConfigValue } from "@/components/features/crews/runtime-config"

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

interface AISuggestion {
  crew_name: string
  crew_slug: string
  description: string
  agents: Array<{
    name: string
    slug: string
    role_title: string
    agent_role: string
    system_prompt: string
  }>
}

type Mode = "choose" | "ai" | "ai-preview" | "template" | "manual" | "no-coordinator"

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
  // Crew color is always a palette id — never a raw hex value. Downstream
  // renderers (CrewIcon, crew-group-node, crews overviews) all resolve the
  // id to a Tailwind class, so any freeform hex would break them.
  const [color, setColor] = useState<CrewPaletteId>("blue")
  const [icon, setIcon] = useState("")
  const [runtimeConfig, setRuntimeConfig] = useState<RuntimeConfigValue>({
    runtimeImage: "",
    devcontainerConfig: "",
    miseConfig: "",
  })
  const [runtimeDirty, setRuntimeDirty] = useState(false)

  useEffect(() => {
    if (!slugManual) setSlug(slugify(name))
  }, [name, slugManual])

  // Fetch templates once workspaceId is available
  useEffect(() => {
    if (!workspaceId) {
      setTemplates([])
      if (!wsLoading) setLoadingTemplates(false)
      return
    }
    setLoadingTemplates(true)
    fetch(`/api/v1/crew-templates?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data) => setTemplates(Array.isArray(data) ? data : []))
      .catch(() => setTemplates([]))
      .finally(() => setLoadingTemplates(false))
  }, [workspaceId, wsLoading])

  /**
   * @deprecated COORDINATOR role is no longer actively developed (2026-04-16).
   * See docs/guides/coordinator.mdx. The "Create with AI" flow will be
   * reworked to use MCP-connected external LLMs or a scheduler-driven AGENT.
   * Function retained for backward compatibility with existing COORDINATOR agents.
   */
  const handleCreateWithAI = async () => {
    if (!workspaceId) return
    setFindingCoordinator(true)
    try {
      const res = await fetch(`/api/v1/agents?workspace_id=${workspaceId}&role=COORDINATOR`)
      if (!res.ok) {
        toast.error("Failed to look up a Coordinator agent. Please try again.")
        return
      }
      const data = await res.json()
      const agents: Array<{ id: string; agent_role: string }> = Array.isArray(data) ? data : []
      const coordinator = agents.find((a) => a.agent_role === "COORDINATOR")
      if (coordinator) {
        const prefill = encodeURIComponent(
          "I need you to create a new crew for me. Please describe what kind of crew you want and I will design the agents, roles, and system prompts, then create everything for you.\n\nWhat should the crew do?"
        )
        router.push(`/crews/agents/${coordinator.id}/chat?prefill=${prefill}&workspace_id=${workspaceId}`)
        return
      }
      setMode("no-coordinator")
    } catch {
      toast.error("Failed to look up a Coordinator agent. Please try again.")
    } finally {
      setFindingCoordinator(false)
    }
  }

  const handleSelectTemplate = (t: CrewTemplate) => {
    setSelectedTemplate(t)
    setName(t.name)
    setSlugManual(false)
    setDescription(t.description || "")
    // Only accept known palette ids; anything else falls back to "blue"
    // so a legacy hex color stored on a template can't leak into state.
    const templateColor = (t.color && t.color in CREW_COLORS ? t.color : "blue") as CrewPaletteId
    setColor(templateColor)
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
      const crewRes = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, slug, description: aiSuggestion.description, color }),
      })
      if (!crewRes.ok) {
        const d = await crewRes.json()
        toast.error(typeof d.error === "string" ? d.error : "Failed to create crew")
        setSubmitting(false)
        return
      }
      const crew = await crewRes.json()

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
          await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, { method: "DELETE" }).catch(() => {})
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
      if (runtimeDirty && runtimeConfig.devcontainerConfig) body.devcontainer_config = runtimeConfig.devcontainerConfig
      if (runtimeDirty && runtimeConfig.miseConfig) body.mise_config = runtimeConfig.miseConfig
      if (runtimeDirty && runtimeConfig.runtimeImage && runtimeConfig.runtimeImage !== "debian:bookworm-slim") body.runtime_image = runtimeConfig.runtimeImage

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
    [workspaceId, name, slug, description, color, icon, runtimeConfig, runtimeDirty, router]
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
      <PageShell
        title="New Crew"
        description="Create a new crew to organize your agents"
        actions={
          <Button variant="outline" size="sm" asChild>
            <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back</Link>
          </Button>
        }
        className="max-w-3xl"
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
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
              <span className="text-body font-semibold">Create with AI</span>
            </div>
            <p className="text-label text-muted-foreground">
              Chat with your Coordinator agent — describe what the crew should do and it will create it for you.
            </p>
            <Badge className="text-micro">Recommended</Badge>
          </button>

          {/* Start from Template */}
          <button
            onClick={() => setMode("template")}
            className="flex flex-col items-start gap-3 rounded-lg border border-border p-5 text-left transition-all hover:bg-accent hover:border-primary/50"
          >
            <div className="flex items-center gap-2">
              <Bot className="h-5 w-5 text-muted-foreground" />
              <span className="text-body font-semibold">Start from Template</span>
            </div>
            <p className="text-label text-muted-foreground">
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
              <span className="text-body font-semibold">From Scratch</span>
            </div>
            <p className="text-label text-muted-foreground">
              Set up an empty crew and configure every agent manually.
            </p>
          </button>
        </div>

        <QuickStartTemplateGrid
          templates={templates}
          loading={loadingTemplates}
          onSelect={handleSelectTemplate}
        />
      </PageShell>
    )
  }

  // ── No coordinator found ──────────────────────────────────────────────────────

  if (mode === "no-coordinator") {
    return (
      <PageShell
        title="Coordinator Required"
        description="Create with AI needs a Coordinator agent"
        actions={
          <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        }
        className="max-w-3xl"
      >
        <div className={cn("rounded-lg border border-border p-5 space-y-3", STATUS_BG_LIGHT.BLOCKED)}>
          <div className="flex items-start gap-3">
            <AlertTriangle className="h-5 w-5 mt-0.5 shrink-0" />
            <div className="space-y-2">
              <p className="text-body font-medium">No Coordinator agent found in this workspace.</p>
              <p className="text-body text-muted-foreground">
                The &ldquo;Create with AI&rdquo; feature works by chatting with a COORDINATOR agent.
                Create one first, then come back to use this feature.
              </p>
              <p className="text-body text-muted-foreground">
                The Coordinator agent needs <code className="text-micro bg-muted px-1 rounded">agent_role = COORDINATOR</code>.
                You can create one manually or deploy a crew template that includes a Coordinator.
              </p>
            </div>
          </div>
          <div className="flex gap-2 pt-1">
            <Button size="sm" onClick={() => setMode("template")}>Browse Templates</Button>
            <Button size="sm" variant="outline" onClick={() => setMode("manual")}>Create Manually</Button>
          </div>
        </div>
      </PageShell>
    )
  }

  // ── Step 2: AI description input ─────────────────────────────────────────────

  if (mode === "ai") {
    return (
      <PageShell
        title="Create with AI"
        description="Describe your crew and AI will design it for you"
        actions={
          <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        }
        className="max-w-3xl"
      >
        {hasAnthropicKey === false && (
          <div className={cn("flex items-start gap-3 rounded-lg border border-border p-4", STATUS_BG_LIGHT.BLOCKED)}>
            <AlertTriangle className="h-4 w-4 mt-0.5 shrink-0" />
            <div className="text-body">
              <span className="font-medium">Anthropic API key required.</span>
              {" "}
              <Link href="/settings/credentials" className="underline hover:no-underline">
                Add one in Settings → Credentials
              </Link>
              {" "}(type: <code className="text-micro bg-muted px-1 rounded">API_KEY</code>, provider: <code className="text-micro bg-muted px-1 rounded">ANTHROPIC</code>). Claude Code OAuth tokens cannot be used here.
            </div>
          </div>
        )}

        <SectionCard title="What should this crew do?">
          <div className="space-y-4">
            <Textarea
              value={aiDescription}
              onChange={(e) => setAiDescription(e.target.value)}
              placeholder="e.g. I need a team to automate our accounting — process invoices, reconcile transactions, prepare monthly financial reports, and handle tax filing prep."
              rows={5}
              className="resize-none"
            />
            <p className="text-label text-muted-foreground">
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
          </div>
        </SectionCard>
      </PageShell>
    )
  }

  // ── Step 3: AI preview + deploy ──────────────────────────────────────────────

  if (mode === "ai-preview" && aiSuggestion) {
    return (
      <PageShell
        title="AI-Designed Crew"
        description={aiSuggestion.description || "Review and deploy your AI-designed crew"}
        actions={
          <Button variant="outline" size="sm" onClick={() => setMode("ai")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        }
        className="max-w-3xl"
      >
        <CrewNameSlugFields
          name={name}
          slug={slug}
          onNameChange={setName}
          onSlugChange={setSlug}
          onSlugManualEdit={() => setSlugManual(true)}
          nameId="ai-name"
          slugId="ai-slug"
        />

        <CrewAgentPreviewList agents={aiSuggestion.agents} />

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
      </PageShell>
    )
  }

  // ── Step 2a: Template preview + deploy ───────────────────────────────────────

  if (mode === "template" && selectedTemplate) {
    return (
      <PageShell
        title={`Deploy: ${selectedTemplate.name}`}
        description={selectedTemplate.description || "Deploy this crew template"}
        actions={
          <Button variant="outline" size="sm" onClick={() => { setMode("choose"); setSelectedTemplate(null) }}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        }
        className="max-w-3xl"
      >
        <CrewNameSlugFields
          name={name}
          slug={slug}
          onNameChange={setName}
          onSlugChange={setSlug}
          onSlugManualEdit={() => setSlugManual(true)}
          namePlaceholder={selectedTemplate.name}
          nameId="tmpl-name"
          slugId="tmpl-slug"
        />

        <CrewAgentPreviewList agents={selectedTemplate.agents} />

        <div className="flex items-center gap-3 pt-2">
          <Button onClick={handleDeployTemplate} disabled={submitting || !name.trim()} className="gap-2">
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Sparkles className="h-4 w-4" />}
            Deploy Crew ({selectedTemplate.agents.length} agents)
          </Button>
          <Button variant="outline" onClick={() => { setMode("choose"); setSelectedTemplate(null) }}>
            Cancel
          </Button>
        </div>
      </PageShell>
    )
  }

  // ── Step 2b: Template gallery ────────────────────────────────────────────────

  if (mode === "template" && !selectedTemplate) {
    return (
      <PageShell
        title="Choose a Template"
        description="Pick a crew blueprint to get started quickly"
        actions={
          <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
            <ArrowLeft className="mr-2 h-4 w-4" />Back
          </Button>
        }
        className="max-w-3xl"
      >
        <TemplateGallery
          templates={templates}
          loading={loadingTemplates}
          onSelect={handleSelectTemplate}
        />
      </PageShell>
    )
  }

  // ── Step 2c: Manual form ─────────────────────────────────────────────────────

  return (
    <PageShell
      title="New Crew"
      description="Create a new crew from scratch"
      actions={
        <Button variant="outline" size="sm" onClick={() => setMode("choose")}>
          <ArrowLeft className="mr-2 h-4 w-4" />Back
        </Button>
      }
      className="max-w-3xl"
    >
      <form onSubmit={handleManualSubmit} className="space-y-6">
        <SectionCard title="Crew Details">
          <div className="space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="name">Name *</Label>
                <Input id="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Marketing" required />
              </div>
              <div className="space-y-2">
                <Label htmlFor="slug">Slug *</Label>
                <Input id="slug" value={slug} onChange={(e) => { setSlugManual(true); setSlug(slugify(e.target.value)) }} placeholder="marketing" className="font-mono text-body" required />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="description">Description</Label>
              <Textarea id="description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What is this crew responsible for?" rows={3} />
            </div>
          </div>
        </SectionCard>

        <SectionCard title="Appearance">
          <div className="space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label>Color</Label>
                <div className="flex items-center gap-2" role="radiogroup" aria-label="Crew color">
                  {(Object.keys(CREW_COLORS) as CrewPaletteId[]).map((id) => {
                    const selected = color === id
                    return (
                      <button
                        key={id}
                        type="button"
                        role="radio"
                        aria-checked={selected}
                        aria-label={id}
                        onClick={() => setColor(id)}
                        className={cn(
                          "h-7 w-7 rounded-md transition-all",
                          CREW_BG_CLASSES[id],
                          selected
                            ? "ring-2 ring-offset-2 ring-offset-background ring-foreground/60 scale-110"
                            : "hover:scale-105 opacity-80 hover:opacity-100",
                        )}
                      />
                    )
                  })}
                </div>
              </div>
              <div className="space-y-2">
                <Label htmlFor="icon">Icon (Lucide name)</Label>
                <Input
                  id="icon"
                  value={icon}
                  onChange={(e) => setIcon(e.target.value)}
                  placeholder="e.g. rocket, code, clipboard"
                  maxLength={40}
                />
              </div>
            </div>
          </div>
        </SectionCard>

        <SectionCard
          title="Runtime Configuration"
          description="Configure devcontainer features, language runtimes, and base image for the crew container."
        >
          <RuntimeConfig value={runtimeConfig} onChange={(val) => { setRuntimeConfig(val); setRuntimeDirty(true) }} />
        </SectionCard>

        <div className="flex items-center gap-3 pt-2">
          <Button type="submit" disabled={submitting || !workspaceId} className="gap-2">
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Users className="h-4 w-4" />}
            Create Crew
          </Button>
          <Button type="button" variant="outline" onClick={() => setMode("choose")}>Cancel</Button>
        </div>
      </form>
    </PageShell>
  )
}
