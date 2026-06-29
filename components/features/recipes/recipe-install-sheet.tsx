"use client"

import * as React from "react"
import { useRouter } from "next/navigation"
import { Check, ChevronRight, Eye, EyeOff } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import {
  Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription,
} from "@/components/ui/sheet"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"

interface Recipe {
  slug: string
  name: string
  description: string
  icon: string
  color: string
  crew_slug: string
  credentials: { env_var_name: string; provider: string; type: string; label: string; help_url?: string }[]
  mcp_servers: { name: string; display_name: string; transport: string; icon?: string }[]
}

interface PreviewResp {
  recipe: Recipe
  needed_credentials: string[]
  existing_credentials: Record<string, boolean>
  crew_slug_available: boolean
  resolved_crew_slug: string
}

export interface RecipeInstallSheetProps {
  workspaceId: string
  recipeSlug: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onInstalled?: () => void
}

export function RecipeInstallSheet({
  workspaceId, recipeSlug, open, onOpenChange, onInstalled,
}: RecipeInstallSheetProps) {
  const router = useRouter()
  const [step, setStep] = React.useState<1 | 2 | 3>(1)
  const [preview, setPreview] = React.useState<PreviewResp | null>(null)
  const [previewLoading, setPreviewLoading] = React.useState(false)
  const [credValues, setCredValues] = React.useState<Record<string, string>>({})
  const [credLabels, setCredLabels] = React.useState<Record<string, string>>({})
  const [showSecrets, setShowSecrets] = React.useState<Record<string, boolean>>({})
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open || !recipeSlug) {
      setStep(1); setPreview(null); setCredValues({}); setCredLabels({}); setSubmitting(false); setError(null)
      return
    }
    setPreviewLoading(true)
    apiFetch(`/api/v1/recipes/${recipeSlug}/preview?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : null)
      .then((data: PreviewResp | null) => setPreview(data))
      .catch(() => setPreview(null))
      .finally(() => setPreviewLoading(false))
  }, [open, recipeSlug, workspaceId])

  const handleInstall = async () => {
    if (!preview) return
    setSubmitting(true); setError(null)
    try {
      const res = await apiFetch(`/api/v1/recipes/${preview.recipe.slug}/install?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          credential_values: credValues,
          account_labels: credLabels,
        }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(typeof data.error === "string" ? data.error : "Install failed")
        setSubmitting(false)
        return
      }
      const result: { crew_slug: string } = await res.json()
      toast.success(`${preview.recipe.name} installed`)
      onInstalled?.()
      onOpenChange(false)
      router.push(`/crews?crew=${encodeURIComponent(result.crew_slug)}`)
    } catch {
      setError("Network error")
      setSubmitting(false)
    }
  }

  if (!recipeSlug) return null

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="sm:max-w-[640px] p-0 flex flex-col">
        <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <SheetTitle className="text-base">
            Install {preview?.recipe.name ?? "recipe"}
            <span className="ml-2 text-sm text-muted-foreground font-normal">— step {step} of 3</span>
          </SheetTitle>
          <SheetDescription className="text-[12.5px]">
            {step === 1 && "Preview what will be created."}
            {step === 2 && "Provide the credentials this recipe needs."}
            {step === 3 && "Confirm and install."}
          </SheetDescription>
        </SheetHeader>

        <StepStrip step={step} />

        <div className="flex-1 px-5 py-4 overflow-y-auto">
          {previewLoading && (
            <div className="flex items-center justify-center py-12">
              <Spinner className="h-5 w-5 text-muted-foreground" />
            </div>
          )}

          {!previewLoading && !preview && (
            <p className="text-sm text-muted-foreground">Recipe not found.</p>
          )}

          {!previewLoading && preview && step === 1 && (
            <PreviewStep preview={preview} />
          )}

          {!previewLoading && preview && step === 2 && (
            <CredentialsStep
              preview={preview}
              credValues={credValues}
              setCredValues={setCredValues}
              credLabels={credLabels}
              setCredLabels={setCredLabels}
              showSecrets={showSecrets}
              setShowSecrets={setShowSecrets}
            />
          )}

          {!previewLoading && preview && step === 3 && (
            <ConfirmStep preview={preview} credValues={credValues} />
          )}
        </div>

        {error && (
          <div className="px-5 py-2 text-xs text-red-400 border-t border-white/10">{error}</div>
        )}

        <div className="px-5 py-3 border-t border-white/10 flex items-center gap-2">
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
            className="text-sm px-3 py-1.5 rounded text-muted-foreground hover:text-foreground"
          >
            Cancel
          </button>
          {step > 1 && (
            <button
              type="button"
              onClick={() => setStep((s) => (s - 1) as typeof s)}
              disabled={submitting}
              className="text-sm px-3 py-1.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5 ml-auto"
            >
              ← Back
            </button>
          )}
          <button
            type="button"
            onClick={() => {
              if (step === 3) { handleInstall(); return }
              setStep((s) => (s + 1) as typeof s)
            }}
            disabled={!isStepValid(step, preview, credValues) || submitting}
            className={cn("text-sm px-3.5 py-1.5 rounded bg-blue-500 hover:bg-blue-400 text-white disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-1.5", step === 1 && "ml-auto")}
          >
            {submitting && <Spinner className="h-3 w-3" />}
            {step === 3 ? (submitting ? "Installing..." : "Install") : "Continue"}
            {step < 3 && !submitting && <ChevronRight className="h-3.5 w-3.5" />}
          </button>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function StepStrip({ step }: { step: 1 | 2 | 3 }) {
  const labels = ["Preview", "Credentials", "Confirm"] as const
  return (
    <nav className="px-5 py-3 border-b border-white/10 bg-card/50 flex items-center gap-3">
      {([1, 2, 3] as const).map((n, i) => (
        <React.Fragment key={n}>
          <div className="flex items-center gap-2 text-[12px] shrink-0">
            <div className={cn(
              "h-6 w-6 rounded-full border text-[11px] font-semibold flex items-center justify-center",
              n < step ? "bg-emerald-500/20 border-emerald-400/50 text-emerald-300"
                : n === step ? "bg-blue-500/20 border-blue-400 text-blue-300 ring-2 ring-blue-400/20"
                : "bg-card border-white/10 text-muted-foreground",
            )}>
              {n < step ? <Check className="h-3 w-3" strokeWidth={3} /> : n}
            </div>
            <span className={cn("font-medium", n !== step && "opacity-60")}>{labels[n - 1]}</span>
          </div>
          {i < 2 && <div className={cn("flex-1 h-px", n < step ? "bg-emerald-400/40" : "bg-white/10")} />}
        </React.Fragment>
      ))}
    </nav>
  )
}

function PreviewStep({ preview }: { preview: PreviewResp }) {
  const r = preview.recipe
  return (
    <div className="space-y-4">
      <div className="rounded-md border border-white/10 bg-zinc-950 p-4">
        <div className="text-sm font-medium">{r.name}</div>
        <div className="text-xs text-muted-foreground mt-1">{r.description}</div>
      </div>

      <div className="space-y-1.5">
        <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">Will create</div>
        <ul className="space-y-1.5 text-xs">
          <li className="flex items-center gap-2">
            <Check className="h-3 w-3 text-emerald-400" />
            <span>1 crew &mdash; <span className="font-mono">{preview.resolved_crew_slug}</span>{!preview.crew_slug_available && <span className="text-amber-400 ml-1">(suffixed; original taken)</span>}</span>
          </li>
          {r.mcp_servers.map((s) => (
            <li key={s.name} className="flex items-center gap-2">
              <Check className="h-3 w-3 text-emerald-400" />
              <span>1 MCP server &mdash; <span className="font-mono">{s.display_name}</span> ({s.transport})</span>
            </li>
          ))}
          {r.credentials.map((c) => {
            const have = preview.existing_credentials[c.env_var_name]
            return (
              <li key={c.env_var_name} className="flex items-center gap-2">
                {have ? (
                  <>
                    <Check className="h-3 w-3 text-blue-400" />
                    <span className="text-muted-foreground">Reuse credential <span className="font-mono">{c.env_var_name}</span></span>
                  </>
                ) : (
                  <>
                    <Check className="h-3 w-3 text-emerald-400" />
                    <span>1 credential &mdash; <span className="font-mono">{c.env_var_name}</span> ({c.label})</span>
                  </>
                )}
              </li>
            )
          })}
        </ul>
      </div>

      {preview.needed_credentials.length > 0 && (
        <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs">
          You&apos;ll be prompted for {preview.needed_credentials.length} credential{preview.needed_credentials.length === 1 ? "" : "s"} on the next step.
          The values are encrypted with AES-256-GCM before being stored.
        </div>
      )}
    </div>
  )
}

function CredentialsStep({
  preview, credValues, setCredValues, credLabels, setCredLabels, showSecrets, setShowSecrets,
}: {
  preview: PreviewResp
  credValues: Record<string, string>
  setCredValues: React.Dispatch<React.SetStateAction<Record<string, string>>>
  credLabels: Record<string, string>
  setCredLabels: React.Dispatch<React.SetStateAction<Record<string, string>>>
  showSecrets: Record<string, boolean>
  setShowSecrets: React.Dispatch<React.SetStateAction<Record<string, string | boolean>>> | React.Dispatch<React.SetStateAction<Record<string, boolean>>>
}) {
  const needed = preview.recipe.credentials.filter((c) => !preview.existing_credentials[c.env_var_name])
  if (needed.length === 0) {
    return (
      <div className="rounded-md border border-emerald-500/30 bg-emerald-500/[0.05] p-4 text-sm">
        All credentials this recipe needs are already in your workspace. Continue to confirm.
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {needed.map((c) => (
        <div key={c.env_var_name} className="space-y-2 rounded-md border border-white/10 bg-zinc-950 p-3">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium font-mono">{c.env_var_name}</span>
            <Badge variant="outline" className="text-[10px]">{c.label}</Badge>
          </div>
          <div className="space-y-1.5">
            <label className="block text-[11px] text-muted-foreground">Value</label>
            <div className="relative">
              <input
                type={showSecrets[c.env_var_name] ? "text" : "password"}
                value={credValues[c.env_var_name] ?? ""}
                onChange={(e) => setCredValues((s) => ({ ...s, [c.env_var_name]: e.target.value }))}
                placeholder={c.help_url ? `Get from ${c.help_url}` : "Paste value..."}
                className="w-full bg-black/40 border border-white/10 rounded px-2.5 py-1.5 pr-9 text-xs font-mono outline-none focus:border-blue-400"
              />
              <button
                type="button"
                onClick={() => (setShowSecrets as React.Dispatch<React.SetStateAction<Record<string, boolean>>>)((s) => ({ ...s, [c.env_var_name]: !s[c.env_var_name] }))}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                {showSecrets[c.env_var_name] ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
              </button>
            </div>
          </div>
          <div className="space-y-1.5">
            <label className="block text-[11px] text-muted-foreground">Account label (optional)</label>
            <input
              value={credLabels[c.env_var_name] ?? ""}
              onChange={(e) => setCredLabels((s) => ({ ...s, [c.env_var_name]: e.target.value }))}
              placeholder={`e.g. production`}
              className="w-full bg-black/40 border border-white/10 rounded px-2.5 py-1.5 text-xs outline-none focus:border-blue-400"
            />
          </div>
          {c.help_url && (
            <a href={c.help_url} target="_blank" rel="noreferrer" className="text-[11px] text-blue-400 hover:underline">
              Where do I find this?
            </a>
          )}
        </div>
      ))}
    </div>
  )
}

function ConfirmStep({ preview, credValues }: { preview: PreviewResp; credValues: Record<string, string> }) {
  const willCreate = preview.recipe.credentials.filter((c) => !preview.existing_credentials[c.env_var_name]).length
  const willReuse = preview.recipe.credentials.length - willCreate
  return (
    <div className="space-y-3">
      <div className="rounded-md border border-emerald-500/30 bg-emerald-500/[0.05] p-4 text-sm space-y-1">
        <div className="font-medium">Ready to install</div>
        <ul className="text-xs text-foreground/80 list-disc list-inside space-y-0.5">
          <li>1 crew &mdash; <span className="font-mono">{preview.resolved_crew_slug}</span></li>
          <li>{preview.recipe.mcp_servers.length} MCP server{preview.recipe.mcp_servers.length === 1 ? "" : "s"}</li>
          <li>{willCreate} new credential{willCreate === 1 ? "" : "s"}, {willReuse} reused</li>
        </ul>
      </div>
      <div className="text-xs text-muted-foreground">
        Atomic install: if any step fails, nothing is created.
        You&apos;ll be redirected to the new crew once it&apos;s ready.
      </div>
      {/* Sanity guard against accidentally posting empty values */}
      {Object.values(credValues).some((v) => !v.trim()) && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/[0.05] p-2 text-xs text-amber-300">
          Some credential values are empty. Go back to fill them in.
        </div>
      )}
    </div>
  )
}

function isStepValid(step: 1 | 2 | 3, preview: PreviewResp | null, credValues: Record<string, string>): boolean {
  if (!preview) return false
  if (step === 1) return true
  const needed = preview.recipe.credentials.filter((c) => !preview.existing_credentials[c.env_var_name])
  if (step === 2) {
    return needed.every((c) => (credValues[c.env_var_name] ?? "").trim().length > 0)
  }
  if (step === 3) {
    return needed.every((c) => (credValues[c.env_var_name] ?? "").trim().length > 0)
  }
  return false
}
