"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import { ArrowLeft, Loader2, Users } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { PageHeader } from "@/components/layout/page-header"
import { useWorkspace } from "@/hooks/use-workspace"
import { slugify } from "@/lib/utils/slugify"
import { toast } from "sonner"

export default function NewCrewPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [submitting, setSubmitting] = useState(false)

  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [slugManual, setSlugManual] = useState(false)
  const [description, setDescription] = useState("")
  const [color, setColor] = useState("#3B82F6")
  const [icon, setIcon] = useState("")

  useEffect(() => {
    if (!slugManual) {
      setSlug(slugify(name))
    }
  }, [name, slugManual])

  const handleSubmit = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault()
      if (!workspaceId) return

      setSubmitting(true)

      const body: Record<string, unknown> = {
        name,
        slug,
      }

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
          const message =
            typeof data.error === "string"
              ? data.error
              : "Failed to create crew. Please check your input."
          toast.error(message)
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

  if (wsLoading) {
    return (
      <div className="flex items-center justify-center p-12">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
      <PageHeader title="New Crew" description="Create a new crew to organize your agents">
        <Button variant="outline" size="sm" asChild>
          <Link href="/crews">
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back
          </Link>
        </Button>
      </PageHeader>

      <form onSubmit={handleSubmit} className="space-y-4 sm:space-y-6">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Crew Details</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="name">Name *</Label>
                <Input
                  id="name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="e.g. Marketing"
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
                  placeholder="marketing"
                  className="font-mono text-sm"
                  required
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="description">Description</Label>
              <Textarea
                id="description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="What is this crew responsible for?"
                rows={3}
              />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">Appearance</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="color">Color</Label>
                <div className="flex items-center gap-3">
                  <Input
                    id="color"
                    type="color"
                    value={color}
                    onChange={(e) => setColor(e.target.value)}
                    className="h-9 w-14 cursor-pointer p-1"
                  />
                  <Input
                    value={color}
                    onChange={(e) => setColor(e.target.value)}
                    placeholder="#3B82F6"
                    className="font-mono text-sm"
                  />
                </div>
              </div>
              <div className="space-y-2">
                <Label htmlFor="icon">Icon (emoji)</Label>
                <Input
                  id="icon"
                  value={icon}
                  onChange={(e) => setIcon(e.target.value)}
                  placeholder="e.g. 🚀"
                  maxLength={10}
                />
              </div>
            </div>
          </CardContent>
        </Card>

        <div className="flex items-center gap-3 pt-2">
          <Button type="submit" disabled={submitting || !workspaceId} className="gap-2">
            {submitting ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Users className="h-4 w-4" />
            )}
            Create Crew
          </Button>
          <Button type="button" variant="outline" asChild>
            <Link href="/crews">Cancel</Link>
          </Button>
        </div>
      </form>
    </div>
  )
}
