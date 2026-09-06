"use client"

/**
 * Add-credential flow — PRD-CREDENTIALS-V2-2026 §0 (KISS scope), wireframe
 * screens 2–4.
 *
 * Three steps, in the order the user actually decides things:
 *
 *   1. WHAT SHAPE is it — Token / Login / Key pair / SSH key / File /
 *      Certificate — and, optionally, WHICH BRAND it wears. The shape decides
 *      the fields; the brand does not. The earlier draft of this flow started
 *      from a ~150-entry brand catalog and that was rejected: six shapes plus
 *      custom fields cover the same ground without a table that is wrong for
 *      somebody.
 *   2. THE VALUES the shape implies, plus a human name for the account.
 *   3. WHO GETS IT and under WHICH ENV VAR — scope, tier, then slot.
 *
 * The brand is detected from the pasted value (`ghp_` → GitHub) or from the
 * name, and contributes exactly two things: an icon, and a suggested slot
 * name. Both are hints. Nothing here refuses to continue because a brand was
 * not recognised — §0 item 5: "the user knows the env var name better than we
 * do; a wrong row in a catalog is worse than no row". The picker sits on step
 * 1 because that is where a user thinks about what the secret IS ("it's a
 * GitHub token"), and it stays on step 2 beside the name because the icon is
 * part of the credential's identity — one piece of state, two places it is
 * natural to reach for it.
 *
 * Why the name and the slot are separate boxes (§2.5b): `credentials.name` is
 * WHICH ACCOUNT ("github-acme"), the slot is WHAT THE CONTAINER SEES
 * ("GH_TOKEN"). Fusing them is what made ten GitHub accounts in one workspace
 * impossible — UNIQUE(workspace_id, name) meant the second one would have had
 * to be called GH_TOKEN too.
 *
 * LAYOUT. The component owns a three-band column — step bar, scrolling body,
 * docked footer — rather than one long scroll: a footer inside the scrollport
 * means "Save secret" is somewhere below a PEM key. The bands are still
 * chosen here, because only this file knows which control belongs in which
 * one, but they are now the SHELL's bands (CreateSurfaceSteps /
 * CreateSurfaceBody / CreateSurfaceFooter, and CreateSurfaceRefusal for what
 * the server said when it said no) rather than three hand-rolled divs. The
 * container above them is CreateSurface — see add-secret-sheet.tsx for what
 * that changed. Inside the body everything visual still comes from the detail
 * kit (components/ui/detail): the shell owns the room, not the furniture.
 *
 * Two things travel back up to the shell, because the shell owns the gestures
 * and this file owns the state they act on: `dirty` (so Esc, the overlay and
 * the × ask before throwing a half-typed secret away) and the primary action
 * (so ⌘↵ does whatever the footer's primary does on the step you are on).
 */

import * as React from "react"
import {
  Braces, Check, ChevronLeft, ChevronsUpDown, CreditCard, FileText, KeyRound,
  Palette, Plus, ShieldCheck, Tag, Terminal, User, Users, X,
} from "lucide-react"

import { useSessionSafe } from "@/hooks/use-auth"
import { DeviceSignIn } from "./device-sign-in"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSection,
  CreateSurfaceSteps,
} from "@/components/layout/create-surface"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"
import { defaultEnvVarName } from "@/lib/credential-provider"
import { loginProvider } from "@/lib/credentials/login-providers"
import { providerConnectionGuide } from "@/lib/credentials/provider-connection-guides"
import { LoginProviderPicker } from "./login-provider-picker"
import {
  brandColor, detectBrandFromName, detectBrandFromValue, getBrand,
} from "@/lib/credential-providers/registry"
import {
  CREDENTIAL_ITEM_TYPES, extraFieldsFor, getItemType,
  type CustomFieldDraft, type ItemTypeKey,
} from "@/lib/credentials/item-types"
import { isValidEnvVarName, suggestEnvVarName } from "@/lib/env-var-name"
import {
  providerLoginCredentialType, providerLoginPresentation, type ProviderLoginMode,
} from "@/lib/credentials/item-types"
import { cn } from "@/lib/utils"
import { ACCENT, type Accent } from "@/lib/concept-accents"
import { BrandPicker } from "./brand-picker"
import { CREDENTIAL_TIERS } from "./credential-form"

/**
 * A colour per shape.
 *
 * Not decoration and not arbitrary: the hue follows what the secret IS, so the
 * same idea wears the same colour wherever it appears. Amber for the bearer
 * secrets that are simply a string, purple for an identity, gold for a
 * key-and-id pair, teal for a private key, blue for a file, green for the one
 * that exists to prove trust. Every value is a token globals.css already
 * declares — see lib/concept-accents.ts for why none of them is a new one.
 */
const SHAPE_ACCENT: Partial<Record<ItemTypeKey, Accent>> = {
  PROVIDER_LOGIN: ACCENT.sky,
  TOKEN: ACCENT.amber,
  LOGIN: ACCENT.purple,
  KEYPAIR: ACCENT.gold,
  SSH_KEY: ACCENT.teal,
  FILE: ACCENT.blue,
  CERTIFICATE: ACCENT.green,
}

const TYPE_ICON: Record<ItemTypeKey, React.ComponentType<{ className?: string }>> = {
  PROVIDER_LOGIN: CreditCard,
  TOKEN: KeyRound,
  LOGIN: User,
  KEYPAIR: Terminal,
  SSH_KEY: KeyRound,
  FILE: FileText,
  CERTIFICATE: ShieldCheck,
}

/**
 * Field sizing for this dialog.
 *
 * The height is the thumb target — 36px is the ui-kit's pointer default and is
 * under it. The type size is not a taste call either: iOS Safari zooms the
 * whole page whenever a focused field's font-size is below 16px, and every box
 * in here used to be 12–14px, so the first tap on a phone zoomed the dialog and
 * left the user pinching back out. The ui kit's own `text-base md:text-sm`
 * already does the right thing — the rule is not to override it below sm.
 */
const FIELD = "h-10 sm:h-9"
/** …and a field that opts down to 12px mono has to opt back up below sm. */
const MONO_AREA = "font-mono text-xs max-sm:text-base"

interface Crew { id: string; name: string }

/** GET /workspaces/{id}/members — the people a seat can belong to. */
interface Member { id: string; user: { id: string; email: string; full_name: string | null } }

/** How a subscription login gets in: pasted from the CLI's own file, or minted here with a code. */
export type SignInMethod = "paste" | "device"

/**
 * Where the wizard starts. The Providers tab opens it on the Provider login
 * shape; Re-login opens it on the sign-in step with the seat's provider and
 * mode already chosen, so the person who owns the seat types nothing they
 * have typed before.
 */
export interface WizardInitial {
  itemType?: ItemTypeKey
  provider?: string
  loginMode?: ProviderLoginMode
  signIn?: SignInMethod
  step?: Step
  name?: string
}

export interface AddCredentialWizardProps {
  workspaceId: string
  onSuccess: () => void
  onCancel: () => void
  knownTags?: string[]
  initial?: WizardInitial
  /** Overrides the device-code poll period; tests pass milliseconds. */
  devicePollMs?: number
  /**
   * Reports whether there is unsaved input, for the shell's discard guard.
   * Optional so the wizard still renders standalone (and in its own tests)
   * without a CreateSurface around it.
   */
  onDirtyChange?: (dirty: boolean) => void
  /**
   * Where the shell picks up ⌘↵. The primary action depends on the step, and
   * the step lives here.
   */
  primaryRef?: React.MutableRefObject<(() => void) | null>
}

type Step = "type" | "values" | "scope"

/** The three steps, in order, as the shell's step bar wants them. */
const STEPS: { id: Step; label: string }[] = [
  { id: "type", label: "Shape" },
  { id: "values", label: "Values" },
  { id: "scope", label: "Delivery" },
]

const STEP_ORDER: Step[] = STEPS.map((s) => s.id)

/**
 * The line a DetailCard used to render in its footer.
 *
 * `CreateSurfaceSection` has no footer slot and its `hint` is hidden below the
 * `sm` breakpoint, which is right for "— optional" and wrong for a sentence
 * that stops a mistake. These stay visible everywhere.
 */
function CardNote({ children }: { children: React.ReactNode }) {
  return (
    <p className="type-meta leading-relaxed text-muted-foreground-soft">{children}</p>
  )
}

export function AddCredentialWizard({
  workspaceId, onSuccess, onCancel, knownTags, initial, devicePollMs, onDirtyChange, primaryRef,
}: AddCredentialWizardProps) {
  const { abilities } = useAbilities()
  // POST /credentials/bindings is roleManage — OWNER/ADMIN — and the handler
  // repeats the check. A MANAGER may create the credential but not claim a
  // slot for it, so the step is hidden from them rather than offered and 403'd.
  const canBind = abilities.can("manage", "Credential")
  const session = useSessionSafe()

  const [step, setStep] = React.useState<Step>(initial?.step ?? "type")
  // Keeper tier. Defaults to L1 — the column's default — so a wizard run that
  // ignores this control behaves exactly as it did before the control existed.
  const [securityLevel, setSecurityLevel] = React.useState(1)
  const [itemTypeKey, setItemTypeKey] = React.useState<ItemTypeKey>(initial?.itemType ?? "TOKEN")
  // Provider login only: a flat-rate seat or a metered key (#2428). Decides
  // the server type at save time and what the value box asks for.
  const [loginMode, setLoginMode] = React.useState<ProviderLoginMode>(initial?.loginMode ?? "subscription")
  // Provider login, subscription, a provider with a device flow: paste the
  // CLI's file, or sign in with a code and let the server mint the login.
  const [signIn, setSignIn] = React.useState<SignInMethod>(initial?.signIn ?? "paste")
  // The credential a device sign-in created. Once set, the save step has no
  // row to create — only a name to give it and a slot to claim.
  const [deviceCredentialId, setDeviceCredentialId] = React.useState<string | null>(null)
  const [deviceBusy, setDeviceBusy] = React.useState(false)
  // Whose seat this is (§3.4: seats are per person). Defaults to the person
  // signed in; the list of others arrives with the members request.
  const [ownerId, setOwnerId] = React.useState<string>("")
  const [members, setMembers] = React.useState<Member[]>([])
  const [primaryValue, setPrimaryValue] = React.useState("")
  const [extras, setExtras] = React.useState<Record<string, string>>({})
  const [custom, setCustom] = React.useState<CustomFieldDraft[]>([])
  const [name, setName] = React.useState(initial?.name ?? "")
  const nameTouched = React.useRef(Boolean(initial?.name))
  const [username, setUsername] = React.useState("")
  const [accountLabel, setAccountLabel] = React.useState("")
  const [tags, setTags] = React.useState<string[]>([])
  const [tagDraft, setTagDraft] = React.useState("")
  const [provider, setProvider] = React.useState(initial?.provider ?? "NONE")
  const providerTouched = React.useRef(Boolean(initial?.provider))
  const [scope, setScope] = React.useState<"WORKSPACE" | "CREW">("WORKSPACE")
  const [crewIds, setCrewIds] = React.useState<string[]>([])
  const [crews, setCrews] = React.useState<Crew[]>([])
  const [crewPopoverOpen, setCrewPopoverOpen] = React.useState(false)
  const [slot, setSlot] = React.useState("")
  const [slotTouched, setSlotTouched] = React.useState(false)
  // YYYY-MM-DD from the date input, or "" — same shape CredentialForm (the
  // edit-only surface) already sends as `token_expires_at` on PATCH. Empty
  // means "no expiry", not "unchanged" — there is nothing to preserve on a
  // brand-new credential.
  const [expiresAt, setExpiresAt] = React.useState("")
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [warning, setWarning] = React.useState<string | null>(null)

  const itemType = getItemType(itemTypeKey)
  const ItemIcon = TYPE_ICON[itemTypeKey]

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
  // A provider login knows its own slot from the brand and the mode; detection
  // is the fallback for every other shape.
  const login = itemTypeKey === "PROVIDER_LOGIN" ? providerLoginPresentation(provider, loginMode) : null
  const connectionGuide = providerConnectionGuide(provider)
  const suggestedSlot = login?.slot ?? (detected ? defaultEnvVarName(detected) : null)
  // Which providers can sign in with a code — the server runs the device flow
  // (PRD §5.6 v2, §10.3). Anthropic has no device flow: `claude setup-token`
  // is the only way in, so it keeps the paste. A key is always pasted.
  const deviceFlowAvailable = Boolean(login) && loginMode === "subscription" && provider.toUpperCase() === "OPENAI"
  const usingDevice = deviceFlowAvailable && signIn === "device"

  // The owner list, only once the shape asks for one. A failure leaves the
  // signed-in person as the one choice, which is also the default.
  const sessionUserId = session.data?.user.id ?? ""
  const sessionUserEmail = session.data?.user.email ?? ""
  React.useEffect(() => {
    if (!ownerId && sessionUserId) setOwnerId(sessionUserId)
  }, [ownerId, sessionUserId])
  const membersFetchedFor = React.useRef<string | null>(null)
  React.useEffect(() => {
    if (!login || membersFetchedFor.current === workspaceId) return
    membersFetchedFor.current = workspaceId
    apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: Member[]) => setMembers(Array.isArray(data) ? data.filter((m) => m?.user?.id) : []))
      .catch(() => setMembers([]))
  }, [login, workspaceId])

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

  const selectLoginProvider = (key: string) => {
    if (key === provider) return
    if (key !== provider && deviceCredentialId) return
    providerTouched.current = true
    setProvider(key)
    setItemTypeKey("PROVIDER_LOGIN")
    if (!nameTouched.current) setName(loginProvider(key)?.label ?? key)
    setLoginMode(loginProvider(key)?.subscription && key !== "GOOGLE" ? "subscription" : "api_key")
    setPrimaryValue("")
    setExtras({})
    setDeviceBusy(false)
    setSignIn(key === "OPENAI" ? "device" : "paste")
  }

  // Without a binding the credential is delivered under its own NAME (the
  // pre-P3 behaviour the backend still honours), so the name has to be a legal
  // env var in that case — and is free-form when a slot is set. We warn rather
  // than block: the server does not reject either shape, and a hard stop here
  // would be the UI inventing a rule.
  const nameIsEnvVar = isValidEnvVarName(name.trim())
  const nameSuggestion = nameIsEnvVar ? null : suggestEnvVarName(name.trim())

  const missingRequired = React.useMemo(() => {
    // A provider login without a provider cannot be delivered anywhere — the
    // server routes and renders it by the provider column — and a brand with
    // no subscription login is a save that would only fail at run time.
    if (login && !login.supported) return "Provider"
    // With a code the server holds the value; the step waits for the sign-in.
    if (usingDevice) return deviceCredentialId ? null : "Sign-in"
    if (!primaryValue.trim()) return login?.label ?? itemType.primary.label
    if (itemType.usernameOnRow && !username.trim()) return "Username"
    for (const f of itemType.extra) {
      if (f.required && !(extras[f.key] ?? "").trim()) return f.label
    }
    return null
  }, [itemType, login, usingDevice, deviceCredentialId, primaryValue, username, extras])

  // What is holding step 2 back, in the order the boxes are on screen. The
  // Continue button being dead is not an explanation; naming the box is.
  const blocker = missingRequired ?? (name.trim() ? null : "Name")
  const stepIndex = STEP_ORDER.indexOf(step)

  /**
   * Is there anything to lose?
   *
   * Typed input only. The SHAPE is deliberately not in here: TOKEN is
   * preselected, and picking "Certificate" without filling anything in is a
   * choice with no data behind it — prompting for it would teach people to
   * click through the guard, which is how a guard stops working.
   */
  const dirty = Boolean(
    primaryValue ||
      username ||
      accountLabel ||
      name ||
      expiresAt ||
      tagDraft ||
      tags.length > 0 ||
      slotTouched ||
      providerTouched.current ||
      // A login the code minted exists on the server already; walking away
      // leaves it unnamed and unbound, which is worth one question.
      deviceCredentialId ||
      Object.values(extras).some((v) => v.trim()) ||
      custom.some((f) => f.key.trim() || f.value.trim()),
  )
  React.useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])

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
      let credentialId: string | undefined
      // Everything past the create is a follow-up write on a credential that
      // ALREADY EXISTS. A failure there is reported as a warning, never as
      // "save failed" — telling the user nothing was saved when a secret is
      // now in the vault is the worse of the two lies.
      const problems: string[] = []

      if (deviceCredentialId) {
        // The sign-in created the row (§10.3: the device status names it).
        // What is left is the name the person typed; the value never came
        // through this browser.
        credentialId = deviceCredentialId
        try {
          const pr = await apiFetch(
            `/api/v1/credentials/${encodeURIComponent(credentialId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
            {
              method: "PATCH",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ name: name.trim(), tags }),
            },
          )
          if (!pr.ok) problems.push(`name (HTTP ${pr.status})`)
        } catch {
          problems.push("name")
        }
      } else {
        const body: Record<string, unknown> = {
          name: name.trim(),
          value: primaryValue,
          type: login ? providerLoginCredentialType(loginMode) : itemType.credentialType,
          provider,
          scope,
          tags,
        }
        if (login) {
          // Contract §10.2: PROVIDER_LOGIN with the mode as its own field and
          // the value exactly as pasted — the server splits it into parts and
          // seals the refresh token. The owner is §5.1's `owner_user_id`.
          body.mode = loginMode
          if (ownerId) body.owner_user_id = ownerId
        }
        // Provider accounts use server policy, like device-code-created accounts.
        // Ordinary secrets retain the explicitly selected Keeper tier.
        if (!login) body.security_level = securityLevel
        if (itemType.usernameOnRow && username.trim()) body.username = username.trim()
        if (accountLabel.trim()) body.account_label = accountLabel.trim()
        // Only when set — an absent key leaves the column NULL, which is what a
        // brand-new row with no expiry should be. `internal/api/credentials_mutate.go`
        // writes this straight into `credentials.token_expires_at` (createCredentialRequest.TokenExpires,
        // json tag "token_expires_at"), same column and same ISO-string shape
        // EditCredentialDialog already sends on PATCH.
        if (expiresAt) body.token_expires_at = new Date(expiresAt).toISOString()
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
        credentialId = created?.id
      }

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

  /**
   * What the footer's primary does, which is also what ⌘↵ does. Guarded here
   * rather than in the shell — CreateSurface fires `onSubmit` unconditionally
   * and expects the surface to know when it is not submittable.
   */
  function primaryAction() {
    if (submitting) return
    if (step === "type" && login && !loginProvider(provider)) return
    if (step === "scope") {
      void submit()
      return
    }
    if (step === "values" && (blocker || deviceBusy)) return
    setStep(step === "type" ? "values" : "scope")
  }

  // No dependency array: the action closes over every field, so the shell has
  // to be handed the current one after each commit rather than a stale one.
  React.useEffect(() => {
    if (!primaryRef) return
    primaryRef.current = primaryAction
    return () => {
      primaryRef.current = null
    }
  })

  return (
    <>
      {/* The landmark is the component's own. This surface wrapped it in a
          second <nav> back when CreateSurfaceSteps rendered a bare div; it
          renders the nav itself now, so the wrapper was a nested landmark. */}
      <CreateSurfaceSteps
        ariaLabel="Add credential steps"
        steps={login ? [{ id: "type", label: "Provider" }, { id: "values", label: "Connect" }, { id: "scope", label: "Access" }] : STEPS}
        current={stepIndex}
        onJump={(i) => setStep(STEP_ORDER[i])}
      />

      {/* The only scrollport. Everything that has to stay reachable — the step
          bar above, the actions below — lives outside it. */}
      <CreateSurfaceBody data-testid="wizard-body" className="space-y-3">
        {step === "type" && (
          <>
            {login ? (
              <>
                <CreateSurfaceSection title="Connect an AI provider" icon={KeyRound} accent="blue">
                  <p className="type-meta text-muted-foreground">Choose your provider. We will guide you through its supported sign-in methods.</p>
                </CreateSurfaceSection>
                <LoginProviderPicker value={provider} locked={Boolean(deviceCredentialId)} onChange={(key) => {
                  selectLoginProvider(key)
                  setStep("values")
                }} />
              </>
            ) : <>
            <CreateSurfaceSection title="What shape is it?" icon={KeyRound} accent="amber">
              <p className="type-meta leading-relaxed text-muted-foreground">
                The shape decides which boxes you fill next. Every brand fits one of these.
              </p>
            </CreateSurfaceSection>
            {/* Two-up on a phone: six tiles in one column is four thumb-swipes
                to reach Certificate, and three-up leaves 110px of tile for a
                label plus a blurb. */}
            <div data-testid="shape-grid" className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {CREDENTIAL_ITEM_TYPES.filter((t) => t.key !== "PROVIDER_LOGIN").map((t) => {
                const Icon = TYPE_ICON[t.key]
                const selected = t.key === itemTypeKey
                const tone = SHAPE_ACCENT[t.key] ?? ACCENT.slate
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
                      "flex min-h-20 flex-col items-start gap-1 rounded-xl border p-3 text-left transition-colors",
                      selected
                        ? "border-primary/60 bg-primary/10"
                        : "border-border/60 bg-card hover:border-border hover:bg-surface-raised",
                    )}
                  >
                    {/* The glyph carries the shape's own colour whether or not
                        the tile is selected. Six identical grey squares are
                        read by their captions every time; six colours are told
                        apart before the caption is read, which is the whole
                        job of a pick-one grid. Selection stays the blue
                        border and fill, so "which is chosen" and "which is
                        which" never compete for the same channel. */}
                    <span
                      className={cn(
                        "mb-0.5 flex h-7 w-7 items-center justify-center rounded-lg border",
                        tone.chip,
                      )}
                    >
                      <Icon className={cn("h-4 w-4", tone.fg)} />
                    </span>
                    <span className="type-row font-medium leading-tight text-foreground">{t.label}</span>
                    <span className="type-meta leading-snug text-muted-foreground">{t.blurb}</span>
                  </button>
                )
              })}
            </div>
            </>}

            {/* "I just want to give an icon, which brand it is, and that's it."
                It is offered here, next to the shape, and it gates nothing —
                the icon is what the rail and the list draw, not a category the
                flow makes you choose. */}
            {itemTypeKey !== "PROVIDER_LOGIN" && <CreateSurfaceSection title="Brand icon" hint="optional" icon={Palette} accent="purple">
              <div className="flex flex-wrap items-center gap-3">
                <BrandPicker
                  value={provider}
                  onChange={(key) => { providerTouched.current = true; setProvider(key) }}
                />
                <span className="type-meta min-w-0 flex-1 text-muted-foreground">
                  The face this credential wears everywhere it is listed.
                </span>
              </div>
              <CardNote>
                Pasting the secret on the next step usually recognises the brand on its own. Setting it
                here just wins the tie.
              </CardNote>
            </CreateSurfaceSection>}
          </>
        )}

        {step === "values" && (
          <>
            <CreateSurfaceSection title={login ? `Connect ${loginProvider(provider)?.label ?? "your provider"}` : "The secret"} hint={login ? undefined : itemType.label.toLowerCase()} icon={login ? BrandIcon : ItemIcon} accent="amber">
              <div className="space-y-3">
                {login && (
                  <div className="space-y-3">
                    {/* Provider identity is fixed by step 1; only supported
                        connection methods belong on the Connect step. */}
                    {loginProvider(provider)?.subscription && <div role="group" aria-label="How does this seat pay" className="grid grid-cols-2 gap-2">
                      {(["subscription", "api_key"] as const).filter((m) => m === "api_key" || loginProvider(provider)?.subscription).map((m) => (
                        <button
                          key={m}
                          type="button"
                          aria-pressed={loginMode === m}
                          disabled={deviceBusy || Boolean(deviceCredentialId)}
                          onClick={() => { if (m === loginMode) return; setLoginMode(m); setPrimaryValue(""); setSignIn(provider === "OPENAI" && m === "subscription" ? "device" : "paste") }}
                          className={cn(
                            "flex min-h-10 flex-col items-start rounded-lg border px-3 py-2 text-left transition-colors",
                            loginMode === m
                              ? "border-primary/60 bg-primary/10"
                              : "border-border/60 bg-card hover:border-border hover:bg-surface-raised",
                          )}
                        >
                          <span className="type-row font-medium text-foreground">
                            {m === "subscription" ? (provider === "GOOGLE" ? "Google account" : "Subscription") : "API key"}
                          </span>
                          <span className="type-meta text-muted-foreground">
                            {m === "subscription" ? "Use your existing provider account" : "Use a key from your provider account"}
                          </span>
                        </button>
                      ))}
                    </div>}
                    {deviceFlowAvailable && (
                      <div className="space-y-2">
                        <Label className="type-section text-muted-foreground">Sign in</Label>
                        <div role="group" aria-label="Sign in" className="grid grid-cols-2 gap-2">
                          {(
                            [
                              { key: "device", title: "Sign in with a code" },
                              { key: "paste", title: "Import from Codex CLI" },
                            ] as const
                          ).map((m) => (
                            <button
                              key={m.key}
                              type="button"
                              aria-pressed={signIn === m.key}
                              disabled={Boolean(deviceCredentialId)}
                              onClick={() => { if (m.key === signIn) return; setDeviceBusy(false); setSignIn(m.key) }}
                              className={cn(
                                "flex min-h-10 flex-col items-start rounded-lg border px-3 py-2 text-left transition-colors",
                                signIn === m.key
                                  ? "border-primary/60 bg-primary/10"
                                  : "border-border/60 bg-card hover:border-border hover:bg-surface-raised",
                              )}
                            >
                              <span className="type-row font-medium text-foreground">{m.title}</span>
                            </button>
                          ))}
                        </div>
                      </div>
                    )}
                    {loginMode === "api_key" && connectionGuide ? <div className="space-y-2">
                      <p className="type-meta text-muted-foreground">{connectionGuide.instruction}</p>
                      <a href={connectionGuide.url} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-10 items-center text-sm font-medium text-primary hover:underline">Get API key ↗</a>
                      {connectionGuide.note && <p className="type-meta text-muted-foreground">{connectionGuide.note}</p>}
                    </div> : login.hint && !usingDevice && <CardNote>{login.hint}</CardNote>}
                  </div>
                )}
                {usingDevice ? deviceCredentialId ? (
                  <CardNote>Signed in. Continue to choose access for this account.</CardNote>
                ) : (
                  <>
                    <DeviceSignIn
                      key={`${provider}:${loginMode}`}
                      workspaceId={workspaceId}
                      provider={provider}
                      mode={loginMode}
                      pollIntervalMs={devicePollMs}
                      onComplete={(id) => { setDeviceBusy(false); setDeviceCredentialId(id) }}
                      onStateChange={(s) => setDeviceBusy(s === "starting" || s === "pending")}
                    />
                    <CardNote>Device-code login must be enabled in your ChatGPT security settings. You can also import an existing Codex login.</CardNote>
                  </>
                ) : (
                  <SecretField
                    id="cred-primary"
                    label={login?.label ?? itemType.primary.label}
                    required
                    multiline={login?.multiline ?? itemType.primary.multiline}
                    placeholder={login?.placeholder ?? itemType.primary.placeholder}
                    value={primaryValue}
                    onChange={setPrimaryValue}
                  />
                )}
                {login && (
                  <details className="space-y-1.5">
                    <summary className="cursor-pointer py-2 type-meta text-muted-foreground">Account owner · {members.find((m) => m.user.id === ownerId)?.user.email || sessionUserEmail || "you"}</summary>
                    <Label htmlFor="cred-owner" className="type-section text-muted-foreground">Owner</Label>
                    {members.length > 0 ? (
                      <select
                        id="cred-owner"
                        value={ownerId}
                        onChange={(e) => setOwnerId(e.target.value)}
                        className={cn(
                          FIELD,
                          "w-full rounded-md border border-border/60 bg-background px-2.5 text-sm text-foreground outline-none focus:border-primary",
                        )}
                      >
                        {!members.some((m) => m.user.id === ownerId) && ownerId && (
                          <option value={ownerId}>{sessionUserEmail || ownerId}</option>
                        )}
                        {members.map((m) => (
                          <option key={m.user.id} value={m.user.id}>
                            {m.user.email}{m.user.id === sessionUserId ? " (you)" : ""}
                          </option>
                        ))}
                      </select>
                    ) : (
                      <Input
                        id="cred-owner"
                        readOnly
                        value={sessionUserEmail || (ownerId ? ownerId : "you")}
                        className={cn(FIELD, "text-muted-foreground")}
                      />
                    )}
                    <p className="type-meta text-muted-foreground">
                      The person responsible for this provider account.
                    </p>
                  </details>
                )}
                {detected && !login && (
                  <p className="flex items-start gap-1.5 type-meta text-success">
                    <BrandIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" style={{ color: brandColor(brand) }} aria-hidden="true" />
                    <span className="min-w-0 break-words">
                      Looks like {detected.label}
                      {suggestedSlot && <> — we&apos;ll suggest <span className="font-mono">{suggestedSlot}</span> as the variable name</>}
                    </span>
                  </p>
                )}

                {itemType.usernameOnRow && (
                  <div className="space-y-1.5">
                    <Label htmlFor="cred-username" className="type-section text-muted-foreground">Username</Label>
                    <Input
                      id="cred-username"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      className={cn(FIELD, "font-mono")}
                    />
                    <p className="type-meta text-muted-foreground">
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
                      <Label htmlFor={`cred-extra-${f.key}`} className="type-section text-muted-foreground">
                        {f.label}{f.required ? "" : " (optional)"}
                      </Label>
                      {f.multiline ? (
                        <Textarea
                          id={`cred-extra-${f.key}`}
                          rows={3}
                          placeholder={f.placeholder}
                          value={extras[f.key] ?? ""}
                          onChange={(e) => setExtras((prev) => ({ ...prev, [f.key]: e.target.value }))}
                          className={MONO_AREA}
                        />
                      ) : (
                        <Input
                          id={`cred-extra-${f.key}`}
                          placeholder={f.placeholder}
                          value={extras[f.key] ?? ""}
                          onChange={(e) => setExtras((prev) => ({ ...prev, [f.key]: e.target.value }))}
                          className={cn(FIELD, "font-mono")}
                        />
                      )}
                      {f.hint && <p className="type-meta text-muted-foreground">{f.hint}</p>}
                    </div>
                  )
                ))}

                {itemType.fileNote && (
                  <div className="rounded-md border border-warn/30 bg-warn/[0.05] px-3 py-2 type-meta leading-relaxed text-foreground/80">
                    {itemType.fileNote}
                  </div>
                )}
              </div>
            </CreateSurfaceSection>

            <details open={login ? undefined : true}>
              <summary className={login ? "cursor-pointer py-2 text-sm text-muted-foreground" : "hidden"}>Name and labels · {name || "your account"}</summary>
            <CreateSurfaceSection title="Identity" icon={Tag} accent="blue">
              <div className="space-y-3">
                <div className="space-y-1.5">
                  {/* Wraps: "NAME (WHICH ACCOUNT)" is ~170px of wide-tracked
                      uppercase and the picker is ~130px, which is more than a
                      phone's 326px card body once the gap is paid. */}
                  <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1.5">
                    <Label htmlFor="cred-name" className="type-section text-muted-foreground">
                      Name (which account)
                    </Label>
                    {!login && <span className="flex items-center gap-1.5">
                      <span className="type-meta text-muted-foreground-soft">Icon</span>
                      <BrandPicker
                        value={provider}
                        onChange={(key) => { providerTouched.current = true; setProvider(key) }}
                      />
                    </span>}
                  </div>
                  <Input
                    id="cred-name"
                    placeholder={login ? `e.g. ${loginProvider(provider)?.label ?? "Provider"} · my account` : "e.g. github-acme"}
                    value={name}
                    onChange={(e) => { nameTouched.current = true; setName(e.target.value) }}
                    className={cn(FIELD, "font-mono")}
                  />
                </div>

                <CreateSurfaceField label="Account label" hint="optional" htmlFor="cred-account-label">
                  <Input
                    id="cred-account-label"
                    placeholder="acme-bot"
                    value={accountLabel}
                    onChange={(e) => setAccountLabel(e.target.value)}
                    className={FIELD}
                  />
                </CreateSurfaceField>

                {/* Tags stay on the create path: they drive the rail's Tag facet,
                    and a credential that can only be tagged after the fact tends
                    never to be. */}
                <CreateSurfaceField label="Tags" hint="optional" htmlFor="cred-tags">
                  <div className="flex min-h-10 flex-wrap items-center gap-1.5 rounded-md border border-border/60 bg-background px-2 py-1.5 sm:min-h-9">
                    {tags.map((t) => (
                      <Badge key={t} variant="outline" className="gap-1 type-meta font-mono">
                        {t}
                        <button
                          type="button"
                          aria-label={`Remove tag ${t}`}
                          onClick={() => setTags(tags.filter((x) => x !== t))}
                          className="hover:text-destructive"
                        >
                          <X className="h-3 w-3" />
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
                      className="min-w-[80px] flex-1 bg-transparent type-meta outline-none max-sm:text-base placeholder:text-muted-foreground"
                    />
                    {knownTags && knownTags.length > 0 && (
                      <datalist id="cred-wizard-tags">
                        {knownTags.filter((t) => !tags.includes(t)).map((t) => <option key={t} value={t} />)}
                      </datalist>
                    )}
                  </div>
                </CreateSurfaceField>
              </div>
              <CardNote>
                {login
                  ? "How it shows in the Providers list and on agents. It does not have to be the variable name — that is the slot, on the next step."
                  : "The name is a human label for the account. It does not have to be the variable name — that is the slot, on the next step."}
              </CardNote>
            </CreateSurfaceSection>

            </details>
            {!login && <CreateSurfaceSection title="Extra fields" hint="optional" icon={Plus} accent="slate">
              <CustomFields fields={custom} onChange={setCustom} />
              <CardNote>
                Anything else that travels with this credential — a tenant id, an endpoint. Each part is
                stored separately and can be secret or plain.
              </CardNote>
            </CreateSurfaceSection>}
          </>
        )}

        {step === "scope" && (
          <>
            <CreateSurfaceSection title="Who gets it" icon={Users} accent="teal">
              <div className="space-y-3">
                {/* Grouped rather than labelled: the card header already says
                    "Who gets it", and a second sr-only <label> pointing at
                    nothing is noise in the a11y tree, not help. */}
                <div role="group" aria-label="Who gets it" className="grid grid-cols-2 gap-2">
                  {(["WORKSPACE", "CREW"] as const).map((s) => (
                    <ChoiceButton
                      key={s}
                      selected={scope === s}
                      onClick={() => { setScope(s); if (s === "WORKSPACE") setCrewIds([]) }}
                    >
                      {s === "WORKSPACE" ? "The whole workspace" : "Selected crews"}
                    </ChoiceButton>
                  ))}
                </div>

                {scope === "CREW" && (
                  <CreateSurfaceField label="Crews">
                    <Popover open={crewPopoverOpen} onOpenChange={setCrewPopoverOpen}>
                      <PopoverTrigger asChild>
                        <Button variant="outline" role="combobox" className="h-10 w-full justify-between font-normal text-sm sm:h-9">
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
                  </CreateSurfaceField>
                )}
              </div>
              <CardNote>
                {scope === "CREW"
                  ? "Every agent in the selected crews receives it — including agents created later."
                  : "Every agent in the workspace receives it — including agents created later."}
              </CardNote>
            </CreateSurfaceSection>

            {/* Keeper tier. On this step rather than a fourth one: "who gets it" and
                "how hard is it to get" are the same decision, and splitting them
                would put the tier behind another click nobody takes. */}
            {login && <CreateSurfaceSection title="Account protection" icon={ShieldCheck} accent="green">
              <p className="type-meta text-muted-foreground">
                Your login is encrypted. Access follows the workspace or crews you select above.
              </p>
            </CreateSurfaceSection>}
            {!login && <CreateSurfaceSection
              title="Keeper tier"
              icon={ShieldCheck}
              accent={securityLevel >= 4 ? "amber" : "green"}
            >
              <div className="space-y-2.5">
                <div
                  role="group"
                  aria-label="How closely Keeper guards it"
                  className="grid grid-cols-2 gap-2 sm:grid-cols-4"
                >
                  {CREDENTIAL_TIERS.map((t) => (
                    <ChoiceButton
                      key={t.level}
                      selected={securityLevel === t.level}
                      tone={t.level >= 4 ? "warn" : "primary"}
                      onClick={() => setSecurityLevel(t.level)}
                    >
                      {t.label}
                    </ChoiceButton>
                  ))}
                </div>
                {/* The blast radius and what the choice costs, for the tier selected.
                    An operator picking "critical" is opting into a human approval on
                    every read, which they should read before saving, not after. */}
                <p className="type-meta leading-relaxed text-muted-foreground">
                  {CREDENTIAL_TIERS.find((t) => t.level === securityLevel)?.blast}
                </p>
                <p className={cn(
                  "type-meta leading-relaxed",
                  securityLevel >= 4 ? "text-warn" : "text-muted-foreground",
                )}>
                  {CREDENTIAL_TIERS.find((t) => t.level === securityLevel)?.consequence}
                </p>

                {/* Same column the tier lives in, same section — "how hard is
                    it to get" and "how long is it good for" are both about
                    what this secret costs to keep around. Wired to
                    `token_expires_at`, which the create request already
                    accepts (internal/api/credentials_mutate.go) and which
                    /credentials' "Expiring" KPI and 30-day warning read. */}
                <CreateSurfaceField
                  label="Expires on"
                  hint="optional — drives the “Expiring” KPI and the 30-day warning on the credential list"
                  htmlFor="cred-expires"
                >
                  <input
                    id="cred-expires"
                    type="date"
                    value={expiresAt}
                    onChange={(e) => setExpiresAt(e.target.value)}
                    className={cn(
                      FIELD,
                      "w-[180px] rounded-md border border-border/60 bg-background px-2.5 font-mono text-foreground outline-none focus:border-primary",
                    )}
                  />
                </CreateSurfaceField>
              </div>
            </CreateSurfaceSection>}

            <details open={login ? undefined : true}>
              <summary className={login ? "cursor-pointer py-2 text-sm text-muted-foreground" : "hidden"}>Advanced delivery settings · {slot || "not assigned"}</summary>
            <CreateSurfaceSection title="Env var slot" icon={Braces} accent="gold">
              {canBind ? (
                <CreateSurfaceField
                  label="Slot"
                  hint="the variable the container sees"
                  htmlFor="cred-slot"
                >
                  <Input
                    id="cred-slot"
                    placeholder="GH_TOKEN"
                    value={slot}
                    onChange={(e) => { setSlotTouched(true); setSlot(e.target.value) }}
                    className={cn(FIELD, "font-mono")}
                  />
                </CreateSurfaceField>
              ) : (
                <p className="type-meta leading-relaxed text-muted-foreground">
                  Choosing the variable name (the slot) is a workspace-admin action, so this credential
                  will be delivered under its own name. An admin can bind it to a slot afterwards.
                </p>
              )}

              {(!canBind || !slot.trim()) && !nameIsEnvVar && name.trim() ? (
                <div className="mt-3 rounded-md border border-warn/30 bg-warn/[0.05] px-3 py-2 type-meta leading-relaxed">
                  Without a slot the container sees this credential under its own name, and{" "}
                  <span className="font-mono break-all">{name.trim()}</span> is not a valid environment-variable
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
              ) : null}
              {canBind && (
                <CardNote>
                  {suggestedSlot && !slotTouched
                    ? "Suggested from the detected brand. Overwrite it freely — this is a hint, not a rule."
                    : "Leave it empty to deliver the credential under its own name."}
                </CardNote>
              )}
            </CreateSurfaceSection>
            </details>
          </>
        )}
      </CreateSurfaceBody>

      {/* Docked, and outside the scrollport for the same reason the buttons
          are: on a phone this is the only part of the surface guaranteed to be
          on screen, so it also carries whatever is blocking the next move — a
          dead Continue button eight fields below the fold explains nothing.
          The wrapper is one band stack, not a second footer; each strip draws
          its own top rule the way CreateSurfaceRefusal does. */}
      <div data-testid="wizard-footer" className="shrink-0">
        {step === "values" && blocker && (
          <p className="border-t border-hairline px-4 py-2 type-meta text-muted-foreground sm:px-5">
            {usingDevice
              ? (deviceBusy ? "Waiting for approval in your browser." : "Complete sign-in above, or import a Codex login.")
              : <><span className="font-medium text-foreground/80">{blocker}</span> is still empty.</>}
          </p>
        )}

        {/* The shell's refusal band: a 409 on the name is the one thing here
            that must not be scrolled past or faded out. */}
        <CreateSurfaceRefusal message={error} />

        {/* Bounded: a partial-save warning quotes whatever the server said
            about every part that failed, and an unbounded one would push the
            buttons it belongs to off a phone. */}
        {warning && (
          <div className="max-h-24 overflow-y-auto border-t border-warn/40 bg-warn/[0.06] px-4 py-2.5 type-meta leading-relaxed break-words sm:px-5">
            {warning}
          </div>
        )}

        <CreateSurfaceFooter
          hint={
            <>
              <kbd className="font-mono">⌘↵</kbd> to {step === "scope" ? "save" : "continue"} ·{" "}
              <kbd className="font-mono">Esc</kbd> to cancel
            </>
          }
          onCancel={onCancel}
          secondary={
            step === "type" ? undefined : (
              <CreateSurfaceSecondaryAction
                icon={ChevronLeft}
                disabled={submitting}
                onClick={() => setStep(step === "scope" ? "values" : "type")}
              >
                Back
              </CreateSurfaceSecondaryAction>
            )
          }
          primaryLabel={step === "scope" ? (login ? "Save login" : "Save secret") : "Continue"}
          onPrimary={primaryAction}
          primaryDisabled={(step === "type" && Boolean(login) && !loginProvider(provider)) || (step === "values" && (Boolean(blocker) || deviceBusy))}
          busy={submitting}
        />
      </div>
    </>
  )
}

/**
 * A pick-one choice. Sized as a target rather than as a chip: the scope and
 * the tier were 22px-tall pills, which is half of the 44px a thumb needs and
 * the reason those two rows were the hardest thing in the dialog to hit.
 */
function ChoiceButton({
  selected, tone = "primary", onClick, children,
}: {
  selected: boolean
  tone?: "primary" | "warn"
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onClick}
      className={cn(
        "flex min-h-10 items-center justify-center rounded-lg border px-3 py-2 text-center type-meta font-medium transition-colors",
        selected
          ? tone === "warn"
            ? "border-warn/50 bg-warn/10 text-warn"
            : "border-primary/50 bg-primary/10 text-primary-hover"
          : "border-border/60 text-muted-foreground hover:border-border hover:text-foreground",
      )}
    >
      {children}
    </button>
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
      <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
        <Label htmlFor={id} className="type-section text-muted-foreground">
          {label}{required ? "" : " (optional)"}
        </Label>
        <button
          type="button"
          onClick={() => setReveal((r) => !r)}
          className="shrink-0 type-meta text-muted-foreground hover:text-foreground"
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
          className={cn(MONO_AREA, !reveal && "[-webkit-text-security:disc]")}
        />
      ) : (
        <Input
          id={id}
          type={reveal ? "text" : "password"}
          placeholder={placeholder}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className={cn(FIELD, "font-mono")}
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
    <div className="space-y-3">
      {fields.map((f, i) => (
        // Stacked below sm: a key box, a value box, a secrecy toggle and a
        // delete button on one 358px row leaves ~70px per input, which is
        // narrower than the placeholder it holds.
        <div key={i} className="space-y-2 rounded-lg border border-border/60 p-2 sm:space-y-0 sm:border-0 sm:p-0">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
            <div className="flex-1 space-y-1">
              <Label className="type-meta text-muted-foreground-soft">Field key</Label>
              <Input
                value={f.key}
                placeholder="tenant_id"
                aria-label={`Custom field ${i + 1} key`}
                onChange={(e) =>
                  onChange(fields.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))
                }
                className={cn(FIELD, MONO_AREA)}
              />
            </div>
            <div className="flex-1 space-y-1">
              <Label className="type-meta text-muted-foreground-soft">Value</Label>
              <Input
                type={f.secret ? "password" : "text"}
                value={f.value}
                aria-label={`Custom field ${i + 1} value`}
                onChange={(e) =>
                  onChange(fields.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))
                }
                className={cn(FIELD, MONO_AREA)}
              />
            </div>
            <div className="flex items-center gap-2 sm:mb-0.5">
              <Badge
                variant="outline"
                // A real button, not a span wearing role="button". A span does
                // not activate on Enter or Space, so a keyboard user could focus
                // this toggle and never change it — and whether a field is
                // secret decides whether its value is encrypted.
                asChild
              >
                <button
                  type="button"
                  aria-pressed={f.secret}
                  aria-label={`Custom field ${i + 1} is ${f.secret ? "secret" : "plain text"}`}
                  onClick={() =>
                    onChange(fields.map((x, j) => (j === i ? { ...x, secret: !x.secret } : x)))
                  }
                  className="cursor-pointer type-meta"
                >
                  {f.secret ? "secret" : "text"}
                </button>
              </Badge>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`Remove custom field ${i + 1}`}
                className="ml-auto sm:ml-0"
                onClick={() => onChange(fields.filter((_, j) => j !== i))}
              >
                <X className="h-3.5 w-3.5" />
              </Button>
            </div>
          </div>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...fields, { key: "", value: "", secret: true }])}
        className="inline-flex min-h-9 items-center gap-1.5 type-meta text-muted-foreground hover:text-foreground"
      >
        <Plus className="h-3.5 w-3.5" /> Add a field
      </button>
    </div>
  )
}
