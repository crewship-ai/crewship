"use client"

// Unified credential form — the single source of truth for how users
// type a credential into Crewship. Used by:
//   * AddCredentialDialog       (mode="create")
//   * EditCredentialDialog      (mode="edit")
//   * CredentialDetailSheet     (inline value rewrite, mode="edit")
//
// The wizardised "Connect service" flow (OAuth handshakes, setup-token,
// PAT-with-test) lives separately and stays intact — that codepath
// genuinely needs the provider as a first-class step. This form is the
// flat "paste a secret" path Doppler/Vercel are built around.

import * as React from "react"
import { Eye, EyeOff, Loader2, ChevronDown, ChevronRight, X, Plus, Check, ChevronsUpDown, FlaskConical, CheckCircle2, XCircle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Badge } from "@/components/ui/badge"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import { detectProvider, detectType, detectFromValue } from "@/lib/credential-provider"
import { getBrand } from "@/lib/credential-providers/registry"
import { BrandPicker } from "./brand-picker"
import { cn } from "@/lib/utils"

export type CredentialType = "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET" | "OAUTH2"
export type CredentialScope = "WORKSPACE" | "CREW"

export interface CredentialFormValues {
  name: string
  description: string
  value: string
  type: CredentialType
  provider: string
  scope: CredentialScope
  crewIds: string[]
  tags: string[]
  expiresAt: string // YYYY-MM-DD or ""
}

export const EMPTY_FORM: CredentialFormValues = {
  name: "",
  description: "",
  value: "",
  type: "API_KEY",
  provider: "NONE",
  scope: "WORKSPACE",
  crewIds: [],
  tags: [],
  expiresAt: "",
}

interface Crew { id: string; name: string }

export interface CredentialFormProps {
  workspaceId: string
  mode: "create" | "edit"
  initial?: Partial<CredentialFormValues>
  /** Hide the value input entirely (e.g. metadata-only edit). */
  hideValue?: boolean
  /** Submit handler — return a string error message to surface, or null on success. */
  onSubmit: (values: CredentialFormValues) => Promise<string | null>
  onCancel: () => void
  submitLabel?: string
  /** Optional hook to test the value with the provider before submit. */
  onTest?: (values: CredentialFormValues) => Promise<{ valid: boolean; error?: string }>
  /** Existing tag list in the workspace — drives the tag autocomplete. */
  knownTags?: string[]
}

export function CredentialForm({
  workspaceId,
  mode,
  initial,
  hideValue,
  onSubmit,
  onCancel,
  submitLabel,
  onTest,
  knownTags,
}: CredentialFormProps) {
  const [values, setValues] = React.useState<CredentialFormValues>(() => ({
    ...EMPTY_FORM,
    ...initial,
  }))
  const [showValue, setShowValue] = React.useState(false)
  const [advancedOpen, setAdvancedOpen] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<{ valid: boolean; error?: string } | null>(null)
  const [tagDraft, setTagDraft] = React.useState("")
  const [crews, setCrews] = React.useState<Crew[]>([])
  const [crewsLoading, setCrewsLoading] = React.useState(false)
  const [crewPopoverOpen, setCrewPopoverOpen] = React.useState(false)
  // Track whether the user has manually edited provider so name-driven
  // auto-detect doesn't keep overriding their choice.
  const providerTouched = React.useRef(mode === "edit")

  React.useEffect(() => {
    if (values.scope === "CREW" && crews.length === 0 && !crewsLoading) {
      setCrewsLoading(true)
      fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        .then((r) => r.ok ? r.json() : [])
        .then((data: Crew[]) => setCrews(Array.isArray(data) ? data : []))
        .catch(() => setCrews([]))
        .finally(() => setCrewsLoading(false))
    }
  }, [values.scope, workspaceId, crews.length, crewsLoading])

  const setField = <K extends keyof CredentialFormValues>(k: K, v: CredentialFormValues[K]) => {
    setValues((prev) => ({ ...prev, [k]: v }))
  }

  const handleNameChange = (next: string) => {
    setValues((prev) => {
      const patch: Partial<CredentialFormValues> = { name: next }
      if (!providerTouched.current) {
        patch.provider = detectProvider(next)
      }
      // Always re-derive type from name for create flow — type is a
      // pure function of the name suffix. In edit mode we keep the
      // stored type so users don't see it flip when fixing a typo.
      if (mode === "create") {
        patch.type = detectType(next)
      }
      return { ...prev, ...patch }
    })
    setTestResult(null)
  }

  // Paste-first flow: when the user pastes a recognisable secret
  // shape (sk-ant-, ghp_, AIza...) into a still-empty form, pre-fill
  // the name + provider for them. Mirrors Doppler / 1Password.
  const handleValueChange = (next: string) => {
    setTestResult(null)
    setValues((prev) => {
      const patch: Partial<CredentialFormValues> = { value: next }
      const shouldAutofill =
        mode === "create" && prev.name.trim() === "" && next.trim().length >= 8
      if (shouldAutofill) {
        const guess = detectFromValue(next)
        if (guess) {
          patch.name = guess.suggestedName
          if (!providerTouched.current) patch.provider = guess.provider
          patch.type = detectType(guess.suggestedName)
        }
      }
      return { ...prev, ...patch }
    })
  }

  const addTag = (raw: string) => {
    const t = raw.trim().toLowerCase()
    if (!t) return
    if (values.tags.includes(t)) return
    if (values.tags.length >= 8) return
    setField("tags", [...values.tags, t])
  }

  const removeTag = (t: string) => {
    setField("tags", values.tags.filter((x) => x !== t))
  }

  const handleTest = async () => {
    if (!onTest) return
    setTesting(true)
    setTestResult(null)
    try {
      const result = await onTest(values)
      setTestResult(result)
    } finally {
      setTesting(false)
    }
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    if (!values.name.trim()) {
      setError("Name is required")
      return
    }
    if (mode === "create" && !hideValue && !values.value.trim()) {
      setError("Value is required")
      return
    }
    if (values.scope === "CREW" && values.crewIds.length === 0) {
      setError("Pick at least one crew, or switch scope to Workspace")
      return
    }
    setSubmitting(true)
    try {
      const result = await onSubmit({
        ...values,
        name: values.name.trim(),
        description: values.description.trim(),
        value: values.value.trim(),
      })
      if (result) setError(result)
    } catch {
      setError("Network error")
    } finally {
      setSubmitting(false)
    }
  }

  const detected = getBrand(values.provider)
  const DetectedIcon = detected.Icon

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {/* Name + brand picker. The picker doubles as auto-detection
          preview: typing "notion" suggests Notion automatically; user
          can click the chip to override or pick a different brand
          from the full ~140-entry registry. */}
      <div className="space-y-1.5">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor="cred-name" className="text-xs">Name</Label>
          <BrandPicker
            value={values.provider}
            onChange={(key) => {
              providerTouched.current = true
              setField("provider", key)
            }}
          />
        </div>
        <div className="relative">
          <Input
            id="cred-name"
            placeholder="e.g. STRIPE_API_KEY"
            value={values.name}
            onChange={(e) => handleNameChange(e.target.value)}
            className="font-mono text-sm pr-9"
            autoFocus={mode === "create"}
            required
          />
          {detected.key !== "NONE" && (
            <div
              className="absolute right-2.5 top-1/2 -translate-y-1/2"
              style={{ color: detected.hex }}
              title={`Detected: ${detected.label}`}
            >
              <DetectedIcon className="h-3.5 w-3.5" />
            </div>
          )}
        </div>
        <p className="text-[11px] text-muted-foreground">
          ENV variable name your agent will read. Brand is auto-detected from the name —
          click the chip above to pick manually.
        </p>
      </div>

      {/* Value */}
      {!hideValue && (
        <div className="space-y-1.5">
          <Label htmlFor="cred-value" className="text-xs">
            Value
            {mode === "edit" && (
              <span className="ml-1 text-[10px] font-normal text-muted-foreground">
                (leave empty to keep existing)
              </span>
            )}
          </Label>
          <div className="relative">
            <Input
              id="cred-value"
              type={showValue ? "text" : "password"}
              placeholder={mode === "edit" ? "•••••••••••••••" : "Paste secret value"}
              value={values.value}
              onChange={(e) => handleValueChange(e.target.value)}
              className="pr-10 font-mono text-sm"
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              className="absolute right-1.5 top-1/2 -translate-y-1/2"
              onClick={() => setShowValue((s) => !s)}
              aria-label={showValue ? "Hide value" : "Show value"}
            >
              {showValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
            </Button>
          </div>
          {/* Test button only for CLI providers — those are the brands
              Crewship itself uses inside agent containers, where we
              maintain real upstream HTTP probes. For passive secrets
              (Notion, Stripe, Linear, …) the agent talks to the API
              directly, so a "Test value" button here would be a
              placebo that returns "no validation available". */}
          {onTest && values.value.trim().length > 0 && detected.cli && (
            <div className="flex items-center gap-2 pt-1">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleTest}
                disabled={testing}
                className="h-7 text-[11px]"
              >
                {testing
                  ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                  : <FlaskConical className="mr-1.5 h-3 w-3" />}
                Test value
              </Button>
              {testResult && (
                <span className={cn(
                  "flex items-center gap-1 text-[11px]",
                  testResult.valid ? "text-emerald-400" : "text-red-400",
                )}>
                  {testResult.valid
                    ? <CheckCircle2 className="h-3 w-3" />
                    : <XCircle className="h-3 w-3" />}
                  {testResult.valid ? "Valid" : (testResult.error || "Invalid")}
                </span>
              )}
            </div>
          )}
        </div>
      )}

      {/* Tags row — promoted out of "Advanced" because tagging is the
          primary organisation tool now that grouping is gone. */}
      <div className="space-y-1.5">
        <Label className="text-xs">Tags</Label>
        <div className="flex items-center flex-wrap gap-1.5 rounded-md border border-white/10 bg-zinc-950 px-2 py-1.5 min-h-[34px]">
          {values.tags.map((t) => (
            <Badge
              key={t}
              variant="outline"
              className="text-[10px] gap-1 font-mono"
            >
              {t}
              <button
                type="button"
                onClick={() => removeTag(t)}
                className="hover:text-red-400"
                aria-label={`Remove tag ${t}`}
              >
                <X className="h-2.5 w-2.5" />
              </button>
            </Badge>
          ))}
          <input
            type="text"
            list="cred-tag-suggestions"
            value={tagDraft}
            onChange={(e) => setTagDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === ",") {
                e.preventDefault()
                addTag(tagDraft)
                setTagDraft("")
              } else if (e.key === "Backspace" && tagDraft === "" && values.tags.length > 0) {
                removeTag(values.tags[values.tags.length - 1])
              }
            }}
            onBlur={() => {
              if (tagDraft.trim()) {
                addTag(tagDraft)
                setTagDraft("")
              }
            }}
            placeholder={values.tags.length === 0 ? "prod, billing, internal…" : ""}
            className="flex-1 min-w-[80px] bg-transparent text-xs outline-none placeholder:text-muted-foreground/60"
          />
          {knownTags && knownTags.length > 0 && (
            <datalist id="cred-tag-suggestions">
              {knownTags
                .filter((t) => !values.tags.includes(t))
                .map((t) => <option key={t} value={t} />)}
            </datalist>
          )}
        </div>
      </div>

      {/* Advanced toggle */}
      <button
        type="button"
        onClick={() => setAdvancedOpen((o) => !o)}
        className="flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground transition-colors"
      >
        {advancedOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        Advanced
        <span className="text-muted-foreground/60">
          (description, expiry, scope, provider override)
        </span>
      </button>

      {advancedOpen && (
        <div className="space-y-3 pl-4 border-l border-white/10">
          {/* Description */}
          <div className="space-y-1">
            <Label htmlFor="cred-desc" className="text-xs">Description</Label>
            <Textarea
              id="cred-desc"
              placeholder="What is this credential for?"
              value={values.description}
              onChange={(e) => setField("description", e.target.value)}
              rows={2}
              className="text-sm"
            />
          </div>

          {/* Expires */}
          <div className="space-y-1">
            <Label htmlFor="cred-expires" className="text-xs">Expires on</Label>
            <Input
              id="cred-expires"
              type="date"
              value={values.expiresAt}
              onChange={(e) => setField("expiresAt", e.target.value)}
              className="text-sm w-[180px]"
            />
            <p className="text-[10px] text-muted-foreground">
              Optional — drives the &quot;Expiring&quot; KPI and the 30-day warning banner.
            </p>
          </div>

          {/* Scope */}
          <div className="space-y-1">
            <Label htmlFor="cred-scope" className="text-xs">Visible to</Label>
            <Select
              value={values.scope}
              onValueChange={(v) => {
                setField("scope", v as CredentialScope)
                if (v === "WORKSPACE") setField("crewIds", [])
              }}
            >
              <SelectTrigger id="cred-scope" className="text-sm">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="WORKSPACE">Whole workspace</SelectItem>
                <SelectItem value="CREW">Specific crews only</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {/* Crews picker */}
          {values.scope === "CREW" && (
            <div className="space-y-1">
              <Label className="text-xs">Crews</Label>
              {crewsLoading ? (
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Loader2 className="h-3 w-3 animate-spin" /> Loading crews…
                </div>
              ) : (
                <>
                  <Popover open={crewPopoverOpen} onOpenChange={setCrewPopoverOpen}>
                    <PopoverTrigger asChild>
                      <Button
                        variant="outline"
                        role="combobox"
                        aria-expanded={crewPopoverOpen}
                        className="w-full justify-between font-normal text-sm"
                      >
                        {values.crewIds.length === 0
                          ? "Select crews…"
                          : `${values.crewIds.length} crew${values.crewIds.length > 1 ? "s" : ""} selected`}
                        <ChevronsUpDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-50" />
                      </Button>
                    </PopoverTrigger>
                    <PopoverContent className="w-[--radix-popover-trigger-width] p-0" align="start">
                      <Command>
                        <CommandInput placeholder="Search crews…" />
                        <CommandList>
                          <CommandEmpty>No crews found.</CommandEmpty>
                          <CommandGroup>
                            {crews.map((crew) => {
                              const isSelected = values.crewIds.includes(crew.id)
                              return (
                                <CommandItem
                                  key={crew.id}
                                  value={crew.name}
                                  onSelect={() => {
                                    setField(
                                      "crewIds",
                                      isSelected
                                        ? values.crewIds.filter((id) => id !== crew.id)
                                        : [...values.crewIds, crew.id],
                                    )
                                  }}
                                >
                                  <Check className={cn("mr-2 h-4 w-4", isSelected ? "opacity-100" : "opacity-0")} />
                                  {crew.name}
                                </CommandItem>
                              )
                            })}
                          </CommandGroup>
                        </CommandList>
                      </Command>
                    </PopoverContent>
                  </Popover>
                  {values.crewIds.length > 0 && (
                    <div className="flex flex-wrap gap-1 pt-1">
                      {values.crewIds.map((id) => {
                        const c = crews.find((c) => c.id === id)
                        return c ? (
                          <Badge
                            key={id}
                            variant="secondary"
                            className="cursor-pointer text-[10px]"
                            onClick={() => setField("crewIds", values.crewIds.filter((x) => x !== id))}
                          >
                            {c.name}
                            <X className="ml-1 h-2.5 w-2.5" />
                          </Badge>
                        ) : null
                      })}
                    </div>
                  )}
                </>
              )}
            </div>
          )}

        </div>
      )}

      {error && (
        <div className="text-xs text-red-400 border border-red-500/30 bg-red-500/[0.05] rounded-md px-3 py-2">
          {error}
        </div>
      )}

      <div className="flex items-center gap-2 pt-2 border-t border-white/10">
        <Button type="button" variant="outline" onClick={onCancel} disabled={submitting} size="sm">
          Cancel
        </Button>
        <div className="ml-auto flex items-center gap-1.5">
          {!advancedOpen && (
            <button
              type="button"
              onClick={() => setAdvancedOpen(true)}
              className="text-[11px] text-muted-foreground hover:text-foreground inline-flex items-center gap-1"
            >
              <Plus className="h-3 w-3" /> More options
            </button>
          )}
          <Button type="submit" disabled={submitting} size="sm">
            {submitting && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}
            {submitLabel ?? (mode === "create" ? "Save secret" : "Save changes")}
          </Button>
        </div>
      </div>
    </form>
  )
}
