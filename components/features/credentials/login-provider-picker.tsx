"use client"

import * as React from "react"
import { Input } from "@/components/ui/input"
import { LOGIN_PROVIDERS } from "@/lib/credentials/login-providers"
import { LoginBrandMark } from "./provider-login-bits"
import { cn } from "@/lib/utils"

export function LoginProviderPicker({ value, onChange, locked = false }: { value: string; onChange: (key: string) => void; locked?: boolean }) {
  const [query, setQuery] = React.useState("")
  const providers = LOGIN_PROVIDERS.filter((p) => `${p.label} ${p.key}`.toLowerCase().includes(query.toLowerCase()))
  return (
    <div className="space-y-3"><Input aria-label="Search providers" placeholder="Search providers…" value={query} onChange={(e) => setQuery(e.target.value)} />
    <div role="group" aria-label="Choose AI provider" className="grid grid-cols-2 gap-3 sm:grid-cols-2">
      {providers.map((p) => (
        <button type="button" key={p.key} aria-pressed={value === p.key} disabled={locked && value !== p.key} onClick={() => onChange(p.key)}
          className={cn("flex min-w-0 items-center gap-3 rounded-xl border p-4 text-left transition-colors disabled:opacity-40 disabled:cursor-not-allowed",
            value === p.key ? "border-primary/60 bg-primary/10" : "border-border/60 bg-card hover:bg-surface-raised")}>
          <LoginBrandMark provider={p.key} size="md" />
          <span className="min-w-0"><span className="block type-row font-medium">{p.label}</span>
            <span className="block type-meta text-muted-foreground">{p.detail}</span></span>
        </button>
      ))}
    </div>
    {providers.length === 0 && <p className="text-sm text-muted-foreground">No matching provider.</p>}</div>
  )
}
