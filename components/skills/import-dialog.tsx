"use client"

import { useState } from "react"
import { z } from "zod"
import { Upload, AlertTriangle, Check } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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

const BulkImportResultSchema = z.object({
  source: z.string(),
  total_found: z.number(),
  total_imported: z.number(),
  imported: z.array(z.object({ skill_id: z.string(), slug: z.string(), created: z.boolean() })),
  skipped: z.array(z.object({ path: z.string(), slug: z.string().optional(), reason: z.string() })),
})
type BulkImportResult = z.infer<typeof BulkImportResultSchema>

interface ImportSkillDialogProps {
  workspaceId: string
  onImported?: (result: ImportResult) => void
  // The 3-panel skills browser passes a custom trigger label/variant so
  // the Import CTA can sit in the left rail without looking like a top
  // toolbar action. Defaults preserve the previous "Import Skill" CTA
  // for callers that haven't migrated.
  triggerLabel?: React.ReactNode
  triggerVariant?: "default" | "outline" | "secondary" | "ghost"
  triggerSize?: "default" | "sm" | "lg" | "icon"
}

export function ImportSkillDialog({
  workspaceId,
  onImported,
  triggerLabel,
  triggerVariant = "outline",
  triggerSize = "sm",
}: ImportSkillDialogProps) {
  const [open, setOpen] = useState(false)
  const [tab, setTab] = useState<"url" | "content" | "repo">("url")
  const [url, setUrl] = useState("")
  const [content, setContent] = useState("")
  const [repoUrl, setRepoUrl] = useState("")
  const [repoRef, setRepoRef] = useState("")
  const [repoVendor, setRepoVendor] = useState("")
  const [unsafeLicense, setUnsafeLicense] = useState(false)
  const [dryRun, setDryRun] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [bulkResult, setBulkResult] = useState<BulkImportResult | null>(null)

  function reset() {
    setError(null)
    setBulkResult(null)
    setUrl("")
    setContent("")
    setRepoUrl("")
    setRepoRef("")
    setRepoVendor("")
    setUnsafeLicense(false)
    setDryRun(false)
  }

  async function handleImport() {
    setError(null)
    setBulkResult(null)
    setLoading(true)

    try {
      if (tab === "repo") {
        const res = await fetch(
          `/api/v1/workspaces/${workspaceId}/skills/bulk-import`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              git_url: repoUrl.trim(),
              git_ref: repoRef.trim(),
              vendor: repoVendor.trim(),
              allow_unsafe_license: unsafeLicense,
              dry_run: dryRun,
            }),
          },
        )
        if (!res.ok) {
          const data = await res.json().catch(() => ({}))
          setError(data.detail ?? data.error ?? `Bulk import failed (HTTP ${res.status})`)
          return
        }
        const parsed = BulkImportResultSchema.parse(await res.json())
        setBulkResult(parsed)
        // Don't auto-close so the user sees the imported/skipped
        // breakdown — the bulk flow is a real action with real
        // skipped-license-and-such information that's worth a beat.
        if (!dryRun && parsed.total_imported > 0) {
          // Trigger the parent reload so the new skills appear in
          // the grid; keep the dialog open until the user dismisses.
          onImported?.({
            skill_id: parsed.imported[0]?.skill_id ?? "",
            name: parsed.imported[0]?.slug ?? "",
            slug: parsed.imported[0]?.slug ?? "",
            created: parsed.imported[0]?.created ?? true,
          })
        }
        return
      }

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
      reset()
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
        <Button variant={triggerVariant} size={triggerSize}>
          {triggerLabel ?? (
            <>
              <Upload className="mr-2 h-4 w-4" />
              Import Skill
            </>
          )}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Import Skill</DialogTitle>
          <DialogDescription>
            Import a skill from a GitHub URL or paste a SKILL.md file directly.
          </DialogDescription>
        </DialogHeader>

        <Tabs value={tab} onValueChange={(v) => setTab(v as "url" | "content" | "repo")}>
          <TabsList className="w-full">
            <TabsTrigger value="url" className="flex-1">
              From URL
            </TabsTrigger>
            <TabsTrigger value="repo" className="flex-1">
              From Repo
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

          <TabsContent value="repo" className="mt-4 space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="repo-url">Git repository URL</Label>
              <Input
                id="repo-url"
                placeholder="https://github.com/anthropics/skills"
                value={repoUrl}
                onChange={(e) => setRepoUrl(e.target.value)}
                disabled={loading}
              />
              <p className="text-xs text-muted-foreground">
                Server clones <code className="font-mono">--depth 1</code>, walks <code className="font-mono">**/SKILL.md</code>, and gates each by SPDX license (MIT, Apache-2.0, BSD-2/3, ISC, CC0-1.0, MPL-2.0, Unlicense, 0BSD).
              </p>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div className="space-y-1.5">
                <Label htmlFor="repo-ref" className="text-xs">Ref (optional)</Label>
                <Input id="repo-ref" placeholder="main" value={repoRef} onChange={(e) => setRepoRef(e.target.value)} disabled={loading} />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="repo-vendor" className="text-xs">Vendor namespace</Label>
                <Input id="repo-vendor" placeholder="community" value={repoVendor} onChange={(e) => setRepoVendor(e.target.value)} disabled={loading} />
              </div>
            </div>
            <div className="flex items-center gap-4 pt-1">
              <label className="flex items-center gap-2 text-xs">
                <Checkbox checked={dryRun} onCheckedChange={(v) => setDryRun(v === true)} />
                Dry run (preview only)
              </label>
              <label className="flex items-center gap-2 text-xs text-amber-300">
                <Checkbox checked={unsafeLicense} onCheckedChange={(v) => setUnsafeLicense(v === true)} />
                <span className="inline-flex items-center gap-1">
                  <AlertTriangle className="h-3 w-3" />
                  Skip license gate
                </span>
              </label>
            </div>
            {bulkResult && (
              <div className="rounded-md border border-emerald-500/30 bg-emerald-500/[0.06] p-2 text-xs space-y-1">
                <div className="text-emerald-200 font-medium flex items-center gap-1">
                  <Check className="h-3 w-3" />
                  {dryRun ? "Dry run" : `Imported ${bulkResult.total_imported} of ${bulkResult.total_found}`}
                </div>
                {bulkResult.imported.slice(0, 5).map((s) => (
                  <div key={s.skill_id} className="text-white/65 font-mono text-[11px]">
                    + {s.created ? "created" : "updated"} {s.slug}
                  </div>
                ))}
                {bulkResult.imported.length > 5 && (
                  <div className="text-white/45 text-[11px]">…+{bulkResult.imported.length - 5} more</div>
                )}
                {bulkResult.skipped.length > 0 && (
                  <details className="mt-1">
                    <summary className="cursor-pointer text-amber-300">{bulkResult.skipped.length} skipped</summary>
                    <ul className="mt-1 space-y-0.5 text-[11px] text-white/55">
                      {bulkResult.skipped.slice(0, 8).map((s) => (
                        <li key={s.path}><span className="font-mono">{s.path}</span>: {s.reason}</li>
                      ))}
                    </ul>
                  </details>
                )}
              </div>
            )}
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
            onClick={() => { setOpen(false); reset() }}
            disabled={loading}
          >
            {bulkResult ? "Close" : "Cancel"}
          </Button>
          <Button
            onClick={handleImport}
            disabled={
              loading ||
              (tab === "url" && !url.trim()) ||
              (tab === "content" && !content.trim()) ||
              (tab === "repo" && !repoUrl.trim())
            }
          >
            {loading ? "Importing…" : tab === "repo" ? (dryRun ? "Preview" : "Import repo") : "Import"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
