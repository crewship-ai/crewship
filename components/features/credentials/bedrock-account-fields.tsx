"use client"

import * as React from "react"

import { CreateSurfaceField } from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"
import { BEDROCK_PROFILES, BEDROCK_REGIONS, bedrockModelLabel, bedrockModels } from "@/lib/credentials/bedrock"
import { cn } from "@/lib/utils"

const PROFILE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/

/**
 * Region, permitted models and the EU policy of a Bedrock account (Vault ›
 * Add AI provider). The region is one the server has an endpoint for; the
 * models are picked from the common inference profiles, and any other
 * profile id the account has can be added. EU-only hides the profiles that
 * may be served outside the EU and keeps the region in the EU.
 */
export function BedrockAccountFields({ region, models, euOnly, onChange }: {
  region: string
  /** Newline-separated native ids, the wizard's stored form. */
  models: string
  euOnly: boolean
  onChange: (next: { region?: string; models?: string; euOnly?: boolean }) => void
}) {
  const [extra, setExtra] = React.useState("")
  const picked = bedrockModels(models)
  const offered = Array.from(new Set([...BEDROCK_PROFILES.filter((id) => !euOnly || id.startsWith("eu.")), ...picked]))
  const set = (ids: string[]) => onChange({ models: ids.join("\n") })
  const toggle = (id: string, on: boolean) => set(on ? [...picked, id] : picked.filter((m) => m !== id))
  const extraId = extra.trim().replace(/^amazon-bedrock\//, "")
  const extraError = extraId === "" ? null
    : !PROFILE_ID.test(extraId) ? "Use the profile id as AWS shows it, without spaces."
    : euOnly && !extraId.startsWith("eu.") ? "EU only accepts eu. profiles."
    : null
  const addExtra = () => {
    if (!extraId || extraError) return
    if (!picked.includes(extraId)) set([...picked, extraId])
    setExtra("")
  }

  return (
    <div className="space-y-3">
      <CreateSurfaceField label="AWS region" htmlFor="bedrock-region" hint="Where the Bedrock key was created.">
        <select
          id="bedrock-region"
          value={region}
          onChange={(e) => onChange({ region: e.target.value })}
          className="h-9 w-full rounded-md border border-border bg-background px-2 text-sm coarse:h-[44px]"
        >
          <optgroup label="Europe">
            {BEDROCK_REGIONS.filter((r) => r.eu).map((r) => <option key={r.id} value={r.id}>{r.label} · {r.id}</option>)}
          </optgroup>
          {!euOnly && (
            <optgroup label="Other regions">
              {BEDROCK_REGIONS.filter((r) => !r.eu).map((r) => <option key={r.id} value={r.id}>{r.label} · {r.id}</option>)}
            </optgroup>
          )}
        </select>
      </CreateSurfaceField>

      <label className="flex min-h-9 coarse:min-h-[44px] items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={euOnly}
          onChange={(e) => {
            const on = e.target.checked
            onChange(on
              ? { euOnly: true, models: picked.filter((m) => m.startsWith("eu.")).join("\n"), region: region.startsWith("eu-") ? region : "eu-central-1" }
              : { euOnly: false })
          }}
        />
        EU profiles only — the agent, its helper calls and sub-agents stay on eu. inference profiles
      </label>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Models this account may use</legend>
        <div className="flex flex-col overflow-hidden rounded-lg border border-border">
          {offered.map((id) => (
            <label key={id} className="flex min-h-9 coarse:min-h-[44px] cursor-pointer flex-wrap items-center gap-x-3 gap-y-0.5 border-b border-border px-3 py-2 last:border-b-0 hover:bg-muted">
              <input type="checkbox" checked={picked.includes(id)} onChange={(e) => toggle(id, e.target.checked)} />
              <span className="text-sm">{bedrockModelLabel(id)}</span>
              <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">{id}</span>
            </label>
          ))}
        </div>
        <div className="flex items-start gap-2">
          <div className="flex-1">
            <Input
              aria-label="Another inference profile id"
              value={extra}
              placeholder={euOnly ? "eu.… profile id from the AWS console" : "Profile id from the AWS console"}
              aria-invalid={Boolean(extraError)}
              onChange={(e) => setExtra(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); addExtra() } }}
              className={cn("font-mono", extraError && "border-destructive")}
            />
            {extraError && <p className="mt-1 text-xs text-destructive">{extraError}</p>}
          </div>
          <button type="button" onClick={addExtra} disabled={!extraId || Boolean(extraError)} className="h-9 rounded-md border border-border px-3 text-sm disabled:opacity-40 coarse:h-[44px]">Add</button>
        </div>
        <p className="text-xs text-muted-foreground">{picked.length} selected. Test checks each one against AWS before agents can use it.</p>
      </fieldset>
    </div>
  )
}
