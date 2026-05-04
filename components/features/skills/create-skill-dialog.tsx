"use client"

import { useState } from "react"
import { Streamdown } from "streamdown"
import { Sparkles, Loader2, AlertTriangle, ArrowRight, Check } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"

interface CreateSkillDialogProps {
  workspaceId: string
  onCreated?: () => void
  // Optional custom trigger — defaults to the toolbar primary button.
  trigger?: React.ReactNode
}

interface GenerateResponse {
  skill_id: string
  slug: string
  content: string
  scan_status: string
  scan_reason?: string
  description_quality?: string
}

const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/

// CreateSkillDialog wires the user-facing skill-author flow end to end.
// Steps: gather slug + intent → POST /skills/generate → stream the
// resulting SKILL.md back into a preview pane → success closes the
// dialog and invokes onCreated so the browser refetches.
//
// No fake popups, no "coming soon" — every action either calls the
// API or shows the actual error from the API. If ANTHROPIC credentials
// are missing on the workspace the dialog surfaces the precondition
// error verbatim so the user knows where to go.
export function CreateSkillDialog({ workspaceId, onCreated, trigger }: CreateSkillDialogProps) {
  const [open, setOpen] = useState(false)
  const [step, setStep] = useState<"input" | "generating" | "preview">("input")
  const [slug, setSlug] = useState("")
  const [prompt, setPrompt] = useState("")
  const [model, setModel] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<GenerateResponse | null>(null)

  const slugValid = SLUG_PATTERN.test(slug)
  const promptValid = prompt.trim().length >= 20

  function reset() {
    setStep("input")
    setSlug("")
    setPrompt("")
    setModel("")
    setError(null)
    setResult(null)
  }

  async function handleGenerate() {
    if (!slugValid || !promptValid) {
      setError("slug must be kebab-case (e.g. pdf-clean) and prompt must be ≥20 characters")
      return
    }
    setError(null)
    setStep("generating")
    try {
      const body: Record<string, string> = { slug: slug.trim(), prompt: prompt.trim() }
      // Trim before truthiness check — a whitespace-only input is
      // truthy as a string and would bypass the server's default-model
      // fallback by sending "   " on the wire.
      const trimmedModel = model.trim()
      if (trimmedModel) body.model = trimmedModel
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/skills/generate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const msg = await res.text().catch(() => res.statusText)
        // Server returns RFC7807 problem JSON for the typical errors
        // (412 "no Anthropic credential", 409 "slug exists", 502
        // "model rejected the prompt"). Surface .detail when present
        // so the user sees actionable text, not a JSON blob.
        let detail = msg
        try {
          const parsed = JSON.parse(msg) as { detail?: string; title?: string }
          detail = parsed.detail ?? parsed.title ?? msg
        } catch {
          // not JSON; show raw
        }
        setError(`HTTP ${res.status}: ${detail}`)
        setStep("input")
        return
      }
      const data = (await res.json()) as GenerateResponse
      setResult(data)
      setStep("preview")
    } catch (e) {
      const message = e instanceof Error ? e.message : "network error"
      setError(message)
      setStep("input")
    }
  }

  function handleAccept() {
    setOpen(false)
    onCreated?.()
    // Defer reset so the closing animation doesn't show step-zero
    // content for one frame.
    setTimeout(reset, 200)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        // Block close while the LLM call is in flight — otherwise the
        // pending fetch can resolve into the closed dialog, set step
        // back to "preview", and the next open jumps straight into a
        // stale skill body.
        if (!v && step === "generating") return
        setOpen(v)
        if (!v) setTimeout(reset, 200)
      }}
    >
      <DialogTrigger asChild>
        {trigger ?? (
          <button
            type="button"
            className="flex items-center gap-1.5 h-7 px-3 rounded-md text-xs font-medium transition-colors shrink-0 bg-primary/10 text-primary hover:bg-primary/20 border border-primary/20"
          >
            <Sparkles className="h-3 w-3" />
            Create Skill
          </button>
        )}
      </DialogTrigger>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Sparkles className="h-4 w-4 text-violet-400" />
            Create skill via LLM authoring
          </DialogTitle>
          <DialogDescription>
            Server calls Anthropic with a condensed skill-creator prompt
            (
            <a
              href="https://github.com/anthropics/skills/blob/main/skills/skill-creator/SKILL.md"
              target="_blank"
              rel="noreferrer"
              className="text-blue-400 hover:underline"
            >
              upstream reference
            </a>
            ). Requires an active ANTHROPIC credential in the workspace. The result lands as a fresh row with{" "}
            <code className="text-violet-300">source=GENERATED</code>; you can edit the body afterwards from the detail panel.
          </DialogDescription>
        </DialogHeader>

        {step === "input" && (
          <div className="space-y-3">
            <div>
              <Label htmlFor="skill-slug" className="text-xs">Slug</Label>
              <Input
                id="skill-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder="pdf-clean"
                className="font-mono"
                aria-invalid={slug.length > 0 && !slugValid}
              />
              <p className="text-[11px] text-white/40 mt-1">kebab-case, 1–64 chars (a–z, 0–9, hyphen)</p>
            </div>
            <div>
              <Label htmlFor="skill-prompt" className="text-xs">What should this skill do?</Label>
              <Textarea
                id="skill-prompt"
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                rows={6}
                placeholder="Use when the user asks to remove metadata from PDF files and flatten interactive forms before sharing externally."
                aria-invalid={prompt.length > 0 && !promptValid}
              />
              <p className="text-[11px] text-white/40 mt-1">
                Tip: start with a trigger phrase (&ldquo;Use when …&rdquo;, &ldquo;Useful for …&rdquo;) — that&apos;s what the LLM router matches on.
              </p>
            </div>
            <div>
              <Label htmlFor="skill-model" className="text-xs">Model (optional)</Label>
              <Input
                id="skill-model"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder="claude-sonnet-4-6 (default)"
                className="font-mono"
              />
            </div>
            {error && (
              <div className="rounded-md border border-red-500/30 bg-red-500/[0.08] px-3 py-2 text-xs text-red-200 flex items-start gap-2">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
                <span className="break-words">{error}</span>
              </div>
            )}
          </div>
        )}

        {step === "generating" && (
          <div className="flex flex-col items-center justify-center gap-3 py-12">
            <Loader2 className="h-6 w-6 animate-spin text-violet-400" />
            <div className="text-sm text-white/85">Calling Anthropic…</div>
            <div className="text-xs text-white/45">
              Skill-creator prompt → SKILL.md draft. Usually 5–15s.
            </div>
          </div>
        )}

        {step === "preview" && result && (
          <div className="space-y-3">
            <div className="rounded-md border border-emerald-500/30 bg-emerald-500/[0.08] px-3 py-2 text-xs text-emerald-200 flex items-start gap-2">
              <Check className="h-3.5 w-3.5 shrink-0 mt-0.5" />
              <span>
                Saved as <code className="text-emerald-300">{result.slug}</code> ·{" "}
                <code className="text-emerald-300">{result.skill_id}</code>
                {result.description_quality && (
                  <span className="block text-amber-200 mt-1">
                    Linter: {result.description_quality}
                  </span>
                )}
                {result.scan_status === "FLAGGED" && (
                  <span className="block text-red-200 mt-1">
                    Scan flag: {result.scan_reason ?? "see scan_status"}
                  </span>
                )}
              </span>
            </div>
            <div className="max-h-72 overflow-y-auto rounded-md border border-white/[0.08] bg-black/30 p-3 prose prose-invert prose-xs max-w-none">
              <Streamdown>{result.content}</Streamdown>
            </div>
          </div>
        )}

        <DialogFooter>
          {step === "input" && (
            <>
              <Button variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
              <Button
                onClick={handleGenerate}
                disabled={!slugValid || !promptValid}
              >
                <ArrowRight className="h-3 w-3 mr-1" />
                Generate
              </Button>
            </>
          )}
          {step === "generating" && (
            <Button variant="ghost" disabled>
              <Loader2 className="h-3 w-3 mr-1 animate-spin" />
              Generating…
            </Button>
          )}
          {step === "preview" && (
            <>
              <Button variant="ghost" onClick={() => setStep("input")}>Back</Button>
              <Button onClick={handleAccept}>
                <Check className="h-3 w-3 mr-1" />
                Done
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
