"use client"

import * as React from "react"
import {
  SiGithub, SiGooglecloud, SiSlack, SiLinear, SiNotion,
  SiStripe, SiSupabase, SiDatadog, SiCloudflare, SiGitlab, SiSentry,
  SiPostgresql, SiBrave, SiAirtable, SiZendesk, SiHubspot, SiTwilio,
  SiOpenai, SiAnthropic, SiVercel, SiDocker, SiKubernetes,
  SiJira, SiAsana, SiTrello, SiDiscord, SiTelegram,
} from "react-icons/si"
import { Folder, Database, Bug, Search, Plug, Globe, Terminal, Cloud } from "lucide-react"

// Lookup map: registry name (lower-case) → icon component. Falls back
// to a transport-shaped generic when no brand is known. The key is
// matched case-insensitively against several substrings of the
// registry entry's `name` so we don't have to maintain an exact
// mapping for every variant.

const LOGO_MAP: Record<string, React.ComponentType<{ className?: string }>> = {
  github: SiGithub,
  "google-workspace": SiGooglecloud,
  google: SiGooglecloud,
  slack: SiSlack,
  linear: SiLinear,
  notion: SiNotion,
  stripe: SiStripe,
  supabase: SiSupabase,
  datadog: SiDatadog,
  cloudflare: SiCloudflare,
  gitlab: SiGitlab,
  sentry: SiSentry,
  postgres: SiPostgresql,
  postgresql: SiPostgresql,
  brave: SiBrave,
  "brave-search": SiBrave,
  airtable: SiAirtable,
  zendesk: SiZendesk,
  hubspot: SiHubspot,
  twilio: SiTwilio,
  openai: SiOpenai,
  anthropic: SiAnthropic,
  vercel: SiVercel,
  aws: Cloud,
  amazon: Cloud,
  docker: SiDocker,
  kubernetes: SiKubernetes,
  k8s: SiKubernetes,
  jira: SiJira,
  asana: SiAsana,
  trello: SiTrello,
  discord: SiDiscord,
  telegram: SiTelegram,
  // Lucide fallbacks for generic registry icons
  search: Search,
  folder: Folder,
  filesystem: Folder,
  database: Database,
  bug: Bug,
}

export interface MCPLogoProps {
  /** Either the registry icon string, or the server name (we'll try both). */
  name: string
  /** Optional transport hint — used as fallback when no brand match. */
  transport?: string
  className?: string
}

export function MCPLogo({ name, transport, className }: MCPLogoProps) {
  const key = (name ?? "").toLowerCase()
  // Direct hit first.
  if (LOGO_MAP[key]) {
    const Icon = LOGO_MAP[key]
    return <Icon className={className} />
  }
  // Substring match (e.g. "github-server" -> SiGithub)
  for (const k of Object.keys(LOGO_MAP)) {
    if (key.includes(k)) {
      const Icon = LOGO_MAP[k]
      return <Icon className={className} />
    }
  }
  // Generic transport fallback
  if (transport === "stdio") return <Terminal className={className} />
  if (transport === "streamable-http" || transport === "http" || transport === "sse") return <Globe className={className} />
  return <Plug className={className} />
}
