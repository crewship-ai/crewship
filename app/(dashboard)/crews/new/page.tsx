"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import { ArrowLeft, Loader2, Users, Sparkles, Bot, ChevronRight } from "lucide-react"
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

interface CrewTemplateAgent {
  name: string
  slug: string
  role_title: string
  agent_role: string
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

type Mode = "choose" | "template" | "manual"

export default function NewCrewPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [mode, setMode] = useState<Mode>("choose")
  const [submitting, setSubmitting] = useState(false)

  // Template state
  const [templates, setTemplates] = useState<CrewTemplate[]>([])
  const [loadingTemplates, setLoadingTemplates] = useState(false)
  const [selectedTemplate, setSelectedTemplate] = useState<CrewTemplate | null>(null)

  // Form state
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [slugManual, setSlugManual] = useState(false)
  const [description, setDescription] = useState("")
  const [color, setColor] = useState("#3B82F6")
  const [icon, setIcon] = useState("")

  useEffect(() => {
    if (!slugManual) setSlug(slugify(name))
  }, [name, slugManual])

  const fetchTemplates = useCallback(async () => {
    setLoadingTemplates(true)
    try {
      const res = await fetch("/api/v1/crew-templates")
      if (res.ok) {
        const data = await res.json()
        setTemplates(data)
      }
    } catch { /* ignore */ } finally {
      setLoadingTemplates(false)
    }
  }, [])

  useEffect(() => {
    if (mode === "choose" || mode === "template") fetchTemplates()
  }, [mode, fetchTemplates])

  const handleSelectTemplate = (t: CrewTemplate) => {
    setSelectedTemplate(t)
    setName(t.name)
    setSlugManual(false)
    setDescription(t.description || "")
    setColor(t.color || "#3B82F6")
    setIcon(t.icon || "")
    setMode("template")
  }

  const handleDeployTemplate = async () => {
    if (!workspaceId || !selectedTemplate) return
    setSubmitting(true)

    try {
      const res = await fetch(`/api/v1/crew-templates/${selectedTemplate.slug}/deploy?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ crew_name: name, crew_slug: slug }),
      })
      if (!res.ok) {
        const data = await res.json()
        toast.error(data.detail || data.error || "Failed to deploy template")
        setSubmitting(false)
        return
      }
      const data = await res.json()
      toast.success(`Crew "${name}" created with ${data.agent_count} agents`)
      router.push(`/crews/${data.crew_id}`)
    } catch {
      toast.error("Network error")
      setSubmitting(false)
    }
  }

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
        toast.error("Network error")
        setSubmitting(false)
      }
    },
    [workspaceId, name, slug, description, color, icon, router],
  )

  if (wsLoading) {
    return (
      <div className="flex items-center justify-center p-12">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  // Step 1: Choose mode
  if (mode === "choose") {
    return (
      <div className="p-4 sm:p-6 space-y-6 max-w-3xl">
        <PageHeader title="New Crew" description="Create a new crew to organize your agents">
          <Button variant="outline" size="sm" asChild>
            <Link href="/crews"><ArrowLeft className="mr-2 h-4 w-4" />Back</Link>
          </Button>
        </PageHeader>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <button
            onClick={() => setMode("template")}
            className="flex flex-col items-start gap-3 rounded-lg border border-border p-5 text-left transition-all hover:bg-accent hover:border-primary/50"
          >
            <div className="flex items-center gap-2">
              <Sparkles className="h-5 w-5 text-primary" />
              <span className="font-semibold">Start from Template</span>
            </div>
            <p className="text-sm text-muted-foreground">
              Choose a pre-built crew blueprint with agents, roles, and system prompts ready to go.
            </p>
            <Badge variant="secondary" className="text-xs">Recommended</Badge>
          </button>

          <button
            onClick={() => setMode("manual")}
            className="flex flex-col items-start gap-3 rounded-lg border border-border p-5 text-left transition-all hover:bg-accent hover:border-primary/50"
          >
            <div className="flex items-center gap-2">
              <Users className="h-5 w-5 text-muted-foreground" />
              <span className="font-semibold">Create from Scratch</span>
            </div>
            <p className="text-sm text-muted-foreground">
              Set up an empty crew and add agents manually. Full control over every setting.
            </p>
          </button>
        </div>

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
                    <div className="flex items-center gap-1 mt-1.5">
                      <Bot className="h-3 w-3 text-muted-foreground" />
                      <span className="text-xs text-muted-foreground">{t.agents.length} agents</span>
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

  // Step 2a: Template preview + deploy
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
          <CardHeader>
            <CardTitle className="text-base">Crew Name</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="name">Name *</Label>
                <Input
                  id="name" value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={selectedTemplate.name}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="slug">Slug</Label>
                <Input
                  id="slug" value={slug}
                  onChange={(e) => { setSlugManual(true); setSlug(e.target.value) }}
                  className="font-mono text-sm" required
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
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-3">
              {selectedTemplate.agents.map((a) => (
                <div key={a.slug} className="flex items-center gap-3 rounded-lg border border-border p-3">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-sm">{a.name}</span>
                      <Badge variant={a.agent_role === "LEAD" ? "default" : "secondary"} className="text-xs">
                        {a.agent_role === "LEAD" ? "Lead" : a.role_title}
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground mt-0.5">{a.role_title}</p>
                  </div>
                </div>
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

  // Step 2b: Template gallery (no template selected yet)
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

  // Step 2c: Manual form (existing behavior)
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
