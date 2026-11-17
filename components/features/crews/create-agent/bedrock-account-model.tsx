"use client"

import * as React from "react"
import Link from "next/link"
import { Check, Plus } from "lucide-react"

import { CreateSurfaceField } from "@/components/layout/create-surface"
import { LoginBrandMark, LoginStatusPill } from "@/components/features/credentials/provider-login-bits"
import { apiFetch } from "@/lib/api-fetch"
import { bedrockAccountModels, bedrockAgentModel, bedrockModelFor, bedrockModelLabel, bedrockNativeModel } from "@/lib/credentials/bedrock"
import type { LoginCredential } from "@/lib/credentials/provider-logins"
import { cn } from "@/lib/utils"
import { moveRadio, radioTabIndex } from "./radio-keys"

/**
 * Amazon Bedrock in New agent › Model and the agent Edit dialog: the account
 * comes first, because a Bedrock model only exists on the account that
 * permits it. The models offered are that account's allowlist, never a
 * catalog, so the agent cannot be saved on a model its run would refuse.
 *
 * Picking an account is the same draft as Pays with (`onSeat`); the dialog
 * applies it after the agent is saved, through the Bedrock assignment that
 * sets the account, the model and the OpenCode runner together.
 */
export function BedrockAccountModel({ workspaceId, model, seat, boundId = null, autoPick = true, onChange }: {
  workspaceId: string
  /** The agent's llm_model (`amazon-bedrock/<id>`). */
  model: string
  /** The account picked in this draft, if any. */
  seat: LoginCredential | null
  /** The account the saved agent pays with now (Edit). */
  boundId?: string | null
  /** Pick the only account by itself (New agent). Edit never changes the payer on open. */
  autoPick?: boolean
  /** `account` is null when the pick is the account the agent already pays with. */
  onChange: (account: LoginCredential | null, model: string) => void
}) {
  const [accounts, setAccounts] = React.useState<LoginCredential[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    const controller = new AbortController()
    void (async () => {
      try {
        const res = await apiFetch(`/api/v1/credentials?workspace_id=${encodeURIComponent(workspaceId)}&kind=provider_login`, { signal: controller.signal })
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const list = (await res.json()) as unknown
        const rows = Array.isArray(list) ? (list as LoginCredential[]) : []
        setAccounts(rows.filter((c) => c?.login?.provider === "AWS_BEDROCK" && bedrockAccountModels(c).length > 0))
      } catch (err) {
        if ((err as { name?: string })?.name === "AbortError") return
        setError(err instanceof Error ? err.message : "network error")
        setAccounts([])
      }
    })()
    return () => controller.abort()
  }, [workspaceId])

  // One account and nothing chosen yet: it is the only answer, so take it.
  const only = autoPick && accounts?.length === 1 && !boundId ? accounts[0] : null
  React.useEffect(() => {
    if (only && !seat) onChange(only, bedrockAgentModel(bedrockModelFor(only, model)))
  }, [only, seat, model, onChange])

  const currentId = seat?.id ?? boundId
  const current = accounts?.find((a) => a.id === currentId) ?? null
  const models = current ? bedrockAccountModels(current) : []
  const selected = bedrockNativeModel(model)
  const pick = React.useCallback((account: LoginCredential, next: string) => onChange(account.id === boundId ? null : account, next), [onChange, boundId])

  // The account is set but the draft's model is not one it permits (a switch
  // to another provider and back empties it): take the account's first, so
  // the agent is never saved on a model its account refuses.
  const repair = current && models.length > 0 && !models.includes(selected) ? current : null
  React.useEffect(() => {
    if (repair) pick(repair, bedrockAgentModel(bedrockAccountModels(repair)[0]))
  }, [repair, pick])

  return (
    <>
      <CreateSurfaceField label="Pays with" hint="The Amazon Bedrock account this agent runs on. Its models are the ones offered below.">
        {accounts === null && <p role="status" className="text-sm text-muted-foreground">Loading Bedrock accounts…</p>}
        {error && <p role="alert" className="text-sm text-destructive">Couldn&apos;t load the Bedrock accounts ({error}).</p>}
        {accounts && !error && accounts.length === 0 && (
          <div className="flex flex-col gap-2 rounded-lg border border-dashed border-border p-3 text-sm">
            <span>No Amazon Bedrock account is connected in this workspace.</span>
            <Link href="/credentials?tab=providers" className="inline-flex items-center gap-1 text-primary hover:underline">
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />Connect Amazon Bedrock in Vault
            </Link>
          </div>
        )}
        {accounts && accounts.length > 0 && (
          <div role="radiogroup" aria-label="Bedrock account" onKeyDown={moveRadio} className="grid grid-cols-1 gap-2 md:grid-cols-2">
            {accounts.map((account, index) => {
              const on = account.id === current?.id
              const b = account.login?.bedrock
              return (
                <button
                  key={account.id}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  tabIndex={radioTabIndex(on, index, Boolean(current))}
                  onClick={() => pick(account, bedrockAgentModel(bedrockModelFor(account, model)))}
                  className={cn("flex min-h-9 coarse:min-h-[44px] items-start gap-3 rounded-lg border p-3 text-left focus-visible:ring-2 focus-visible:ring-primary", on ? "border-primary bg-primary/10" : "border-border hover:bg-muted")}
                >
                  <LoginBrandMark provider="AWS_BEDROCK" size="sm" />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm">{account.name}</span>
                    <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                      {[b?.region, `${bedrockAccountModels(account).length} ${bedrockAccountModels(account).length === 1 ? "model" : "models"}`, b?.eu_only ? "EU only" : null].filter(Boolean).join(" · ")}
                    </span>
                  </span>
                  <LoginStatusPill credential={account} />
                </button>
              )
            })}
          </div>
        )}
      </CreateSurfaceField>
      {current && (
        <CreateSurfaceField label="Model" hint={`Allowed by ${current.name}. Change the list in Vault.`}>
          <div role="radiogroup" aria-label="Bedrock model" onKeyDown={moveRadio} className="flex flex-col overflow-hidden rounded-lg border border-border">
            {models.map((id, index) => {
              const on = id === selected
              return (
                <button
                  key={id}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  tabIndex={radioTabIndex(on, index, models.includes(selected))}
                  onClick={() => pick(current, bedrockAgentModel(id))}
                  className={cn("flex min-h-9 coarse:min-h-[44px] flex-wrap items-center gap-x-3 gap-y-0.5 border-b border-border px-3 py-2 text-left last:border-b-0 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary", on ? "bg-primary/10" : "hover:bg-muted")}
                >
                  <Check className={cn("h-3.5 w-3.5 shrink-0 text-primary", !on && "invisible")} aria-hidden="true" />
                  <span className="text-sm">{bedrockModelLabel(id)}</span>
                  <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">{id}</span>
                </button>
              )
            })}
          </div>
        </CreateSurfaceField>
      )}
    </>
  )
}
