"use client"

import { SidebarRow, SidebarSection } from "@/components/layout/sidebar-kit"
import { LOGIN_PROVIDERS } from "@/lib/credentials/login-providers"
import { LoginBrandMark } from "./provider-login-bits"

export function ProviderFilterSection({ providers, selected, onSelect }: {
  providers: { value: string; label: string; count: number }[]
  selected: string[]
  onSelect: (key: string) => void
}) {
  const known = new Map(providers.map((p) => [p.value, p]))
  const options = LOGIN_PROVIDERS.map((p) => known.get(p.key) ?? { value: p.key, label: p.label, count: 0 })
  options.push(...providers.filter((p) => !LOGIN_PROVIDERS.some((known) => known.key === p.value)))
  return <SidebarSection label="Providers" count={providers.reduce((sum, p) => sum + p.count, 0)} className="border-b border-white/[0.06]">
    <SidebarRow selected={selected.length === 0} onSelect={() => onSelect("")}>
      <span className="min-w-0 flex-1">All providers</span>
      <span className="type-meta tabular-nums text-muted-foreground">{providers.reduce((sum, p) => sum + p.count, 0)}</span>
    </SidebarRow>
    <div className="max-h-40 overflow-y-auto">
      {options.map((p) => <SidebarRow key={p.value} selected={selected.includes(p.value)} onSelect={() => onSelect(p.value)}>
        <LoginBrandMark provider={p.value} size="sm" />
        <span className="min-w-0 flex-1 truncate">{p.label}</span>
        <span className="type-meta tabular-nums text-muted-foreground" aria-label={`${p.count} connected accounts`}>{p.count}</span>
      </SidebarRow>)}
    </div>
  </SidebarSection>
}
