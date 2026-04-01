import { Folder, Database, Bug, Search } from "lucide-react"
import {
  SiGithub, SiGooglecloud, SiSlack, SiLinear, SiNotion,
  SiStripe, SiSupabase, SiDatadog, SiCloudflare, SiGitlab, SiSentry,
} from "react-icons/si"
import type { MCPTemplate } from "../types"

// --- Provider imports (add new providers here) ---
import { github } from "./providers/github"
import { googleWorkspace } from "./providers/google-workspace"
import { slack } from "./providers/slack"
import { filesystem } from "./providers/filesystem"
import { postgres } from "./providers/postgres"
import { sentry } from "./providers/sentry"
import { braveSearch } from "./providers/brave-search"
import { supabase } from "./providers/supabase"
import { linear } from "./providers/linear"
import { notion } from "./providers/notion"
import { stripe } from "./providers/stripe"
import { datadog } from "./providers/datadog"
import { cloudflare } from "./providers/cloudflare"
import { gitlab } from "./providers/gitlab"

// ---------------------------------------------------------------------------
// Template registry — order determines display order in the UI
// ---------------------------------------------------------------------------

export const MCP_TEMPLATES: MCPTemplate[] = [
  github,
  googleWorkspace,
  slack,
  braveSearch,
  linear,
  notion,
  stripe,
  supabase,
  postgres,
  datadog,
  filesystem,
  sentry,
  cloudflare,
  gitlab,
]

// ---------------------------------------------------------------------------
// Icon map — maps template icon strings to lucide-react or react-icons components.
// Brand icons use react-icons/si (Simple Icons), generic ones use lucide-react.
// ---------------------------------------------------------------------------

export const TEMPLATE_ICONS: Record<string, React.ComponentType<{ className?: string }>> = {
  // Brand icons (react-icons/si)
  github: SiGithub,
  "google-workspace": SiGooglecloud,
  slack: SiSlack,
  linear: SiLinear,
  notion: SiNotion,
  stripe: SiStripe,
  supabase: SiSupabase,
  datadog: SiDatadog,
  cloudflare: SiCloudflare,
  gitlab: SiGitlab,
  sentry: SiSentry,
  // Generic icons (lucide-react)
  search: Search,
  folder: Folder,
  database: Database,
  bug: Bug,
}
