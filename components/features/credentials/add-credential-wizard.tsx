"use client"

/**
 * Add-credential flow — PRD-CREDENTIALS-V2-2026 §0 (KISS scope), wireframe
 * screens 2–4.
 *
 * Three steps, in the order the user actually decides things:
 *
 *   1. WHAT SHAPE is it — Token / Login / Key pair / SSH key / File /
 *      Certificate. The shape decides the fields; the brand does not. The
 *      earlier draft of this flow started from a ~150-entry brand catalog and
 *      that was rejected: six shapes plus custom fields cover the same ground
 *      without a table that is wrong for somebody.
 *   2. THE VALUES the shape implies, plus a human name for the account.
 *   3. WHO GETS IT and under WHICH ENV VAR — scope, then slot.
 *
 * The brand is detected from the pasted value (`ghp_` → GitHub) or from the
 * name, and contributes exactly two things: an icon, and a suggested slot
 * name. Both are hints. Nothing here refuses to continue because a brand was
 * not recognised — §0 item 5: "the user knows the env var name better than we
 * do; a wrong row in a catalog is worse than no row".
 *
 * Why the name and the slot are separate boxes (§2.5b): `credentials.name` is
 * WHICH ACCOUNT ("github-acme"), the slot is WHAT THE CONTAINER SEES
 * ("GH_TOKEN"). Fusing them is what made ten GitHub accounts in one workspace
 * impossible — UNIQUE(workspace_id, name) meant the second one would have had
 * to be called GH_TOKEN too.
 */

import * as React from "react"
import {
  Check, ChevronLeft, ChevronsUpDown, FileText, KeyRound,
  Plus, ShieldCheck, Terminal, User, X,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"
import { defaultEnvVarName } from "@/lib/credential-provider"
import {
  brandColor, detectBrandFromName, detectBrandFromValue, getBrand,
} from "@/lib/credential-providers/registry"
import {
  CREDENTIAL_ITEM_TYPES, extraFieldsFor, getItemType,
  type CustomFieldDraft, type ItemTypeKey,
} from "@/lib/credentials/item-types"
import { isValidEnvVarName, suggestEnvVarName } from "@/lib/env-var-name"
import { cn } from "@/lib/utils"
import { BrandPicker } from "./brand-picker"
import { CREDENTIAL_TIERS } from "./credential-form"

const TYPE_ICON: Record<ItemTypeKey, React.ComponentType<{ className?: string }>> = {
  TOKEN: KeyRound,
  LOGIN: User,
  KEYPAIR: Terminal,
  SSH_KEY: KeyRound,
  FILE: FileText,
  CERTIFICATE: ShieldCheck,
}

interface Crew { id: string; name: string }

export interface AddCredentialWizardProps {
  workspaceId: string
  onSuccess: () => void
  onCancel: () => void
  knownTags?: string[]
}

type Step = "type" | "values" | "scope"

export function AddCredentialWizard({
  workspaceId, onSuccess, onCancel, knownTags,
}: AddCredentialWizardProps) {
  const { abilities } = useAbilities()
  // POST /credentials/bindings is roleManage — OWNER/ADMIN — and the handler
  // repeats the check. A MANAGER may create the credential but not claim a
  // slot for it, so the step is hidden from them rather than offered and 403'd.
  const canBind = abilities.can("manage", "Credential")

  const [step, setStep] = React.useState<Step>("type")
  // Keeper tier. Defaults to L1 — the column's default — so a wizard run that
  // ignores this control behaves exactly as it did before the control existed.
  const [securityLevel, setSecurityLevel] = React.useState(1)
  const [itemTypeKey, setItemTypeKey] = React.useState<ItemTypeKey>("TOKEN")
  const [primaryValue, setPrimaryValue] = React.useState("")
  const [extras, setExtras] = React.useState<Record<string, string>>({})
  const [custom, setCustom] = React.useState<CustomFieldDraft[]>([])
  const [name, setName] = React.useState("")
  const [username, setUsername] = React.useState("")
  const [accountLabel, setAccountLabel] = React.useState("")
  const [tags, setTags] = React.useState<string[]>([])
  const [tagDraft, setTagDraft] = React.useState("")
  const [provider, setProvider] = React.useState("NONE")
  const providerTouched = React.useRef(false)
  const [scope, setScope] = React.useState<"WORKSPACE" | "CREW">("WORKSPACE")
  const [crewIds, setCrewIds] = React.useState<string[]>([])
  const [crews, setCrews] = React.useState<Crew[]>([])
  const [crewPopoverOpen, setCrewPopoverOpen] = React.useState(false)
  const [slot, setSlot] = React.useState("")
  const [slotTouched, setSlotTouched] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [warning, setWarning] = React.useState<string | null>(null)

  const itemType = getItemType(itemTypeKey)

  const crewsFetchedFor = React.useRef<string | null>(null)
  React.useEffect(() => {
    if (scope !== "CREW" || crewsFetchedFor.current === workspaceId) return
    crewsFetchedFor.current = workspaceId
    apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: Crew[]) => setCrews(Array.isArray(data) ? data : []))
      .catch(() => setCrews([]))
  }, [scope, workspaceId])

  /** The brand, and the slot it suggests. A hint on both counts. */
  const detected = React.useMemo(
    () => detectBrandFromValue(primaryValue) ?? detectBrandFromName(name),
    [primaryValue, name],
  )
  const suggestedSlot = detected ? defaultEnvVarName(detected) : null

  React.useEffect(() => {
    if (!detected || providerTouched.current) return
    setProvider(detected.key)
  }, [detected])

  // Prefill the slot from the detection until the user types their own. Never
  // overwrite what they typed — the suggestion is the weaker opinion.
  React.useEffect(() => {
    if (slotTouched || !suggestedSlot) return
    setSlot(suggestedSlot)
  }, [suggestedSlot, slotTouched])

  const brand = getBrand(provider)
  const BrandIcon = brand.Icon

  // Without a binding the credential is delivered under its own NAME (the
  // pre-P3 behaviour the backend still honours), so the name has to be a legal
  // env var in that case — and is free-form when a slot is set. We warn rather
  // than block: the server does not reject either shape, and a hard stop here
  // would be the UI inventing a rule.
  const nameIsEnvVar = isValidEnvVarName(name.trim())
  const nameSuggestion = nameIsEnvVar ? null : suggestEnvVarName(name.trim())

  const missingRequired = React.useMemo(() => {
    if (!primaryValue.trim()) return itemType.primary.label
    if (itemType.usernameOnRow && !username.trim()) return "Username"
    for (const f of itemType.extra) {
      if (f.required && !(extras[f.key] ?? "").trim()) return f.label
    }
    return null
  }, [itemType, primaryValue, username, extras])

  async function submit() {
    setError(null)
    setWarning(null)
    if (!name.trim()) {
      setError("Give the credential a name — it identifies the account, not the env var.")
      return
    }
    if (scope === "CREW" && crewIds.length === 0) {
      setError("Pick at least one crew, or switch the scope back to the whole workspace.")
      return
    }
    setSubmitting(true)
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        value: primaryValue,
        type: itemType.credentialType,
        provider,
        scope,
        tags,
      }
      body.security_level = securityLevel
      if (itemType.usernameOnRow && username.trim()) body.username = username.trim()
      if (accountLabel.trim()) body.account_label = accountLabel.trim()
      if (scope === "CREW") body.crew_ids = crewIds

      const res = await apiFetch(`/api/v1/credentials?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(typeof data.error === "string" ? data.error : `Couldn't save the credential (HTTP ${res.status}).`)
        return
      }
      const created = (await res.json().catch(() => ({}))) as { id?: string }
      const credentialId = created?.id

      // Everything past this point is a follow-up write on a credential that
      // ALREADY EXISTS. A failure here is reported as a warning, never as
      // "save failed" — telling the user nothing was saved when a secret is
      // now in the vault is the worse of the two lies.
      const problems: string[] = []
      const fields = extraFieldsFor(itemTypeKey, extras, custom)
      if (credentialId && fields.length > 0) {
        for (const field of fields) {
          try {
            const fr = await apiFetch(
              `/api/v1/credentials/${encodeURIComponent(credentialId)}/fields` +
                `?workspace_id=${encodeURIComponent(workspaceId)}`,
              {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(field),
              },
            )
            if (!fr.ok) problems.push(field.key)
          } catch {
            problems.push(field.key)
          }
        }
      }

      if (credentialId && canBind && slot.trim()) {
        const targets = scope === "CREW" ? crewIds : [null]
        for (const crewId of targets) {
          try {
            const br = await apiFetch(
              `/api/v1/credentials/bindings?workspace_id=${encodeURIComponent(workspaceId)}`,
              {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                  credential_id: credentialId,
                  scope: crewId ? "CREW" : "WORKSPACE",
                  crew_id: crewId ?? "",
                  agent_id: "",
                  slot: slot.trim(),
                }),
              },
            )
            if (!br.ok) {
              const data = await br.json().catch(() => ({}))
              problems.push(
                typeof data.error === "string" ? data.error : `slot ${slot.trim()} (HTTP ${br.status})`,
              )
            }
          } catch {
            problems.push(`slot ${slot.trim()}`)
          }
        }
      }

      if (problems.length > 0) {
        setWarning(
          `Saved “${name.trim()}”, but some parts did not land: ${problems.join("; ")}. ` +
            `Open the credential and finish it from the Fields tab.`,
        )
        onSuccess()
        return
      }
      onSuccess()
    } catch {
      setError("Network error while saving the credential.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="space-y-4">
      <StepBar step={step} canBind={canBind} />

      {step === "type" && (
        <div className="space-y-3">
          <p className="text-[11px] text-muted-foreground">
            The shape decides which boxes you fill. Any brand fits one of these — the brand only
            contributes an icon and a suggested variable name.
          </p>
          <div className="grid grid-cols-3 gap-2">
            {CREDENTIAL_ITEM_TYPES.map((t) => {
              const Icon = TYPE_ICON[t.key]
              const selected = t.key === itemTypeKey
              return (
                <button
                  key={t.key}
                  type="button"
                  aria-pressed={selected}
                  onClick={() => {
                    setItemTypeKey(t.key)
                    setExtras({})
                  }}
                  className={cn(
                    "rounded-lg border p-3 text-center transition-colors",
                    selected
                      ? "border-primary/60 bg-primary/10"
                      : "border-white/10 bg-background hover:border-white/25",
                  )}
                >
                  <Icon className="mx-auto mb-1.5 h-4 w-4 text-muted-foreground" />
                  <div className="text-xs font-medium">{t.label}</div>
                  <div className="text-[10px] text-muted-foreground">{t.blurb}</div>
                </button>
              )
            })}
          </div>
          <div className="flex items-center gap-2 pt-2 border-t border-white/10">
            <Button type="button" variant="outline" size="sm" onClick={onCancel}>Cancel</Button>
            <Button type="button" size="sm" className="ml-auto" onClick={() => setStep("values")}>
              Continue
            </Button>
          </div>
        </div>
      )}

      {step === "values" && (
        <div className="space-y-3">
          <SecretField
            id="cred-primary"
            label={itemType.primary.label}
            required
            multiline={itemType.primary.multiline}
            placeholder={itemType.primary.placeholder}
            value={primaryValue}
            onChange={setPrimaryValue}
          />
          {detected && (
            <p className="flex items-center gap-1.5 text-[11px] text-success">
              <BrandIcon className="h-3 w-3" style={{ color: brandColor(brand) }} aria-hidden="true" />
              Looks like {detected.label}
              {suggestedSlot && <> — we&apos;ll suggest <span className="font-mono">{suggestedSlot}</span> as the variable name</>}
            </p>
          )}

          {itemType.usernameOnRow && (
            <div className="space-y-1.5">
              <Label htmlFor="cred-username" className="text-xs">Username</Label>
              <Input
                id="cred-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                className="font-mono text-sm"
              />
              <p className="text-[11px] text-muted-foreground">
                An identifier, not a secret — stored in the clear so the list can search it.
              </p>
            </div>
          )}

          {itemType.extra.map((f) => (
            f.secret ? (
              <SecretField
                key={f.key}
                id={`cred-extra-${f.key}`}
                label={f.label}
                required={f.required}
                multiline={f.multiline}
                placeholder={f.placeholder}
                value={extras[f.key] ?? ""}
                onChange={(v) => setExtras((prev) => ({ ...prev, [f.key]: v }))}
              />
            ) : (
              <div key={f.key} className="space-y-1.5">
                <Label htmlFor={`cred-extra-${f.key}`} className="text-xs">
                  {f.label}{f.required ? "" : " (optional)"}
                </Label>
                {f.multiline ? (
                  <Textarea
                    id={`cred-extra-${f.key}`}
                    rows={3}
                    placeholder={f.placeholder}
                    value={extras[f.key] ?? ""}
                    onChange={(e) => setExtras((prev) => ({ ...prev, [f.key]: e.target.value }))}
                    className="font-mono text-xs"
                  />
                ) : (
                  <Input
                    id={`cred-extra-${f.key}`}
                    placeholder={f.placeholder}
                    value={extras[f.key] ?? ""}
                    onChange={(e) => setExtras((prev) => ({ ...prev, [f.key]: e.target.value }))}
                    className="font-mono text-sm"
                  />
                )}
                {f.hint && <p className="text-[11px] text-muted-foreground">{f.hint}</p>}
              </div>
            )
          ))}

          {itemType.fileNote && (
            <div className="rounded-md border border-warn/30 bg-warn/[0.05] px-3 py-2 text-[11px] text-foreground/80">
              {itemType.fileNote}
            </div>
          )}

          <CustomFields fields={custom} onChange={setCustom} />

          <div className="space-y-1.5 pt-2 border-t border-white/10">
            <div className="flex items-center justify-between gap-2">
              <Label htmlFor="cred-name" className="text-xs">Name (which account)</Label>
              <BrandPicker
                value={provider}
                onChange={(key) => { providerTouched.current = true; setProvider(key) }}
              />
            </div>
            <Input
              id="cred-name"
              placeholder="e.g. github-acme"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="font-mono text-sm"
            />
            <p className="text-[11px] text-muted-foreground">
              A human name for the account. It does not have to be the variable name — that is the
              slot, in the next step.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="cred-account-label" className="text-xs">Account label (optional)</Label>
            <Input
              id="cred-account-label"
              placeholder="acme-bot"
              value={accountLabel}
              onChange={(e) => setAccountLabel(e.target.value)}
              className="text-sm"
            />
          </div>

          {/* Tags stay on the create path: they drive the rail's Tag facet, and
              a credential that can only be tagged after the fact tends never
              to be. */}
          <div className="space-y-1.5">
            <Label htmlFor="cred-tags" className="text-xs">Tags (optional)</Label>
            <div className="flex flex-wrap items-center gap-1.5 rounded-md border border-white/10 bg-background px-2 py-1.5 min-h-[34px]">
              {tags.map((t) => (
                <Badge key={t} variant="outline" className="gap-1 text-[10px] font-mono">
                  {t}
                  <button
                    type="button"
                    aria-label={`Remove tag ${t}`}
                    onClick={() => setTags(tags.filter((x) => x !== t))}
                    className="hover:text-destructive"
                  >
                    <X className="h-2.5 w-2.5" />
                  </button>
                </Badge>
              ))}
              <input
                id="cred-tags"
                list="cred-wizard-tags"
                value={tagDraft}
                onChange={(e) => setTagDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key !== "Enter" && e.key !== ",") return
                  e.preventDefault()
                  const t = tagDraft.trim().toLowerCase()
                  if (t && !tags.includes(t) && tags.length < 8) setTags([...tags, t])
                  setTagDraft("")
                }}
                placeholder={tags.length === 0 ? "prod, billing…" : ""}
                className="min-w-[80px] flex-1 bg-transparent text-xs outline-none placeholder:text-muted-foreground"
              />
              {knownTags && knownTags.length > 0 && (
                <datalist id="cred-wizard-tags">
                  {knownTags.filter((t) => !tags.includes(t)).map((t) => <option key={t} value={t} />)}
                </datalist>
              )}
            </div>
          </div>

          {missingRequired && (
            <p className="text-[11px] text-muted-foreground">
              <span className="font-medium text-foreground/80">{missingRequired}</span> is still empty.
            </p>
          )}

          <div className="flex items-center gap-2 pt-2 border-t border-white/10">
            <Button type="button" variant="outline" size="sm" onClick={() => setStep("type")}>
              <ChevronLeft className="mr-1 h-3 w-3" /> Back
            </Button>
            <Button
              type="button"
              size="sm"
              className="ml-auto"
              disabled={Boolean(missingRequired) || !name.trim()}
              onClick={() => setStep("scope")}
            >
              Continue
            </Button>
          </div>
        </div>
      )}

      {step === "scope" && (
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label className="text-xs">Who gets it</Label>
            <div className="flex flex-wrap gap-2">
              {(["WORKSPACE", "CREW"] as const).map((s) => (
                <button
                  key={s}
                  type="button"
                  aria-pressed={scope === s}
                  onClick={() => { setScope(s); if (s === "WORKSPACE") setCrewIds([]) }}
                  className={cn(
                    "rounded-full border px-3 py-1 text-[11px] transition-colors",
                    scope === s
                      ? "border-primary/50 bg-primary/10 text-primary-hover"
                      : "border-white/10 text-muted-foreground hover:text-foreground",
                  )}
                >
                  {s === "WORKSPACE" ? "The whole workspace" : "Selected crews"}
                </button>
              ))}
            </div>
          </div>

          {/* Keeper tier. On this step rather than a fourth one: "who gets it" and
              "how hard is it to get" are the same decision, and splitting them
              would put the tier behind another click nobody takes. */}
          <div className="space-y-1.5">
            <Label className="text-xs">How closely Keeper guards it</Label>
            <div className="flex flex-wrap gap-2">
              {CREDENTIAL_TIERS.map((t) => (
                <button
                  key={t.level}
                  type="button"
                  aria-pressed={securityLevel === t.level}
                  onClick={() => setSecurityLevel(t.level)}
                  className={cn(
                    "rounded-full border px-3 py-1 text-[11px] transition-colors",
                    securityLevel === t.level
                      ? t.level >= 4
                        ? "border-warn/50 bg-warn/10 text-warn"
                        : "border-primary/50 bg-primary/10 text-primary-hover"
                      : "border-white/10 text-muted-foreground hover:text-foreground",
                  )}
                >
                  {t.label}
                </button>
              ))}
            </div>
            {/* The blast radius and what the choice costs, for the tier selected.
                An operator picking "critical" is opting into a human approval on
                every read, which they should read before saving, not after. */}
            <p className="text-[10px] leading-relaxed text-muted-foreground">
              {CREDENTIAL_TIERS.find((t) => t.level === securityLevel)?.blast}
            </p>
            <p className={cn(
              "text-[10px] leading-relaxed",
              securityLevel >= 4 ? "text-warn" : "text-muted-foreground",
            )}>
              {CREDENTIAL_TIERS.find((t) => t.level === securityLevel)?.consequence}
            </p>
          </div>

          {scope === "CREW" && (
            <div className="space-y-1.5">
              <Label className="text-xs">Crews</Label>
              <Popover open={crewPopoverOpen} onOpenChange={setCrewPopoverOpen}>
                <PopoverTrigger asChild>
                  <Button variant="outline" role="combobox" className="w-full justify-between font-normal text-sm">
                    {crewIds.length === 0 ? "Select crews…" : `${crewIds.length} selected`}
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
                          const on = crewIds.includes(crew.id)
                          return (
                            <CommandItem
                              key={crew.id}
                              value={crew.name}
                              onSelect={() =>
                                setCrewIds(on ? crewIds.filter((id) => id !== crew.id) : [...crewIds, crew.id])
                              }
                            >
                              <Check className={cn("mr-2 h-4 w-4", on ? "opacity-100" : "opacity-0")} />
                              {crew.name}
                            </CommandItem>
                          )
                        })}
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>
              <p className="text-[11px] text-muted-foreground">
                Every agent in the selected crews receives it — including agents created later.
              </p>
            </div>
          )}

          {canBind ? (
            <div className="space-y-1.5">
              <Label htmlFor="cred-slot" className="text-xs">Slot — the variable the container sees</Label>
              <Input
                id="cred-slot"
                placeholder="GH_TOKEN"
                value={slot}
                onChange={(e) => { setSlotTouched(true); setSlot(e.target.value) }}
                className="font-mono text-sm"
              />
              <p className="text-[11px] text-muted-foreground">
                {suggestedSlot && !slotTouched
                  ? <>Suggested from the detected brand. Overwrite it freely — this is a hint, not a rule.</>
                  : <>Leave it empty to deliver the credential under its own name.</>}
              </p>
            </div>
          ) : (
            <p className="text-[11px] text-muted-foreground">
              Choosing the variable name (the slot) is a workspace-admin action, so this credential
              will be delivered under its own name. An admin can bind it to a slot afterwards.
            </p>
          )}

          {!canBind || !slot.trim() ? (
            !nameIsEnvVar && name.trim() ? (
              <div className="rounded-md border border-warn/30 bg-warn/[0.05] px-3 py-2 text-[11px]">
                Without a slot the container sees this credential under its own name, and{" "}
                <span className="font-mono">{name.trim()}</span> is not a valid environment-variable
                name.{" "}
                {nameSuggestion && (
                  <button
                    type="button"
                    className="font-mono underline underline-offset-2"
                    onClick={() => setName(nameSuggestion)}
                  >
                    Use {nameSuggestion}
                  </button>
                )}
              </div>
            ) : null
          ) : null}

          {error && (
            <div className="rounded-md border border-destructive/30 bg-destructive/[0.05] px-3 py-2 text-xs text-destructive">
              {error}
            </div>
          )}
          {warning && (
            <div className="rounded-md border border-warn/40 bg-warn/[0.06] px-3 py-2 text-xs">{warning}</div>
          )}

          <div className="flex items-center gap-2 pt-2 border-t border-white/10">
            <Button type="button" variant="outline" size="sm" onClick={() => setStep("values")} disabled={submitting}>
              <ChevronLeft className="mr-1 h-3 w-3" /> Back
            </Button>
            <Button type="button" size="sm" className="ml-auto" onClick={submit} disabled={submitting}>
              {submitting && <Spinner className="mr-1.5 h-3 w-3" />}
              Save secret
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

function StepBar({ step, canBind }: { step: Step; canBind: boolean }) {
  const steps: { key: Step; label: string }[] = [
    { key: "type", label: "1 · Shape" },
    { key: "values", label: "2 · Values" },
    { key: "scope", label: canBind ? "3 · Who & slot" : "3 · Who gets it" },
  ]
  const index = steps.findIndex((s) => s.key === step)
  return (
    <div className="flex flex-wrap gap-1.5 text-[11px]">
      {steps.map((s, i) => (
        <span
          key={s.key}
          className={cn(
            "rounded-full border px-2.5 py-0.5",
            i === index
              ? "border-primary/40 bg-primary/10 text-foreground"
              : i < index
                ? "border-success/30 text-success"
                : "border-white/10 text-muted-foreground",
          )}
        >
          {s.label}
        </span>
      ))}
    </div>
  )
}

/**
 * A secret input. Masked by default and it stays that way unless the user asks
 * — this is the ONE place in the product where a secret is legitimately on
 * screen (they just typed it), and it should not be the place that teaches the
 * habit of leaving values visible.
 */
function SecretField({
  id, label, value, onChange, required, multiline, placeholder,
}: {
  id: string
  label: string
  value: string
  onChange: (v: string) => void
  required?: boolean
  multiline?: boolean
  placeholder?: string
}) {
  const [reveal, setReveal] = React.useState(false)
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <Label htmlFor={id} className="text-xs">{label}{required ? "" : " (optional)"}</Label>
        <button
          type="button"
          onClick={() => setReveal((r) => !r)}
          className="text-[10px] text-muted-foreground hover:text-foreground"
        >
          {reveal ? `Hide ${label.toLowerCase()}` : `Show ${label.toLowerCase()}`}
        </button>
      </div>
      {multiline ? (
        <Textarea
          id={id}
          rows={4}
          placeholder={placeholder}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className={cn("font-mono text-xs", !reveal && "[-webkit-text-security:disc]")}
        />
      ) : (
        <Input
          id={id}
          type={reveal ? "text" : "password"}
          placeholder={placeholder}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="font-mono text-sm"
        />
      )}
    </div>
  )
}

/** The long-tail escape hatch (§2.2): any number of extra key/value parts. */
function CustomFields({
  fields, onChange,
}: {
  fields: CustomFieldDraft[]
  onChange: (next: CustomFieldDraft[]) => void
}) {
  return (
    <div className="space-y-2">
      {fields.map((f, i) => (
        <div key={i} className="flex items-end gap-2">
          <div className="flex-1 space-y-1">
            <Label className="text-[10px] text-muted-foreground">Field key</Label>
            <Input
              value={f.key}
              placeholder="tenant_id"
              aria-label={`Custom field ${i + 1} key`}
              onChange={(e) =>
                onChange(fields.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))
              }
              className="font-mono text-xs"
            />
          </div>
          <div className="flex-1 space-y-1">
            <Label className="text-[10px] text-muted-foreground">Value</Label>
            <Input
              type={f.secret ? "password" : "text"}
              value={f.value}
              aria-label={`Custom field ${i + 1} value`}
              onChange={(e) =>
                onChange(fields.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))
              }
              className="font-mono text-xs"
            />
          </div>
          <Badge
            variant="outline"
            role="button"
            tabIndex={0}
            aria-label={`Custom field ${i + 1} is ${f.secret ? "secret" : "plain text"}`}
            onClick={() => onChange(fields.map((x, j) => (j === i ? { ...x, secret: !x.secret } : x)))}
            className="mb-1.5 cursor-pointer text-[10px]"
          >
            {f.secret ? "secret" : "text"}
          </Badge>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label={`Remove custom field ${i + 1}`}
            className="mb-1.5"
            onClick={() => onChange(fields.filter((_, j) => j !== i))}
          >
            <X className="h-3 w-3" />
          </Button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...fields, { key: "", value: "", secret: true }])}
        className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"
      >
        <Plus className="h-3 w-3" /> Add a field
      </button>
    </div>
  )
}
