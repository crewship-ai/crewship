"use client"

import { useState } from "react"
import { z } from "zod"
import { Upload, AlertTriangle, Check, Eye } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"

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

/** Shared by the three text fields: the shell's phone rule (44px = h-12 here,
 *  because `--spacing` is 0.23rem) applied to a control that is h-8 on a
 *  pointer device. */
const FIELD_CLASS = "h-8 text-xs max-sm:h-12 max-sm:text-sm"

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
  // Lets the sub-bar match the trigger to <SubBarSecondary> sizing
  // (h-7 gap-1.5 text-xs) so Import reads as a neutral row-1 action.
  triggerClassName?: string
}

export function ImportSkillDialog({
  workspaceId,
  onImported,
  triggerLabel,
  triggerVariant = "outline",
  triggerSize = "sm",
  triggerClassName,
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

  const ready =
    tab === "url" ? url.trim() !== "" : tab === "content" ? content.trim() !== "" : repoUrl.trim() !== ""

  // What the shell's discard guard protects. Anything typed or toggled counts;
  // a finished bulk import does not, because the fields are then a receipt of
  // work already done rather than input that would be lost.
  const dirty =
    bulkResult == null &&
    (url.trim() !== "" ||
      content.trim() !== "" ||
      repoUrl.trim() !== "" ||
      repoRef.trim() !== "" ||
      repoVendor.trim() !== "" ||
      unsafeLicense ||
      dryRun)

  function closeAndReset() {
    setOpen(false)
    reset()
  }

  async function handleImport() {
    setError(null)
    setBulkResult(null)
    setLoading(true)

    try {
      if (tab === "repo") {
        const res = await apiFetch(
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
          ? { url: url.trim(), allow_unsafe_license: unsafeLicense }
          : { content: content.trim(), allow_unsafe_license: unsafeLicense }

      const res = await apiFetch(
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
    <>
      {/* CreateSurface has no trigger slot — it is opened by state, not by a
          Radix DialogTrigger — so the button carries the popup semantics the
          primitive used to add for it. */}
      <Button
        variant={triggerVariant}
        size={triggerSize}
        className={triggerClassName}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen(true)}
      >
        {triggerLabel ?? (
          <>
            <Upload className="mr-2 h-4 w-4" />
            Import Skill
          </>
        )}
      </Button>

      <CreateSurface
        open={open}
        onOpenChange={(next) => {
          // When the dialog is dismissed via backdrop click or Escape,
          // the radix primitive only flips `open` to false — without
          // also clearing the form state, the next time the user opens
          // the dialog it still has whatever URL/content/repo they
          // typed before. Mirroring the explicit Cancel/Close path
          // here keeps every close shape consistent.
          if (!next) {
            reset()
          }
          setOpen(next)
        }}
        size="md"
        dirty={dirty}
        discardLabel="this import"
        onSubmit={() => {
          if (!loading && ready) void handleImport()
        }}
      >
        <CreateSurfaceHeader
          concept="skills"
          context="Skills"
          title="Import Skill"
          description="Import a skill from a GitHub URL or paste a SKILL.md file directly."
          onClose={closeAndReset}
        />

        <CreateSurfaceBody className="space-y-3">
          <CreateSurfaceChoice
            ariaLabel="Import source"
            value={tab}
            onChange={setTab}
            options={[
              { value: "url", label: "From URL" },
              { value: "repo", label: "From Repo" },
              { value: "content", label: "Paste Content" },
            ]}
          />

          {tab === "url" && (
            <CreateSurfaceField
              label="SKILL.md URL"
              htmlFor="skill-url"
              hint={
                <>
                  Supports GitHub URLs, raw URLs, or shorthand{" "}
                  <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                    owner/repo/path.md
                  </code>
                </>
              }
            >
              <Input
                id="skill-url"
                autoFocus
                placeholder="https://github.com/org/skills/blob/main/my-skill/SKILL.md"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                disabled={loading}
                className={`${FIELD_CLASS} font-mono`}
              />
            </CreateSurfaceField>
          )}

          {tab === "repo" && (
            <>
              <CreateSurfaceField
                label="Git repository URL"
                htmlFor="repo-url"
                hint={
                  <>
                    Server clones <code className="font-mono">--depth 1</code>, walks{" "}
                    <code className="font-mono">**/SKILL.md</code>, and gates each by SPDX license
                    (MIT, Apache-2.0, BSD-2/3, ISC, CC0-1.0, MPL-2.0, Unlicense, 0BSD).
                  </>
                }
              >
                <Input
                  id="repo-url"
                  autoFocus
                  placeholder="https://github.com/anthropics/skills"
                  value={repoUrl}
                  onChange={(e) => setRepoUrl(e.target.value)}
                  disabled={loading}
                  className={`${FIELD_CLASS} font-mono`}
                />
              </CreateSurfaceField>

              <CreateSurfaceGrid>
                <CreateSurfaceField label="Ref (optional)" htmlFor="repo-ref">
                  <Input
                    id="repo-ref"
                    placeholder="main"
                    value={repoRef}
                    onChange={(e) => setRepoRef(e.target.value)}
                    disabled={loading}
                    className={`${FIELD_CLASS} font-mono`}
                  />
                </CreateSurfaceField>
                <CreateSurfaceField label="Vendor namespace" htmlFor="repo-vendor">
                  <Input
                    id="repo-vendor"
                    placeholder="community"
                    value={repoVendor}
                    onChange={(e) => setRepoVendor(e.target.value)}
                    disabled={loading}
                    className={FIELD_CLASS}
                  />
                </CreateSurfaceField>
              </CreateSurfaceGrid>

              {/* The label is a real <label htmlFor> INSIDE the row's label
                  slot: CreateSurfaceToggleRow renders its label as plain text,
                  so without this the checkbox has no accessible name and the
                  text is not a tap target — both of which the pair of
                  `<label><Checkbox/>…</label>` rows here had before.

                  `max-sm:min-h-12` on both: `CreateSurfaceToggleRow` is a
                  plain flex row with no click handler of its own — measured,
                  the "44px row" a thumb sees is really an 18px label sliver
                  and a 14.7px `size-4` checkbox floating inside it, and a tap
                  anywhere else in that row (the icon column, the row's own
                  padding) hits neither. `label[for]` already forwards a click
                  to the checkbox by id regardless of DOM position (verified:
                  clicking the text toggles it), so growing each slot's own
                  box to `min-h-12` — text via the label wrapper, checkbox via
                  a second label around it — extends both real targets to the
                  full row height without touching the checkbox's own visible
                  size or `create-surface.tsx`. */}
              <CreateSurfaceToggleRow
                icon={Eye}
                accent="teal"
                label={
                  <label
                    htmlFor="repo-dry-run"
                    className="flex cursor-pointer items-center max-sm:min-h-12"
                  >
                    Dry run (preview only)
                  </label>
                }
                control={
                  <label
                    htmlFor="repo-dry-run"
                    className="flex cursor-pointer items-center justify-center max-sm:min-h-12 max-sm:min-w-12"
                  >
                    <Checkbox
                      id="repo-dry-run"
                      checked={dryRun}
                      onCheckedChange={(v) => setDryRun(v === true)}
                    />
                  </label>
                }
              />
              <CreateSurfaceToggleRow
                icon={AlertTriangle}
                accent="red"
                label={
                  <label
                    htmlFor="repo-unsafe-license"
                    className="flex cursor-pointer items-center text-warn max-sm:min-h-12"
                  >
                    Skip license gate
                  </label>
                }
                control={
                  <label
                    htmlFor="repo-unsafe-license"
                    className="flex cursor-pointer items-center justify-center max-sm:min-h-12 max-sm:min-w-12"
                  >
                    <Checkbox
                      id="repo-unsafe-license"
                      checked={unsafeLicense}
                      onCheckedChange={(v) => setUnsafeLicense(v === true)}
                    />
                  </label>
                }
              />

              {bulkResult && (
                <CreateSurfaceNotice tone="ok" icon={Check}>
                  <span className="block font-medium text-success">
                    {dryRun ? "Dry run" : `Imported ${bulkResult.total_imported} of ${bulkResult.total_found}`}
                  </span>
                  {bulkResult.imported.slice(0, 5).map((s) => (
                    <span key={s.skill_id} className="block font-mono text-[11px] text-muted-foreground">
                      + {s.created ? "created" : "updated"} {s.slug}
                    </span>
                  ))}
                  {bulkResult.imported.length > 5 && (
                    <span className="block text-[11px] text-muted-foreground-soft">
                      …+{bulkResult.imported.length - 5} more
                    </span>
                  )}
                  {bulkResult.skipped.length > 0 && (
                    <details className="mt-1">
                      <summary className="cursor-pointer text-warn">
                        {bulkResult.skipped.length} skipped
                      </summary>
                      <ul className="mt-1 space-y-0.5 text-[11px] text-muted-foreground">
                        {bulkResult.skipped.slice(0, 8).map((s) => (
                          <li key={s.path}>
                            <span className="font-mono">{s.path}</span>: {s.reason}
                          </li>
                        ))}
                      </ul>
                    </details>
                  )}
                </CreateSurfaceNotice>
              )}
            </>
          )}

          {tab === "content" && (
            <CreateSurfaceField label="SKILL.md Content" htmlFor="skill-content">
              <Textarea
                id="skill-content"
                autoFocus
                placeholder={`---\nname: my-skill\ndisplay_name: My Skill\ncategory: CUSTOM\n---\n# My Skill\n\n## Instructions\n...`}
                value={content}
                onChange={(e) => setContent(e.target.value)}
                disabled={loading}
                rows={10}
                className="font-mono text-xs"
              />
            </CreateSurfaceField>
          )}
        </CreateSurfaceBody>

        <CreateSurfaceRefusal message={error} onDismiss={() => setError(null)} />

        <CreateSurfaceFooter
          onCancel={closeAndReset}
          cancelLabel={bulkResult ? "Close" : "Cancel"}
          primaryLabel={
            loading ? "Importing…" : tab === "repo" ? (dryRun ? "Preview" : "Import repo") : "Import"
          }
          primaryIcon={Upload}
          primaryDisabled={!ready}
          busy={loading}
          onPrimary={handleImport}
        />
      </CreateSurface>
    </>
  )
}
