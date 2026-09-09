"use client"

// Unified credential form — the single source of truth for how users
// type a credential into Crewship. Used by:
//   * AddCredentialDialog       (mode="create")
//   * EditCredentialDialog      (mode="edit")
//   * CredentialDetailSheet     (inline value rewrite, mode="edit")
//
// The wizardised "Connect service" flow (OAuth handshakes, setup-token,
// PAT-with-test) was removed — it never got mounted. Reviving OAuth
// from /credentials is tracked separately. This form is the flat
// "paste a secret" path Doppler/Vercel are built around.

import { credentialTagClassName } from "@/lib/credentials/tag-accent"
import * as React from "react"
import { Eye, EyeOff, ChevronDown, ChevronRight, X, Plus, Check, ChevronsUpDown, FlaskConical, CheckCircle2, XCircle, Tag, KeyRound, FileText, ShieldCheck } from "lucide-react"
import { Checkbox } from "@/components/ui/checkbox"
import { Spinner } from "@/components/ui/spinner"
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
import { isValidEnvVarName, suggestEnvVarName } from "@/lib/env-var-name"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import { CREDENTIAL_TIERS } from "@/lib/credentials/tiers"
import { BrandPicker } from "./brand-picker"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { CreateSurfaceBody, CreateSurfaceFooter } from "@/components/layout/create-surface"
import { credentialEditPresentation } from "@/lib/credentials/edit-presentation"

export type CredentialType = string
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
  /** Keeper tier, 1–4. See CREDENTIAL_TIERS. */
  securityLevel: number
  username?: string
}

/**
 * The Keeper tiers now live in lib/credentials/tiers.ts, beside the colours the
 * rail and the overview donut draw them with. The table was here first, when the
 * picker was the only surface that knew a tier existed; re-exported so the
 * wizard's import keeps working, and so there is exactly one table.
 */
export { CREDENTIAL_TIERS }

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
  securityLevel: 1,
}

interface Crew { id: string; name: string }

export interface CredentialFormProps {
  workspaceId: string
  mode: "create" | "edit"
  initial?: Partial<CredentialFormValues>
  /** Hide the value input entirely (e.g. metadata-only edit). */
  hideValue?: boolean
  /** A provider identity cannot be changed by picking a decorative brand. */
  lockProvider?: boolean
  fieldKeys?: string[]
  additionalFields?: React.ReactNode
  /** Submit handler — return a string error message to surface, or null on success. */
  onSubmit: (values: CredentialFormValues) => Promise<string | null>
  onCancel: () => void
  submitLabel?: string
  /** Optional hook to test the value with the provider before submit. */
  onTest?: (values: CredentialFormValues) => Promise<{ valid: boolean; error?: string }>
  /** Existing tag list in the workspace — drives the tag autocomplete. */
  knownTags?: string[]
  /** Use the shared create/edit shell's scroll region and fixed action bar. */
  surface?: boolean
  onDirtyChange?: (dirty: boolean) => void
}

export function CredentialForm({
  workspaceId,
  mode,
  initial,
  hideValue,
  lockProvider = false,
  fieldKeys = [],
  additionalFields,
  onSubmit,
  onCancel,
  submitLabel,
  onTest,
  knownTags,
  surface = false,
  onDirtyChange,
}: CredentialFormProps) {
  const [values, setValues] = React.useState<CredentialFormValues>(() => ({
    ...EMPTY_FORM,
    ...initial,
  }))
  const [showValue, setShowValue] = React.useState(false)
  const [replaceValue, setReplaceValue] = React.useState(false)
  const formRef = React.useRef<HTMLFormElement>(null)
  const originalValues = React.useRef(values)
  const [tagDraft, setTagDraft] = React.useState("")
  React.useEffect(() => {
    onDirtyChange?.(!!tagDraft.trim() || JSON.stringify(values) !== JSON.stringify(originalValues.current))
  }, [values, tagDraft, onDirtyChange])
  const [advancedOpen, setAdvancedOpen] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<{ valid: boolean; error?: string } | null>(null)
  const [crews, setCrews] = React.useState<Crew[]>([])
  const [crewsLoading, setCrewsLoading] = React.useState(false)
  const [crewPopoverOpen, setCrewPopoverOpen] = React.useState(false)
  // Track whether the user has manually edited provider so name-driven
  // auto-detect doesn't keep overriding their choice.
  const providerTouched = React.useRef(mode === "edit")
  // Env-var-name validation. The name IS the env var agents read, so
  // it must match ^[A-Z_][A-Z0-9_]*$. Errors only show after first
  // blur (or submit) so we don't scream at half-typed lowercase input.
  const [nameBlurred, setNameBlurred] = React.useState(false)
  // The name the credential had when the form opened. An EXISTING
  // invalid name is tolerated (warn, don't block) so legacy
  // credentials stay editable — we only hard-block newly typed ones.
  const initialName = React.useRef((initial?.name ?? "").trim())
  const presentation = credentialEditPresentation(values.type, fieldKeys)

  const trimmedName = values.name.trim()
  const nameIsLegacy = mode === "edit" && trimmedName === initialName.current
  const nameInvalid = trimmedName !== "" && !isValidEnvVarName(trimmedName)
  const nameSuggestion = nameInvalid ? suggestEnvVarName(trimmedName) : null

  // Fetch the crew list once per workspace, tracked by a ref rather than by
  // `crews.length === 0 && !crewsLoading`. That guard could not tell "not
  // fetched yet" from "fetched, and this workspace has no crews": length stays
  // 0 in both cases, so when the request settled and `finally` cleared
  // crewsLoading, the condition was satisfied all over again and the effect
  // fired a second time. Same on the failure path, where `catch` also sets [].
  // A workspace with no crews is the common case mid-onboarding — exactly when
  // a wasted round trip is least welcome, and against a 120/min limiter.
  const crewsFetchedFor = React.useRef<string | null>(null)
  React.useEffect(() => {
    if (values.scope !== "CREW" || crewsFetchedFor.current === workspaceId) return
    crewsFetchedFor.current = workspaceId
    setCrewsLoading(true)
    apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data: Crew[]) => setCrews(Array.isArray(data) ? data : []))
      .catch(() => setCrews([]))
      .finally(() => setCrewsLoading(false))
  }, [values.scope, workspaceId])

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
    setValues((prev) => prev.tags.includes(t) || prev.tags.length >= 8 ? prev : { ...prev, tags: [...prev.tags, t] })
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
    if (nameInvalid && !nameIsLegacy && !surface) {
      // Legacy names that were already invalid stay submittable (see
      // nameIsLegacy above) — blocking them would make old credentials
      // uneditable. Anything newly typed must be a valid env var name.
      setNameBlurred(true)
      setError(
        nameSuggestion
          ? `Name must be a valid env var name — try ${nameSuggestion}`
          : "Name must be a valid env var name (uppercase letters, digits, underscores; can't start with a digit)",
      )
      return
    }
    if (values.type === "USERPASS" && !values.username?.trim()) {
      setError("Username is required")
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
        tags: [...new Set([...values.tags, tagDraft.trim().toLowerCase()].filter(Boolean))].slice(0, 8),
        name: values.name.trim(),
        description: values.description.trim(),
        value: values.value,
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

  // Whether a "Test value" button is worth showing is the server's answer, not
  // ours. It used to be BrandEntry.cli — the brands Crewship drives inside agent
  // containers — which is a different set from the brands the server can probe:
  // GITHUB, GITLAB and VERCEL have real upstream probes and are not cli:true, so
  // the button was hidden for three providers that would have answered. Keeping
  // a second opinion here is what made them drift; ask instead.
  const [testable, setTestable] = React.useState(false)
  React.useEffect(() => {
    let cancelled = false
    if (!values.provider || values.provider === "NONE") {
      setTestable(false)
      return
    }
    void (async () => {
      try {
        const res = await apiFetch(
          `/api/v1/credentials/default-env-var?provider=${encodeURIComponent(values.provider)}` +
            `&type=${encodeURIComponent(values.type)}`,
        )
        if (!res.ok) throw new Error(String(res.status))
        const body = (await res.json()) as { testable?: boolean }
        if (!cancelled) setTestable(Boolean(body.testable))
      } catch {
        // Unreachable server: hide the button. A Test that cannot run is
        // exactly the placebo this gate exists to prevent.
        if (!cancelled) setTestable(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [values.provider, values.type])

  return (
    <form ref={formRef} onSubmit={handleSubmit} className={surface ? "flex min-h-0 flex-1 flex-col overflow-hidden" : "space-y-4"}>
      <CreateSurfaceBody padded={surface} scroll={surface} className={surface ? "space-y-5 pb-6" : "contents"}>
      {surface && <div className="flex items-center gap-2 border-b border-border/60 pb-3 text-sm font-medium"><FileText aria-hidden="true" className="size-4 text-muted-foreground" /> Credential details</div>}
      {/* Name + brand picker. The picker doubles as auto-detection
          preview: typing "notion" suggests Notion automatically; user
          can click the chip to override or pick a different brand
          from the full ~140-entry registry. */}
      <div className="space-y-1.5">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor="cred-name" className="text-xs">Name</Label>
          {/* The brand IS the icon — it is what the rail, the list and the
              credential's own page draw. It sat here unlabelled, which made
              the one control that changes a credential's face read as a
              read-only badge. */}
          {!lockProvider && <span className="flex items-center gap-1.5">
            <Label className="text-[11px] text-muted-foreground">Icon</Label>
            <BrandPicker
              value={values.provider}
              onChange={(key) => {
                providerTouched.current = true
                setField("provider", key)
              }}
            />
          </span>}
        </div>
        <div className="relative">
          <Input
            id="cred-name"
            placeholder="e.g. STRIPE_API_KEY"
            value={values.name}
            onChange={(e) => handleNameChange(e.target.value)}
            onBlur={() => setNameBlurred(true)}
            className={cn(
              "text-sm", !surface && "font-mono pr-9",
              !surface && nameBlurred && nameInvalid && !nameIsLegacy && "border-destructive/50",
            )}
            aria-invalid={!surface && nameBlurred && nameInvalid && !nameIsLegacy}
            autoFocus={mode === "create"}
            required
          />
          {!surface && detected.key !== "NONE" && (
            <div
              className="absolute right-2.5 top-1/2 -translate-y-1/2"
              style={{ color: brandColor(detected) }}
              title={`Detected: ${detected.label}`}
            >
              <DetectedIcon className="h-3.5 w-3.5" />
            </div>
          )}
        </div>
        {surface ? (
          mode === "edit" && values.name !== initialName.current ? <p role="status" className="text-xs text-warn">
            Renaming? Update any agent binding that uses the old name as an environment variable.
          </p> : null
        ) : nameBlurred && nameInvalid && !nameIsLegacy ? (
          <div className="flex items-center gap-2 flex-wrap text-[11px] text-destructive">
            <span>
              Must be a valid env var name — uppercase letters, digits and underscores,
              not starting with a digit.
            </span>
            {nameSuggestion && (
              <button
                type="button"
                onClick={() => handleNameChange(nameSuggestion)}
                className="font-mono rounded border border-destructive/40 px-1.5 py-0.5 hover:bg-destructive/10 transition-colors"
              >
                Use {nameSuggestion}
              </button>
            )}
          </div>
        ) : nameInvalid && nameIsLegacy ? (
          <p className="text-[11px] text-warn">
            This name isn&apos;t a valid env var name; agents may not see it as an
            environment variable. You can keep it, or rename
            {nameSuggestion ? <> to <button type="button" onClick={() => handleNameChange(nameSuggestion)} className="font-mono underline underline-offset-2 hover:text-warn">{nameSuggestion}</button></> : " it"}.
          </p>
        ) : (
          <p className="text-[11px] text-muted-foreground">
            ENV variable name your agent will read. Brand is auto-detected from the name —
            click the chip above to pick manually.
          </p>
        )}
      </div>

      {/* Value */}
      {surface && values.type === "USERPASS" && <div className="space-y-1.5">
        <Label htmlFor="cred-username" className="text-xs">Username</Label>
        <Input id="cred-username" autoComplete="off" value={values.username ?? ""} onChange={(e) => setField("username", e.target.value)} required />
      </div>}
      {surface && <div className="space-y-1.5">
        <Label htmlFor="cred-description" className="text-xs"><FileText className="h-3.5 w-3.5" /> Description</Label>
        <Textarea id="cred-description" placeholder="What is this secret used for?"
          rows={2} className="min-h-16 resize-y" value={values.description} onChange={(e) => setField("description", e.target.value)} />
      </div>}
      {!hideValue && surface && mode === "edit" && <div className={cn("rounded-xl border p-3 space-y-2 transition-colors", replaceValue ? "border-warn/40 bg-warn/5" : "border-border/60 bg-muted/20")}>
        <label className="flex items-center gap-2 text-sm font-medium">
          <KeyRound aria-hidden="true" className="h-4 w-4 text-muted-foreground" />
          <Checkbox checked={replaceValue} onCheckedChange={(checked) => {
            setReplaceValue(checked === true)
            if (checked !== true) { setField("value", ""); setShowValue(false) }
          }} />
          Replace the stored secret
        </label>
        <p className="text-xs text-muted-foreground">{replaceValue ? "Updates Crewship only. Change the value at the issuing service separately." : "Your stored value stays unchanged."}</p>
      </div>}
      {!hideValue && (!surface || mode !== "edit" || replaceValue) && (
        <div className={cn("space-y-1.5", surface && "rounded-xl border border-warn/30 bg-card p-4")}>
          <Label htmlFor="cred-value" className="text-xs">
            {mode === "edit" && surface ? `Replace ${presentation.label.toLowerCase()}` : mode === "edit" ? "Replace secret value" : "Value"}
            {mode === "edit" && (
              <span className="ml-1 text-[10px] font-normal text-muted-foreground">
                (leave empty to keep existing)
              </span>
            )}
          </Label>
          <div className="relative">
            {surface && presentation.multiline ? <Textarea
              id="cred-value" rows={6} placeholder={presentation.label}
              autoComplete="off" spellCheck={false}
              value={values.value} onChange={(e) => handleValueChange(e.target.value)}
              className={cn("font-mono text-xs pr-10", !showValue && "[-webkit-text-security:disc]")}
            /> : <Input
              id="cred-value"
              type={showValue ? "text" : "password"}
              placeholder={mode === "edit" ? "Paste a new value only to replace the existing secret" : "Paste secret value"}
              autoComplete="new-password"
              value={values.value}
              onChange={(e) => handleValueChange(e.target.value)}
              className="pr-10 font-mono text-sm"
            />}
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
          {surface && <p className="text-xs text-muted-foreground">{presentation.hint}</p>}
          {/* Test button only where the server maintains a real upstream probe.
              For passive secrets (Notion, Stripe, Linear, …) the agent talks to
              the API directly and we have nothing to check against, so a "Test
              value" button here would be a placebo that returns "no validation
              available". Which providers those are is decided in Go, next to the
              probes — see probeSupportedProviders. */}
          {onTest && values.value.trim().length > 0 && testable && (
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
                  ? <Spinner className="mr-1.5 h-3 w-3" />
                  : <FlaskConical className="mr-1.5 h-3 w-3" />}
                Test value
              </Button>
              {testResult && (
                <span className={cn(
                  "flex items-center gap-1 text-[11px]",
                  testResult.valid ? "text-success" : "text-destructive",
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

      {additionalFields}
      {/* Tags row — promoted out of "Advanced" because tagging is the
          primary organisation tool now that grouping is gone. */}
      <div className="space-y-1.5">
        <Label htmlFor="cred-tags" className="text-xs"><Tag className="h-3.5 w-3.5" /> Tags <span className="ml-auto text-muted-foreground font-normal">{values.tags.length}/8</span></Label>
        <div className="flex items-center flex-wrap gap-1.5 rounded-md border border-white/10 bg-background px-2 py-1.5 min-h-[34px]">
          {values.tags.map((t) => (
            <Badge
              key={t}
              variant="outline"
              className={cn("text-[10px] gap-1 font-mono", credentialTagClassName(t))}
            >
              {t}
              <button
                type="button"
                onClick={() => removeTag(t)}
                className="hover:text-destructive"
                aria-label={`Remove tag ${t}`}
              >
                <X className="h-2.5 w-2.5" />
              </button>
            </Badge>
          ))}
          <input
            id="cred-tags"
            aria-label="Tags"
            aria-describedby="cred-tags-hint"
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
            className="flex-1 min-w-[80px] bg-transparent text-xs outline-none placeholder:text-muted-foreground"
          />
          {knownTags && knownTags.length > 0 && (
            <datalist id="cred-tag-suggestions">
              {knownTags
                .filter((t) => !values.tags.includes(t))
                .map((t) => <option key={t} value={t} />)}
            </datalist>
          )}
        </div>
        <p id="cred-tags-hint" className="text-[11px] text-muted-foreground">Enter or comma to add · × to remove.</p>
      </div>

      {/* Advanced toggle */}
      <button
        type="button"
        aria-expanded={advancedOpen}
        onClick={() => setAdvancedOpen((o) => !o)}
        aria-controls="credential-access-settings"
        className="flex min-h-10 w-full items-center gap-2 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors"
      >
        {advancedOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        <ShieldCheck className="h-3.5 w-3.5" /> Access & security
        <span className="text-muted-foreground">
          {surface ? (advancedOpen ? "Hide settings" : "Change settings") : "(description, expiry, scope, provider override)"}
        </span>
      </button>

      {advancedOpen && (
        <div id="credential-access-settings" className="space-y-4 rounded-xl border border-border/60 p-4">
          {surface && (values.scope !== initial?.scope || JSON.stringify(values.crewIds) !== JSON.stringify(initial?.crewIds ?? [])) &&
            <p role="status" className="rounded-lg border border-warn/30 p-3 text-xs text-warn">Access will change to {values.scope === "WORKSPACE" ? "workspace scope" : `${values.crewIds.length} selected crews`}. Agents relying on inherited access may gain or lose this credential. Existing direct grants and delivery bindings must be reviewed separately.</p>}
          {/* Description */}
          {!surface && <div className="space-y-1">
            <Label htmlFor="cred-desc" className="text-xs">Description</Label>
            <Textarea
              id="cred-desc"
              placeholder="What is this credential for?"
              value={values.description}
              onChange={(e) => setField("description", e.target.value)}
              rows={2}
              className="text-sm"
            />
          </div>}

          {/* Expires */}
          {!lockProvider && <div className="space-y-1">
            <Label htmlFor="cred-expires" className="text-xs">Expires on</Label>
            <Input
              id="cred-expires"
              type="date"
              value={values.expiresAt}
              onChange={(e) => setField("expiresAt", e.target.value)}
              className="text-sm w-[180px]"
            />
            <p className="text-[10px] text-muted-foreground">
              Optional. Reminds you before this credential expires.
            </p>
          </div>}

          {/* Provider sign-in has its own protection policy. */}
          {!lockProvider && <div className="space-y-1">
            <Label htmlFor="cred-tier" className="text-xs">Keeper tier</Label>
            <Select
              value={String(values.securityLevel)}
              onValueChange={(v) => setField("securityLevel", Number(v))}
            >
              <SelectTrigger id="cred-tier" className="text-sm">
                <SelectValue>{CREDENTIAL_TIERS.find((tier) => tier.level === values.securityLevel)?.label}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                {CREDENTIAL_TIERS.map((t) => (
                  <SelectItem key={t.level} value={String(t.level)}>
                    <span className="flex flex-col items-start gap-0.5 py-0.5">
                      <span>{t.label}</span>
                      <span className="text-[10px] text-muted-foreground">{t.blast}</span>
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {/* What the choice costs, for the tier actually selected. */}
            <p className={cn(
              "text-[10px] leading-relaxed",
              values.securityLevel >= 4 ? "text-warn" : "text-muted-foreground",
            )}>
              {CREDENTIAL_TIERS.find((t) => t.level === values.securityLevel)?.consequence}
            </p>
          </div>}

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
                  <Spinner className="h-3 w-3" /> Loading crews…
                </div>
              ) : (
                <>
                  <Popover open={crewPopoverOpen} onOpenChange={setCrewPopoverOpen} modal>
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
        <div className="text-xs text-destructive border border-destructive/30 bg-destructive/[0.05] rounded-md px-3 py-2">
          {error}
        </div>
      )}

      </CreateSurfaceBody>
      {surface ? <CreateSurfaceFooter
        onCancel={onCancel}
        hint="Changes to access affect agents using this credential."
        primaryLabel={submitLabel ?? "Save changes"}
        primaryDisabled={submitting}
        busy={submitting}
        onPrimary={() => formRef.current?.requestSubmit()}
      /> : <div className="flex items-center gap-2 pt-2 border-t border-white/10">
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
            {submitting && <Spinner className="mr-1.5 h-3 w-3" />}
            {submitLabel ?? (mode === "create" ? "Save secret" : "Save changes")}
          </Button>
        </div>
      </div>}
    </form>
  )
}
