"use client"

import { useState } from "react"
import { z } from "zod"
import { Upload } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"

const ImportResultSchema = z.object({
  skill_id: z.string(),
  name: z.string(),
  slug: z.string(),
  created: z.boolean(),
})
type ImportResult = z.infer<typeof ImportResultSchema>

interface ImportSkillDialogProps {
  workspaceId: string
  onImported?: (result: ImportResult) => void
}

export function ImportSkillDialog({ workspaceId, onImported }: ImportSkillDialogProps) {
  const [open, setOpen] = useState(false)
  const [tab, setTab] = useState<"url" | "content">("url")
  const [url, setUrl] = useState("")
  const [content, setContent] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleImport() {
    setError(null)
    setLoading(true)

    try {
      const body =
        tab === "url"
          ? { url: url.trim() }
          : { content: content.trim() }

      const res = await fetch(
        `/api/v1/workspaces/${workspaceId}/skills/import`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }
      )

      if (!res.ok) {
        const data = await res.json()
        setError(data.detail ?? data.error ?? "Import failed")
        return
      }

      const result = ImportResultSchema.parse(await res.json())
      setOpen(false)
      setUrl("")
      setContent("")
      onImported?.(result)
    } catch {
      setError("Network error — please try again")
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Upload className="mr-2 h-4 w-4" />
          Import Skill
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Import Skill</DialogTitle>
          <DialogDescription>
            Import a skill from a GitHub URL or paste a SKILL.md file directly.
          </DialogDescription>
        </DialogHeader>

        <Tabs value={tab} onValueChange={(v) => setTab(v as "url" | "content")}>
          <TabsList className="w-full">
            <TabsTrigger value="url" className="flex-1">
              From URL
            </TabsTrigger>
            <TabsTrigger value="content" className="flex-1">
              Paste Content
            </TabsTrigger>
          </TabsList>

          <TabsContent value="url" className="mt-4 space-y-2">
            <Label htmlFor="skill-url">SKILL.md URL</Label>
            <Input
              id="skill-url"
              placeholder="https://github.com/org/skills/blob/main/my-skill/SKILL.md"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              disabled={loading}
            />
            <p className="text-xs text-muted-foreground">
              Supports GitHub URLs, raw URLs, or shorthand{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                owner/repo/path.md
              </code>
            </p>
          </TabsContent>

          <TabsContent value="content" className="mt-4 space-y-2">
            <Label htmlFor="skill-content">SKILL.md Content</Label>
            <Textarea
              id="skill-content"
              placeholder={`---\nname: my-skill\ndisplay_name: My Skill\ncategory: CUSTOM\n---\n# My Skill\n\n## Instructions\n...`}
              value={content}
              onChange={(e) => setContent(e.target.value)}
              disabled={loading}
              rows={10}
              className="font-mono text-xs"
            />
          </TabsContent>
        </Tabs>

        {error && (
          <p className="text-sm text-destructive">{error}</p>
        )}

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => setOpen(false)}
            disabled={loading}
          >
            Cancel
          </Button>
          <Button
            onClick={handleImport}
            disabled={loading || (tab === "url" ? !url.trim() : !content.trim())}
          >
            {loading ? "Importing…" : "Import"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
