"use client"

import { credentialEntryError } from "@/lib/credentials/entry-validation"
import { credentialTagClassName } from "@/lib/credentials/tag-accent"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CredentialFileInput } from "./credential-file-input"

/** Guided credential entry: choose a type/provider, enter its value, then
 * explicitly choose delivery. Visibility alone is never presented as a grant.
 * Drafts stay in memory; follow-up writes retain the created identity on retry.
 */

import * as React from "react"
import {
  Check, ChevronLeft, ChevronsUpDown, CreditCard, FileText, KeyRound,
  Plus, ShieldCheck, Tag, Terminal, User, Users, X,
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
import { isValidEnvVarName } from "@/lib/env-var-name"
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
const MONO_AREA = "font-mono text-xs coarse:text-base"

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
  credentialId?: string
  itemType?: ItemTypeKey
  provider?: string
  loginMode?: ProviderLoginMode
  signIn?: SignInMethod
  step?: Step
  name?: string
}

export interface AddCredentialWizardProps {
  workspaceId: string
  onSuccess: (credentialId?: string) => void
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
  { id: "type", label: "Type" },
  { id: "values", label: "Details" },
  { id: "scope", label: "Use" },
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

  const originWorkspace = React.useRef(workspaceId)
  const [chosenType, setChosenType] = React.useState(Boolean(initial?.itemType))
  const [attempted, setAttempted] = React.useState(false)
  const [assignNow, setAssignNow] = React.useState(false)
  const [agentIds, setAgentIds] = React.useState<string[]>([])
  const [agents, setAgents] = React.useState<Crew[]>([])
  const [savedId, setSavedId] = React.useState<string | null>(null)
  const [uncertainSave, setUncertainSave] = React.useState(false)
  const completedWrites = React.useRef(new Set<string>())
  const [pendingChange, setPendingChange] = React.useState<(() => void) | null>(null)
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
  const [description, setDescription] = React.useState("")
  const [accountLabel, setAccountLabel] = React.useState("")
  const [tags, setTags] = React.useState<string[]>([])
  const [tagDraft, setTagDraft] = React.useState("")
  const [provider, setProvider] = React.useState(initial?.provider ?? "NONE")
  const providerTouched = React.useRef(Boolean(initial?.provider))
  const [scope, setScope] = React.useState<"WORKSPACE" | "CREW" | "AGENT">("WORKSPACE")
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

  React.useEffect(() => {
    if (!assignNow) return
    let active = true
    apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => { if (!r.ok) throw new Error(); return r.json() })
      .then((data) => { if (active) setAgents(Array.isArray(data) ? data : []) })
      .catch(() => { if (active) setError("Could not load agents. Try selecting Assign now again.") })
    return () => { active = false }
  }, [assignNow, workspaceId])

  function changeInput(action: () => void) {
    if (primaryValue || Object.values(extras).some(Boolean) || custom.some((f) => f.value)) {
      setPendingChange(() => action)
    } else action()
  }

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
    () => {
      const candidate = detectBrandFromValue(primaryValue) ?? detectBrandFromName(name)
      // Generic shape icons are selectable, but do not identify an issuing service.
      return candidate?.key.startsWith("VAULT_") ? null : candidate
    },
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
  const usingDevice = deviceFlowAvailable && signIn === "device" && !initial?.credentialId

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

  const missingRequired = React.useMemo(() => {
    // A provider login without a provider cannot be delivered anywhere — the
    // server routes and renders it by the provider column — and a brand with
    // no subscription login is a save that would only fail at run time.
    if (!name.trim()) return "Name"
    if (login && !login.supported) return "Provider"
    // With a code the server holds the value; the step waits for the sign-in.
    if (usingDevice) return deviceCredentialId ? null : "Sign-in"
    if (!primaryValue.trim()) return login?.label ?? itemType.primary.label
    if (itemType.usernameOnRow && !username.trim()) return "Username"
    for (const f of itemType.extra) {
      if (f.required && !(extras[f.key] ?? "").trim()) return f.label
    }
    return null
  }, [name, itemType, login, usingDevice, deviceCredentialId, primaryValue, username, extras])

  // What is holding step 2 back, in the order the boxes are on screen. The
  // Continue button being dead is not an explanation; naming the box is.
  const valueError = usingDevice ? null : credentialEntryError(primaryValue, itemTypeKey, provider, loginMode)
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
      description ||
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
    if (originWorkspace.current !== workspaceId) { setError("Workspace changed. Close this form and start again in the intended workspace."); return }
    if (submitting || uncertainSave) return
    setError(null)
    setWarning(null)
    if (!name.trim()) {
      setError("Give the credential a name — it identifies the account, not the env var.")
      return
    }
    if (assignNow && scope === "AGENT" && agentIds.length === 0) { setError("Choose at least one agent."); return }
    if (assignNow && !isValidEnvVarName(slot.trim())) { setError("Use a valid variable name, for example GH_TOKEN."); return }
    if (assignNow && scope === "CREW" && crewIds.length === 0) {
      setError("Pick at least one crew, or switch the scope back to the whole workspace.")
      return
    }
    setSubmitting(true)
    let saveRequested = false
    try {
      let credentialId: string | undefined = savedId ?? undefined
      // Check occupied slots before creating the secret. The server still enforces
      // uniqueness if another editor claims the slot between these requests.
      if (assignNow && canBind) {
        const check = await apiFetch(`/api/v1/credentials/bindings?workspace_id=${encodeURIComponent(workspaceId)}&slot=${encodeURIComponent(slot.trim())}`)
        if (!check.ok) { setError("Could not check assignment conflicts. Try again before saving."); return }
        const data = await check.json()
        const occupied = (data.bindings ?? []).some((b: {scope: string; crew_id?: string; agent_id?: string; credential_id: string}) =>
          b.credential_id !== credentialId && b.scope === scope && (scope === "WORKSPACE" || (scope === "CREW" ? crewIds.includes(b.crew_id ?? "") : agentIds.includes(b.agent_id ?? ""))))
        if (occupied) { setError(`${slot.trim()} is already assigned for a selected target. Choose another variable or update the existing assignment.`); return }
      }
      // Everything past the create is a follow-up write on a credential that
      // ALREADY EXISTS. A failure there is reported as a warning, never as
      // "save failed" — telling the user nothing was saved when a secret is
      // now in the vault is the worse of the two lies.
      const problems: string[] = []

      if (initial?.credentialId) {
        saveRequested = true
        const response = await apiFetch(`/api/v1/credentials/${encodeURIComponent(initial.credentialId)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
          method: "PATCH", headers: {"Content-Type": "application/json"}, body: JSON.stringify({value: primaryValue, mode: loginMode}),
        })
        if (!response.ok) { const result = await response.json().catch(() => ({})); setError(result.error || "Could not update this login."); return }
        onSuccess(initial.credentialId); return
      }
      if (deviceCredentialId) {
        // The sign-in created the row (§10.3: the device status names it).
        // Persist the chosen account details and access; the value never
        // came through this browser.
        credentialId = deviceCredentialId
        setSavedId(credentialId)
        try {
          const pr = await apiFetch(
            `/api/v1/credentials/${encodeURIComponent(credentialId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
            {
              method: "PATCH",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ name: name.trim(), description: description.trim(), account_label: accountLabel.trim(), tags: Array.from(new Set([...tags, ...(tagDraft.trim() ? [tagDraft.trim().toLowerCase()] : [])])).slice(0, 8), scope: "WORKSPACE" }),
            },
          )
          if (!pr.ok) problems.push(`account details and access (HTTP ${pr.status})`)
        } catch {
          problems.push("account details and access")
        }
      } else if (!credentialId) {
        const body: Record<string, unknown> = {
          name: name.trim(),
          value: primaryValue,
          description: description.trim(),
          type: login ? providerLoginCredentialType(loginMode) : itemType.credentialType,
          provider,
          scope: "WORKSPACE",
          tags: Array.from(new Set([...tags, ...(tagDraft.trim() ? [tagDraft.trim().toLowerCase()] : [])])).slice(0, 8),
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


        saveRequested = true
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
        if (!credentialId) { setUncertainSave(true); setError("The save response was incomplete. Check the credential list before creating another entry."); return }
        setSavedId(credentialId)
      }

      const fields = extraFieldsFor(itemTypeKey, extras, custom)
      if (credentialId && fields.length > 0) {
        for (const field of fields) {
          if (completedWrites.current.has(`field:${field.key}`)) continue
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
            else completedWrites.current.add(`field:${field.key}`)
          } catch {
            problems.push(field.key)
          }
        }
      }

      if (credentialId && assignNow && canBind && slot.trim()) {
        const targets = scope === "CREW" ? crewIds : scope === "AGENT" ? agentIds : [""]
        for (const targetId of targets) {
          const writeKey = `binding:${scope}:${targetId}:${slot.trim()}`
          if (completedWrites.current.has(writeKey)) continue
          try {
            const br = await apiFetch(
              `/api/v1/credentials/bindings?workspace_id=${encodeURIComponent(workspaceId)}`,
              {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                  credential_id: credentialId,
                  scope,
                  crew_id: scope === "CREW" ? targetId : "",
                  agent_id: scope === "AGENT" ? targetId : "",
                  slot: slot.trim(),
                }),
              },
            )
            if (br.ok) completedWrites.current.add(writeKey)
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
            `Retry the missing parts, or finish and open the saved credential.`,
        )
        if (credentialId) setSavedId(credentialId)
        return
      }
      onSuccess(credentialId)
    } catch {
      setUncertainSave(saveRequested)
      setError(saveRequested ? "The connection was interrupted; the credential may have been saved. Check the list before creating another entry." : "Could not check assignment conflicts. Nothing new was saved; try again.")
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
    if (step === "type") return
    if (step === "scope" || (step === "values" && initial?.credentialId && !blocker && !valueError)) {
      void submit()
      return
    }
    if (step === "values" && valueError) { setAttempted(true); document.getElementById("cred-primary")?.focus(); return }
    if (step === "values" && (blocker || deviceBusy)) {
      setAttempted(true)
      const id = blocker === "Name" ? "cred-name" : blocker === "Username" ? "cred-username" : itemType.extra.find((f) => f.label === blocker) ? `cred-extra-${itemType.extra.find((f) => f.label === blocker)!.key}` : "cred-primary"
      document.getElementById(id)?.focus()
      return
    }
    if (custom.some((field) => Boolean(field.key.trim()) !== Boolean(field.value.trim()))) { setError("Complete the name and value of each extra field, or remove the unused field."); return }
    const keys = [...itemType.extra.map((field) => field.key), ...custom.filter((field) => field.key.trim()).map((field) => field.key.trim())]
    if (new Set(keys).size !== keys.length) { setError("Each extra field needs a unique name."); return }
    setError(null)
    setAttempted(false)
    setStep("scope")
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
        steps={login ? [{ id: "type", label: "Provider" }, { id: "values", label: "Connect" }, { id: "scope", label: "Use" }] : STEPS}
        current={stepIndex}
        onJump={(i) => { if (!initial?.credentialId && !savedId && !deviceBusy && !deviceCredentialId) setStep(STEP_ORDER[i]) }}
      />

      {/* The only scrollport. Everything that has to stay reachable — the step
          bar above, the actions below — lives outside it. */}
      <CreateSurfaceBody data-testid="wizard-body" className="space-y-5 pb-6">
        {step === "type" && (
          <>
            {login ? (
              <>
                <CreateSurfaceSection title="Connect an AI provider" icon={KeyRound} accent="blue">
                  <p className="type-meta text-muted-foreground">Choose your provider. We will guide you through its supported sign-in methods.</p>
                </CreateSurfaceSection>
                <LoginProviderPicker value={provider} locked={Boolean(deviceCredentialId)} onChange={(key) => {
                  if (key === provider) setStep("values"); else changeInput(() => { selectLoginProvider(key); setStep("values") })
                }} />
              </>
            ) : <>
            <CreateSurfaceSection title="Choose a secret type" icon={KeyRound} accent="amber">
              <p className="type-meta leading-relaxed text-muted-foreground">
                Choose what you want to store. We will show only the fields it needs.
              </p>
            </CreateSurfaceSection>
            {/* Two-up on a phone: six tiles in one column is four thumb-swipes
                to reach Certificate, and three-up leaves 110px of tile for a
                label plus a blurb. */}
            <div data-testid="shape-grid" className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {CREDENTIAL_ITEM_TYPES.filter((t) => t.key !== "PROVIDER_LOGIN").map((t) => {
                const Icon = TYPE_ICON[t.key]
                const selected = chosenType && t.key === itemTypeKey
                const tone = SHAPE_ACCENT[t.key] ?? ACCENT.slate
                return (
                  <button
                    key={t.key}
                    type="button"
                    aria-pressed={selected}
                    onClick={() => {
                      const select = () => {
                        if (t.key !== itemTypeKey) { setPrimaryValue(""); setExtras({}); setCustom([]) }
                        setItemTypeKey(t.key); setChosenType(true); setStep("values"); setAttempted(false)
                      }
                      if (t.key !== itemTypeKey) changeInput(select); else select()
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

          </>
        )}

        {step === "values" && (
          <>
            <div className="flex items-center gap-3 rounded-xl border border-border/60 bg-card p-3">
              {login ? <BrandIcon className="size-5" /> : <ItemIcon className={cn("size-5", SHAPE_ACCENT[itemTypeKey]?.fg)} />}
              <span className="flex-1 text-sm font-medium">{login ? loginProvider(provider)?.label : itemType.label}</span>
              <Button variant="ghost" size="sm" disabled={deviceBusy || Boolean(deviceCredentialId) || Boolean(savedId) || Boolean(initial?.credentialId)} onClick={() => setStep("type")}>Change</Button>
            </div>
            <details open={login && name.trim() ? undefined : true}>
              <summary className={login ? "cursor-pointer py-2 text-sm text-muted-foreground" : "hidden"}>Name and labels · {name || "your account"}</summary>
            <CreateSurfaceSection title="Identity" icon={Tag} accent="blue">
              <div className="space-y-3">
                <div className="space-y-1.5">
                  {/* Wraps: "NAME (WHICH ACCOUNT)" is ~170px of wide-tracked
                      uppercase and the picker is ~130px, which is more than a
                      phone's 326px card body once the gap is paid. */}
                  <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1.5">
                    <Label htmlFor="cred-name" className="type-section text-muted-foreground">
                      Name
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
                    aria-invalid={attempted && !name.trim()}
                    id="cred-name"
                    placeholder={login ? `e.g. ${loginProvider(provider)?.label ?? "Provider"} · my account` : "e.g. github-acme"}
                    value={name}
                    disabled={Boolean(savedId) || Boolean(initial?.credentialId)}
                    onChange={(e) => { nameTouched.current = true; setName(e.target.value) }}
                    className={cn(FIELD, "font-mono")}
                  />
                  {attempted && !name.trim() && <p className="text-xs text-destructive">Enter a name.</p>}
                </div>

                <details className="rounded-lg border border-border/60 p-3">
                  <summary className="cursor-pointer text-xs text-muted-foreground">Description, account label & tags <span className="ml-1">· optional</span></summary>
                  <div className="mt-3 space-y-3">
                <CreateSurfaceField label="Description" hint="optional" htmlFor="cred-description"><Textarea id="cred-description" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What is this credential used for?" /></CreateSurfaceField>
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
                      <Badge key={t} variant="outline" className={cn("gap-1 type-meta font-mono", credentialTagClassName(t))}>
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
                      className="min-w-[80px] flex-1 bg-transparent type-meta outline-none coarse:text-base placeholder:text-muted-foreground"
                    />
                    {knownTags && knownTags.length > 0 && (
                      <datalist id="cred-wizard-tags">
                        {knownTags.filter((t) => !tags.includes(t)).map((t) => <option key={t} value={t} />)}
                      </datalist>
                    )}
                  </div>
                </CreateSurfaceField>
                  </div>
                </details>
              </div>
              <CardNote>
                {login
                  ? "A name to recognize this account."
                  : "A name to recognize this secret. Configure delivery in the next step."}
              </CardNote>
            </CreateSurfaceSection>

            </details>

            <CreateSurfaceSection title={login ? `Connect ${loginProvider(provider)?.label ?? "your provider"}` : "The secret"} hint={login ? undefined : itemType.label.toLowerCase()} icon={login ? BrandIcon : ItemIcon} accent="amber">
              <div className="space-y-3">
                {login && (
                  <div className="space-y-3">
                    {loginProvider(provider)?.subscription && <div role="group" aria-label="Connection method" className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                      {[
                        ...(provider === "OPENAI" ? [
                          ...(!initial?.credentialId ? [{label: "Sign in with a code", mode: "subscription" as const, method: "device" as const}] : []),
                          {label: "Import from Codex CLI", mode: "subscription" as const, method: "paste" as const},
                        ] : [{label: provider === "ANTHROPIC" ? "Setup token" : "Import Gemini login", mode: "subscription" as const, method: "paste" as const}]),
                        {label: "API key", mode: "api_key" as const, method: "paste" as const},
                      ].map((method) => {
                        const selected = method.mode === loginMode && (method.method === signIn || Boolean(initial?.credentialId))
                        return <button key={method.label} type="button" aria-pressed={selected}
                          disabled={Boolean(deviceCredentialId)}
                          onClick={() => {
                            if (selected) return
                            changeInput(() => { setDeviceBusy(false); setLoginMode(method.mode); setSignIn(method.method); setPrimaryValue("") })
                          }}
                          className={cn("min-h-10 rounded-lg border px-3 py-2 text-left text-sm transition-colors", selected ? "border-primary/60 bg-primary/10" : "border-border/60 bg-card hover:bg-surface-raised")}>{method.label}</button>
                      })}
                    </div>}
                    {initial?.credentialId && provider === "OPENAI" && loginMode === "subscription" && <CardNote>Import a fresh Codex login to update this account and keep its assignments.</CardNote>}
                    {loginMode === "api_key" && connectionGuide ? <div className="space-y-2">
                      <p className="type-meta text-muted-foreground">{connectionGuide.instruction}</p>
                      <a href={connectionGuide.url} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-10 items-center text-sm font-medium text-primary hover:underline">Get API key ↗</a>
                      {connectionGuide.note && <p className="type-meta text-muted-foreground">{connectionGuide.note}</p>}
                    </div> : login.hint && !usingDevice && <CardNote>{login.hint}</CardNote>}
                  </div>
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

                <div className={cn(itemTypeKey === "KEYPAIR" && "grid gap-3 sm:grid-cols-2")} >
                {itemTypeKey === "KEYPAIR" && <CreateSurfaceField label="Access key ID" htmlFor="cred-extra-access_key_id"><Input id="cred-extra-access_key_id" value={extras.access_key_id ?? ""} onChange={(e) => setExtras((v) => ({...v, access_key_id: e.target.value}))} /><CardNote>Stored in the clear so it stays searchable.</CardNote></CreateSurfaceField>}
                {usingDevice ? deviceCredentialId ? (
                  <div><CardNote>Signed in. Continue to choose access for this account.</CardNote><p className="mt-2 text-xs text-muted-foreground">This account is already saved. Closing setup keeps it in the vault.</p></div>
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
                    key={`${itemTypeKey}:${login ? provider : "secret"}:${loginMode}:${signIn}`}
                    fileInput={Boolean(login?.multiline ?? itemType.primary.multiline)}
                    jsonFile={Boolean(login && loginMode === "subscription" && (provider === "OPENAI" || provider === "GOOGLE"))}
                    onFilename={itemTypeKey === "FILE" ? (filename) => setExtras((v) => ({...v, filename})) : undefined}
                    invalid={attempted && Boolean(missingRequired)}
                    id="cred-primary"
                    label={login?.label ?? itemType.primary.label}
                    required
                    multiline={login?.multiline ?? itemType.primary.multiline}
                    placeholder={login?.placeholder ?? itemType.primary.placeholder}
                    value={primaryValue}
                    onChange={setPrimaryValue}
                  />
                )}
                </div>
                {attempted && valueError && <p role="alert" className="text-xs text-destructive">{valueError}</p>}
                {login && !usingDevice && (
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
                  <p className="flex items-start gap-1.5 type-meta text-muted-foreground">
                    <BrandIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" style={{ color: brandColor(brand) }} aria-hidden="true" />
                    <span className="min-w-0 break-words">
                      Looks like {detected.label}
                      {suggestedSlot && <> — we&apos;ll suggest <span className="font-mono">{suggestedSlot}</span> as the variable name</>}
                    </span>
                  </p>
                )}

                {itemType.extra.filter((f) => f.key !== "access_key_id").map((f) => (
                  <details key={f.key} open={f.required ? true : undefined} className="rounded-lg border border-border/60 p-3"><summary className="cursor-pointer text-xs text-muted-foreground">{f.label} · optional</summary><div className="mt-3">{
                  f.secret ? (
                    <SecretField
                      key={f.key}
                      id={`cred-extra-${f.key}`}
                      label={f.label}
                      required={f.required}
                      multiline={f.multiline}
                      fileInput={Boolean(f.multiline)}
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
                }</div></details>))}

                {itemType.fileNote && (
                  <div className="rounded-md border border-warn/30 bg-warn/[0.05] px-3 py-2 type-meta leading-relaxed text-foreground/80">
                    {itemType.fileNote}
                  </div>
                )}
              </div>
            </CreateSurfaceSection>

            {!login && <details className="rounded-xl border border-border/60 p-3">
              <summary className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground"><Plus className="size-3.5" /> Extra fields · optional</summary>
              <CreateSurfaceSection className="mt-3">
              <CustomFields fields={custom} onChange={setCustom} />
              <CardNote>
                Anything else that travels with this credential — a tenant id, an endpoint. Each part is
                stored separately and can be secret or plain.
              </CardNote>
            </CreateSurfaceSection></details>}
          </>
        )}

        {step === "scope" && (
          <>
            <div className="flex items-center gap-3 rounded-xl border border-border/60 p-3">
              {login ? <BrandIcon className="size-5" /> : <ItemIcon className="size-5" />}
              <div><h3 className="text-sm font-medium">{name}</h3><p className="text-xs text-muted-foreground">{deviceCredentialId ? "Account saved · finish its setup" : "Connection not tested · save does not verify access"}</p></div>
            </div>
            <CreateSurfaceSection title="How will you use it?" icon={Users} accent="teal">
              <div className="grid grid-cols-2 gap-2">
                <ChoiceButton selected={!assignNow} onClick={() => setAssignNow(false)}>Save for later</ChoiceButton>
                {canBind && <ChoiceButton selected={assignNow} onClick={() => { setAssignNow(true); setScope("AGENT") }}>Assign now</ChoiceButton>}
              </div>
              <CardNote>{assignNow ? "Choose who will receive this credential. Runtime access still follows workspace policy." : "Save in the workspace vault without creating delivery assignments. You can assign it later."}</CardNote>
            </CreateSurfaceSection>
            {assignNow && <>
            <CreateSurfaceSection title="Assign to" icon={Users} accent="teal">
              <div className="space-y-3">
                {/* Grouped rather than labelled: the card header already says
                    "Who gets it", and a second sr-only <label> pointing at
                    nothing is noise in the a11y tree, not help. */}
                <div role="group" aria-label="Who gets it" className="grid grid-cols-2 gap-2">
                  {(["AGENT", "CREW", "WORKSPACE"] as const).map((s) => (
                    <ChoiceButton
                      key={s}
                      selected={scope === s}
                      onClick={() => { setScope(s); if (s === "WORKSPACE") setCrewIds([]) }}
                    >
                      {s === "WORKSPACE" ? "All agents" : s === "CREW" ? "Selected crews" : "Selected agents"}
                    </ChoiceButton>
                  ))}
                </div>

                {scope === "AGENT" && <div className="max-h-48 space-y-1 overflow-y-auto">
                  {agents.length === 0 && <p className="text-xs text-muted-foreground">No agents available.</p>}
                  {agents.map((agent) => <label key={agent.id} className="flex cursor-pointer items-center gap-2 rounded-lg border border-border/60 p-2 text-sm"><input type="checkbox" checked={agentIds.includes(agent.id)} onChange={(e) => setAgentIds(e.target.checked ? [...agentIds, agent.id] : agentIds.filter((id) => id !== agent.id))} /><AgentAvatar seed={agent.id} className="size-6" alt="" />{agent.name}</label>)}
                </div>}
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
              <CardNote>Assignments take effect on the next run, subject to access policy.</CardNote>
            </CreateSurfaceSection>
            {login ? <CardNote>Uses the provider’s supported authentication delivery. The agent’s model must use this provider.</CardNote> : <CreateSurfaceField label="Variable name" htmlFor="cred-slot"><Input id="cred-slot" value={slot} placeholder="GH_TOKEN" onChange={(e) => { setSlotTouched(true); setSlot(e.target.value) }} /><CardNote>Used by the runtime to deliver this credential. Existing assignments are never overwritten.</CardNote></CreateSurfaceField>}
            </>}

            {!login && <details className="rounded-xl border border-border/60 p-4">
              <summary className="flex cursor-pointer items-center gap-2 text-sm font-medium"><ShieldCheck className="size-4 text-muted-foreground" /> Access & security <span className="ml-auto text-xs text-muted-foreground">{CREDENTIAL_TIERS.find((t) => t.level === securityLevel)?.label}</span></summary>
              <CreateSurfaceSection className="mt-4"
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
                  hint="optional — reminds you before this credential expires"
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
            </CreateSurfaceSection></details>}

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
        {pendingChange && <div role="alert" className="border-t border-warn/40 p-4 text-sm">Changing this choice clears the current secret value and its extra fields. Name and labels stay.
          <div className="mt-2 flex gap-2"><Button variant="outline" onClick={() => setPendingChange(null)}>Keep editing</Button><Button onClick={() => { pendingChange(); setPendingChange(null); setPrimaryValue(""); setExtras({}); setCustom([]) }}>Change and clear value</Button></div>
        </div>}
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
            <Button variant="ghost" onClick={() => onSuccess(savedId ?? undefined)}>Finish and open saved credential</Button>
          </div>
        )}

        <CreateSurfaceFooter
          hint={
            <>
              <kbd className="font-mono">⌘↵</kbd> to {step === "scope" ? "save" : "continue"} ·{" "}
              <kbd className="font-mono">Esc</kbd> to cancel
            </>
          }
          cancelLabel={deviceCredentialId || savedId ? "Close setup" : "Cancel"}
          onCancel={deviceCredentialId || savedId ? () => onSuccess(savedId ?? deviceCredentialId ?? undefined) : onCancel}
          secondary={
            step === "type" || initial?.credentialId ? undefined : (
              <CreateSurfaceSecondaryAction
                icon={ChevronLeft}
                disabled={submitting || Boolean(savedId) || Boolean(deviceCredentialId)}
                onClick={() => setStep(step === "scope" ? "values" : "type")}
              >
                Back
              </CreateSurfaceSecondaryAction>
            )
          }
          primaryLabel={initial?.credentialId ? "Update login" : step === "type" ? undefined : uncertainSave ? "Check credential list" : savedId ? "Retry missing parts" : step === "scope" ? (deviceCredentialId ? "Finish setup" : assignNow ? "Save & assign" : login ? "Save provider" : "Save secret") : "Continue"}
          onPrimary={uncertainSave ? () => onSuccess() : primaryAction}
          primaryDisabled={Boolean(pendingChange) || (step === "values" && deviceBusy)}
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
  id, label, value, onChange, required, multiline, placeholder, fileInput, jsonFile, onFilename, invalid,
}: {
  id: string
  label: string
  value: string
  onChange: (v: string) => void
  required?: boolean
  multiline?: boolean
  placeholder?: string
  fileInput?: boolean
  jsonFile?: boolean
  onFilename?: (name: string) => void
  invalid?: boolean
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
      {fileInput && <CredentialFileInput id={id} value={value} onChange={onChange} jsonOnly={jsonFile} onFilename={onFilename} />}
      {invalid && !value.trim() && <p className="text-xs text-destructive">Enter {label.toLowerCase()}.</p>}
      {multiline ? (
        <Textarea
          id={id}
          aria-invalid={invalid}
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
