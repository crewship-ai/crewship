"use client"

import { LOGIN_PROVIDERS } from "@/lib/credentials/login-providers"
import { LoginBrandMark } from "./provider-login-bits"
import { cn } from "@/lib/utils"

export function LoginProviderPicker({ value, onChange }: { value: string; onChange: (key: string) => void }) {
  return (
    <div role="group" aria-label="Choose AI provider" className="grid grid-cols-2 gap-2 sm:grid-cols-3">
      {LOGIN_PROVIDERS.map((p) => (
        <button type="button" key={p.key} aria-pressed={value === p.key} onClick={() => onChange(p.key)}
          className={cn("flex min-w-0 items-start gap-2 rounded-xl border p-3 text-left transition-colors",
            value === p.key ? "border-primary/60 bg-primary/10" : "border-border/60 bg-card hover:bg-surface-raised")}>
          <LoginBrandMark provider={p.key} size="sm" />
          <span className="min-w-0"><span className="block type-row font-medium">{p.label}</span>
            <span className="block type-meta text-muted-foreground">{p.detail}</span></span>
        </button>
      ))}
    </div>
  )
}
